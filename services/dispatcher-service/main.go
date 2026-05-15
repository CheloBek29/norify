package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/norify/platform/packages/contracts"
	"github.com/norify/platform/packages/go-common/campaigns"
	"github.com/norify/platform/packages/go-common/httpapi"
	appruntime "github.com/norify/platform/packages/go-common/runtime"
	amqp "github.com/rabbitmq/amqp091-go"
)

type dispatchRequest struct {
	UserIDs   []string `json:"user_ids"`
	Channels  []string `json:"channels"`
	BatchSize int      `json:"batch_size"`
}

var mq *amqp.Channel

func main() {
	if conn, channel, err := appruntime.OpenRabbit(); err == nil {
		defer conn.Close()
		defer channel.Close()
		mq = channel
		go consumeCampaignDispatch(channel)
	} else {
		appruntime.LogStartup("dispatcher-service rabbitmq", err)
	}

	mux := httpapi.NewMux(httpapi.Service{Name: "dispatcher-service", Version: "0.2.0", Ready: func() bool { return mq != nil }})
	mux.HandleFunc("/dispatch/preview", previewDispatch)
	_ = httpapi.Listen("dispatcher-service", mux)
}

func previewDispatch(w http.ResponseWriter, r *http.Request) {
	var req dispatchRequest
	if err := httpapi.ReadJSON(r, &req); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, campaigns.BuildDispatchBatches(req.UserIDs, req.Channels, req.BatchSize))
}

func consumeCampaignDispatch(channel *amqp.Channel) {
	_ = channel.Qos(1, 0, false)
	deliveries, err := channel.Consume(appruntime.QueueCampaignDispatch, "dispatcher-service", false, false, false, false, nil)
	if err != nil {
		slog.Error("consume campaign dispatch", "error", err)
		return
	}
	for delivery := range deliveries {
		var req contracts.CampaignDispatchRequest
		if err := appruntime.DecodeJSON(delivery, &req); err != nil {
			_ = delivery.Nack(false, false)
			continue
		}
		start := time.Now()
		active, err := campaignDispatchActive(context.Background(), req.CampaignID)
		if err != nil {
			slog.Warn("check campaign dispatch status", "campaign_id", req.CampaignID, "error", err)
			_ = delivery.Nack(false, true)
			continue
		}
		if !active {
			_ = delivery.Ack(false)
			continue
		}
		maxQueueDepth := appruntime.EnvInt("DISPATCH_MAX_SEND_QUEUE_DEPTH", 300)
		if throttled, err := dispatchBackpressure(channel, maxQueueDepth); err != nil {
			slog.Warn("inspect send queue", "error", err)
		} else if throttled {
			delay := time.Duration(appruntime.EnvInt("DISPATCH_CONTINUATION_DELAY_MS", 250)) * time.Millisecond
			time.Sleep(delay)
			if err := appruntime.PublishJSON(context.Background(), channel, "", appruntime.QueueCampaignDispatch, req); err != nil {
				slog.Error("requeue throttled campaign dispatch", "campaign_id", req.CampaignID, "error", err)
				_ = delivery.Nack(false, true)
				continue
			}
			_ = delivery.Ack(false)
			continue
		}
		maxMessages := appruntime.EnvInt("DISPATCH_MAX_MESSAGES_PER_TICK", 90)
		window := dispatchRecipientWindow(req, maxMessages)
		samples, err := dispatch(context.Background(), channel, req, window)
		if err != nil {
			slog.Error("dispatch campaign", "campaign_id", req.CampaignID, "error", err)
			_ = delivery.Nack(false, true)
			continue
		}
		p95 := campaigns.DispatchP95Milliseconds(samples)
		if err := reportDispatchMetrics(context.Background(), req, p95, len(samples), time.Since(start)); err != nil {
			slog.Warn("report dispatch metrics", "campaign_id", req.CampaignID, "error", err)
		}
		if window.NextStart > 0 {
			req.StartRecipient = window.NextStart
			delay := time.Duration(appruntime.EnvInt("DISPATCH_CONTINUATION_DELAY_MS", 250)) * time.Millisecond
			time.Sleep(delay)
			if err := appruntime.PublishJSON(context.Background(), channel, "", appruntime.QueueCampaignDispatch, req); err != nil {
				slog.Error("requeue campaign dispatch continuation", "campaign_id", req.CampaignID, "error", err)
				_ = delivery.Nack(false, true)
				continue
			}
		}
		_ = delivery.Ack(false)
		slog.Info("campaign dispatch tick", "campaign_id", req.CampaignID, "recipients", window.End-window.Start+1, "next_start", window.NextStart, "duration_ms", time.Since(start).Milliseconds(), "p95_dispatch_ms", p95)
	}
}

func campaignDispatchActive(ctx context.Context, campaignID string) (bool, error) {
	if campaignID == "" {
		return false, nil
	}
	baseURL := appruntime.Env("CAMPAIGN_SERVICE_URL", "http://campaign-service:8080")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/campaigns/"+campaignID, nil)
	if err != nil {
		return false, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return false, errors.New(response.Status)
	}
	var campaign struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&campaign); err != nil {
		return false, err
	}
	return canDispatchCampaignStatus(campaign.Status), nil
}

func canDispatchCampaignStatus(status string) bool {
	return status == campaigns.StatusRunning || status == campaigns.StatusRetrying
}

func dispatchBackpressure(channel *amqp.Channel, maxQueueDepth int) (bool, error) {
	queue, err := channel.QueueInspect(appruntime.QueueMessageSend)
	if err != nil {
		return false, err
	}
	return shouldThrottleSendQueue(queue.Messages, maxQueueDepth), nil
}

func shouldThrottleSendQueue(messages, maxQueueDepth int) bool {
	if maxQueueDepth <= 0 {
		return false
	}
	return messages >= maxQueueDepth
}

type recipientWindow struct {
	Start     int
	End       int
	NextStart int
}

func dispatchRecipientWindow(req contracts.CampaignDispatchRequest, maxMessages int) recipientWindow {
	start := req.StartRecipient
	if start <= 0 {
		start = 1
	}
	channelsCount := len(req.SelectedChannels)
	if channelsCount <= 0 {
		channelsCount = 1
	}
	if maxMessages <= 0 {
		maxMessages = 90
	}
	recipients := maxMessages / channelsCount
	if recipients <= 0 {
		recipients = 1
	}
	end := start + recipients - 1
	if end > req.TotalRecipients {
		end = req.TotalRecipients
	}
	nextStart := 0
	if end < req.TotalRecipients {
		nextStart = end + 1
	}
	return recipientWindow{Start: start, End: end, NextStart: nextStart}
}

func dispatch(ctx context.Context, channel *amqp.Channel, req contracts.CampaignDispatchRequest, window recipientWindow) ([]time.Duration, error) {
	if req.BatchSize <= 0 {
		req.BatchSize = campaigns.DefaultDispatchBatchSize
	}
	samples := make([]time.Duration, 0, ((window.End-window.Start+1)/req.BatchSize)+1)
	for start := window.Start; start <= window.End; start += req.BatchSize {
		batchStartedAt := time.Now()
		end := start + req.BatchSize - 1
		if end > window.End {
			end = window.End
		}
		for i := start; i <= end; i++ {
			userID := userID(i)
			for _, channelCode := range req.SelectedChannels {
				send := contracts.SendMessageRequest{
					CampaignID:     req.CampaignID,
					UserID:         userID,
					ChannelCode:    channelCode,
					MessageBody:    req.MessageBody,
					Attempt:        1,
					IdempotencyKey: campaigns.IdempotencyKey(req.CampaignID, userID, channelCode),
				}
				if err := appruntime.PublishJSON(ctx, channel, "", appruntime.QueueMessageSend, send); err != nil {
					return samples, err
				}
			}
		}
		samples = append(samples, time.Since(batchStartedAt))
	}
	return samples, nil
}

func reportDispatchMetrics(ctx context.Context, req contracts.CampaignDispatchRequest, p95, batchCount int, duration time.Duration) error {
	if p95 <= 0 {
		return nil
	}
	payload := contracts.CampaignDispatchMetrics{
		CampaignID:    req.CampaignID,
		TotalMessages: req.TotalRecipients * len(req.SelectedChannels),
		BatchCount:    batchCount,
		DurationMs:    int(duration.Milliseconds()),
		P95DispatchMs: p95,
		ReportedAt:    time.Now().UTC(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	baseURL := appruntime.Env("CAMPAIGN_SERVICE_URL", "http://campaign-service:8080")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/campaigns/"+req.CampaignID+"/dispatch-metrics", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return errors.New(response.Status)
	}
	return nil
}

func userID(n int) string {
	const digits = "00000"
	value := digits + strconv.Itoa(n)
	return "user-" + value[len(value)-5:]
}
