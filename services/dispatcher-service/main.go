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
		samples, err := dispatch(context.Background(), channel, req)
		if err != nil {
			slog.Error("dispatch campaign", "campaign_id", req.CampaignID, "error", err)
			_ = delivery.Nack(false, true)
			continue
		}
		p95 := campaigns.DispatchP95Milliseconds(samples)
		if err := reportDispatchMetrics(context.Background(), req, p95, len(samples), time.Since(start)); err != nil {
			slog.Warn("report dispatch metrics", "campaign_id", req.CampaignID, "error", err)
		}
		_ = delivery.Ack(false)
		slog.Info("campaign dispatched", "campaign_id", req.CampaignID, "messages", req.TotalRecipients*len(req.SelectedChannels), "duration_ms", time.Since(start).Milliseconds(), "p95_dispatch_ms", p95)
	}
}

func dispatch(ctx context.Context, channel *amqp.Channel, req contracts.CampaignDispatchRequest) ([]time.Duration, error) {
	if req.BatchSize <= 0 {
		req.BatchSize = campaigns.DefaultDispatchBatchSize
	}
	samples := make([]time.Duration, 0, (req.TotalRecipients/req.BatchSize)+1)
	for start := 1; start <= req.TotalRecipients; start += req.BatchSize {
		batchStartedAt := time.Now()
		end := start + req.BatchSize - 1
		if end > req.TotalRecipients {
			end = req.TotalRecipients
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
