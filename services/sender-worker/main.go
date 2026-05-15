package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/norify/platform/packages/contracts"
	"github.com/norify/platform/packages/go-common/campaigns"
	"github.com/norify/platform/packages/go-common/channels"
	"github.com/norify/platform/packages/go-common/httpapi"
	appruntime "github.com/norify/platform/packages/go-common/runtime"
	amqp "github.com/rabbitmq/amqp091-go"
)

var db *pgxpool.Pool
var mq *amqp.Channel

func main() {
	ctx := context.Background()
	var err error
	db, err = appruntime.OpenPostgres(ctx)
	appruntime.LogStartup("sender-worker postgres", err)
	if conn, channel, qErr := appruntime.OpenRabbit(); qErr == nil {
		defer conn.Close()
		defer channel.Close()
		mq = channel
		go consumeMessages(channel)
	} else {
		appruntime.LogStartup("sender-worker rabbitmq", qErr)
	}

	mux := httpapi.NewMux(httpapi.Service{Name: "sender-worker", Version: "0.2.0", Ready: func() bool { return db != nil && mq != nil }})
	mux.HandleFunc("/worker/send", sendOnce)
	_ = httpapi.Listen("sender-worker", mux)
}

func consumeMessages(channel *amqp.Channel) {
	prefetch := appruntime.EnvInt("WORKER_PREFETCH", 20)
	_ = channel.Qos(prefetch, 0, false)
	deliveries, err := channel.Consume(appruntime.QueueMessageSend, "sender-worker", false, false, false, false, nil)
	if err != nil {
		slog.Error("consume message send", "error", err)
		return
	}
	for delivery := range deliveries {
		var req contracts.SendMessageRequest
		if err := appruntime.DecodeJSON(delivery, &req); err != nil {
			_ = delivery.Nack(false, false)
			continue
		}
		if err := process(context.Background(), req); err != nil {
			slog.Error("process send request", "campaign_id", req.CampaignID, "user_id", req.UserID, "channel", req.ChannelCode, "error", err)
			_ = delivery.Nack(false, true)
			continue
		}
		_ = delivery.Ack(false)
	}
}

func sendOnce(w http.ResponseWriter, r *http.Request) {
	var req contracts.SendMessageRequest
	if err := httpapi.ReadJSON(r, &req); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = campaigns.IdempotencyKey(req.CampaignID, req.UserID, req.ChannelCode)
	}
	if err := process(r.Context(), req); err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "processed", "idempotency_key": req.IdempotencyKey})
}

func process(ctx context.Context, req contracts.SendMessageRequest) error {
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = campaigns.IdempotencyKey(req.CampaignID, req.UserID, req.ChannelCode)
	}
	config := channelConfig(ctx, req.ChannelCode)
	registry := channels.NewRegistry([]channels.Config{config})
	result := registry.Adapter(req.ChannelCode).Send(ctx, channels.Message{RecipientID: req.UserID, Body: req.MessageBody})
	status := "sent"
	if result.Status == channels.StatusFailed {
		status = "failed"
	}
	inserted, err := writeDelivery(ctx, req, status, result)
	if err != nil {
		return err
	}
	if inserted {
		if err := updateCampaignCounters(ctx, req.CampaignID, status); err != nil {
			return err
		}
	}
	eventType := "message.sent"
	if status == "failed" {
		eventType = "message.failed"
	}
	event := contracts.MessageStatusEvent{
		Type: eventType, CampaignID: req.CampaignID, TotalMessages: campaignTotal(ctx, req.CampaignID), UserID: req.UserID, ChannelCode: req.ChannelCode,
		Status: status, ErrorCode: result.ErrorCode, ErrorMessage: result.Error, Attempt: req.Attempt,
		IdempotencyKey: req.IdempotencyKey, FinishedAt: result.FinishedAt,
	}
	if mq != nil {
		if err := appruntime.PublishJSON(ctx, mq, appruntime.ExchangeMessageStatus, eventType, event); err != nil {
			return err
		}
	}
	if status == "failed" && mq != nil && req.Attempt < config.RetryLimit {
		req.Attempt++
		return appruntime.PublishJSON(ctx, mq, "", appruntime.QueueMessageRetry, req)
	}
	if status == "failed" && mq != nil {
		return appruntime.PublishJSON(ctx, mq, "", appruntime.QueueMessageDLQ, req)
	}
	return nil
}

func campaignTotal(ctx context.Context, campaignID string) int {
	var total int
	if db == nil {
		return 0
	}
	_ = db.QueryRow(ctx, `SELECT total_messages FROM campaigns WHERE id = $1`, campaignID).Scan(&total)
	return total
}

func channelConfig(ctx context.Context, code string) channels.Config {
	minDelay := time.Duration(appruntime.EnvInt("CHANNEL_STUB_MIN_DELAY_SECONDS", 2)) * time.Second
	maxDelay := time.Duration(appruntime.EnvInt("CHANNEL_STUB_MAX_DELAY_SECONDS", 300)) * time.Second
	config := channels.Config{Code: code, Enabled: true, SuccessProbability: 0.92, MinDelay: minDelay, MaxDelay: maxDelay, MaxParallelism: 100, RetryLimit: 3}
	if db == nil {
		return config
	}
	var minSeconds, maxSeconds int
	err := db.QueryRow(ctx, `
		SELECT success_probability::float8, min_delay_seconds, max_delay_seconds, max_parallelism, retry_limit
		FROM channels
		WHERE code = $1 AND enabled = true`, code).Scan(&config.SuccessProbability, &minSeconds, &maxSeconds, &config.MaxParallelism, &config.RetryLimit)
	if err != nil {
		return config
	}
	config.MinDelay = time.Duration(appruntime.EnvInt("CHANNEL_STUB_MIN_DELAY_SECONDS", minSeconds)) * time.Second
	config.MaxDelay = time.Duration(appruntime.EnvInt("CHANNEL_STUB_MAX_DELAY_SECONDS", maxSeconds)) * time.Second
	return config
}

func writeDelivery(ctx context.Context, req contracts.SendMessageRequest, status string, result channels.Result) (bool, error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO message_deliveries (
		  id, campaign_id, user_id, channel_code, message_body, status, error_code,
		  error_message, attempt, idempotency_key, queued_at, sent_at, finished_at,
		  created_at, updated_at
		) VALUES (
		  $1, $2, $3, $4, $5, $6, $7,
		  $8, $9, $10, now(), now(), $11,
		  now(), now()
		)
		ON CONFLICT (idempotency_key) DO UPDATE
		SET status = EXCLUDED.status,
		    error_code = EXCLUDED.error_code,
		    error_message = EXCLUDED.error_message,
		    attempt = EXCLUDED.attempt,
		    sent_at = EXCLUDED.sent_at,
		    finished_at = EXCLUDED.finished_at,
		    updated_at = now()
		WHERE message_deliveries.status = 'queued'`,
		newID("delivery"), req.CampaignID, req.UserID, req.ChannelCode, req.MessageBody, status, result.ErrorCode,
		result.Error, req.Attempt, req.IdempotencyKey, result.FinishedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func updateCampaignCounters(ctx context.Context, campaignID, status string) error {
	if status == "sent" {
		_, err := db.Exec(ctx, `
			UPDATE campaigns
			SET sent_count = sent_count + 1,
			    success_count = success_count + 1,
			    status = CASE WHEN sent_count + 1 >= total_messages THEN $2 ELSE status END,
			    finished_at = CASE WHEN sent_count + 1 >= total_messages THEN now() ELSE finished_at END,
			    updated_at = now()
			WHERE id = $1`, campaignID, campaigns.StatusFinished)
		return err
	}
	_, err := db.Exec(ctx, `
		UPDATE campaigns
		SET sent_count = sent_count + 1,
		    failed_count = failed_count + 1,
		    status = CASE WHEN sent_count + 1 >= total_messages THEN $2 ELSE status END,
		    finished_at = CASE WHEN sent_count + 1 >= total_messages THEN now() ELSE finished_at END,
		    updated_at = now()
		WHERE id = $1`, campaignID, campaigns.StatusFinished)
	return err
}

func newID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + "-" + hex.EncodeToString(buf)
}
