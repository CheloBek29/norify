package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
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

// pubCh is a shared channel used only for publishing outbound events and retries.
// It is managed by maintainPubChannel and accessed under pubMu.
var (
	pubCh *amqp.Channel
	pubMu sync.Mutex
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var err error
	db, err = appruntime.OpenPostgres(ctx)
	appruntime.LogStartup("sender-worker postgres", err)

	// Keep a dedicated publishing channel alive for HTTP handler and process().
	go appruntime.RunWithReconnect(ctx, "sender-worker-pub", func(ctx context.Context, ch *amqp.Channel) error {
		pubMu.Lock()
		pubCh = ch
		pubMu.Unlock()
		defer func() {
			pubMu.Lock()
			pubCh = nil
			pubMu.Unlock()
		}()
		<-ctx.Done()
		return nil
	})

	pool := newWorkerPool()
	go pool.run(ctx)

	mux := httpapi.NewMux(httpapi.Service{
		Name:    "sender-worker",
		Version: "0.3.0",
		Ready:   func() bool { return db != nil },
	})
	mux.HandleFunc("/worker/send", sendOnce)
	mux.HandleFunc("/worker/stats", pool.statsHandler)
	_ = httpapi.Listen("sender-worker", mux)
}

// ---------------------------------------------------------------------------
// Dynamic worker pool
// ---------------------------------------------------------------------------

type workerPool struct {
	workers map[int]context.CancelFunc
	mu      sync.Mutex
	nextID  int
	min     int
	max     int
}

func newWorkerPool() *workerPool {
	return &workerPool{
		workers: make(map[int]context.CancelFunc),
		min:     appruntime.EnvInt("WORKER_MIN_POOL", 2),
		max:     appruntime.EnvInt("WORKER_MAX_POOL", 10),
	}
}

func (p *workerPool) run(parentCtx context.Context) {
	p.mu.Lock()
	for len(p.workers) < p.min {
		p.launch(parentCtx)
	}
	p.mu.Unlock()

	interval := time.Duration(appruntime.EnvInt("WORKER_SCALE_INTERVAL_SECONDS", 5)) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-parentCtx.Done():
			p.mu.Lock()
			for _, cancel := range p.workers {
				cancel()
			}
			p.mu.Unlock()
			return
		case <-ticker.C:
			p.scale(parentCtx)
		}
	}
}

func (p *workerPool) scale(parentCtx context.Context) {
	depth := appruntime.QueueDepth(appruntime.QueueMessageSend)
	target := p.targetSize(depth)

	p.mu.Lock()
	defer p.mu.Unlock()

	for len(p.workers) < target {
		p.launch(parentCtx)
	}
	for len(p.workers) > target {
		p.killOne()
	}
}

// targetSize maps queue depth to desired worker count using linear interpolation.
func (p *workerPool) targetSize(depth int) int {
	if depth < 0 {
		// Management API unreachable — keep current count, but no less than min.
		if len(p.workers) > p.min {
			return len(p.workers)
		}
		return p.min
	}
	maxDepth := appruntime.EnvInt("WORKER_SCALE_MAX_DEPTH", 200)
	if maxDepth <= 0 {
		maxDepth = 200
	}
	target := p.min + (p.max-p.min)*depth/maxDepth
	if target < p.min {
		return p.min
	}
	if target > p.max {
		return p.max
	}
	return target
}

func (p *workerPool) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}

func (p *workerPool) statsHandler(w http.ResponseWriter, _ *http.Request) {
	depth := appruntime.QueueDepth(appruntime.QueueMessageSend)
	httpapi.WriteJSON(w, http.StatusOK, map[string]int{
		"active_workers": p.count(),
		"min_workers":    p.min,
		"max_workers":    p.max,
		"queue_depth":    depth,
	})
}

func (p *workerPool) launch(parentCtx context.Context) {
	id := p.nextID
	p.nextID++
	workerCtx, cancel := context.WithCancel(parentCtx)
	p.workers[id] = cancel

	name := fmt.Sprintf("sender-worker-%d", id)
	go func() {
		defer func() {
			p.mu.Lock()
			delete(p.workers, id)
			p.mu.Unlock()
		}()
		appruntime.RunWithReconnect(workerCtx, name, consumeMessages)
		slog.Info("worker stopped", "worker", name)
	}()
	slog.Info("worker started", "worker", name, "total", len(p.workers))
}

func (p *workerPool) killOne() {
	// Stop the highest-ID worker (most recently started).
	maxID := -1
	for id := range p.workers {
		if id > maxID {
			maxID = id
		}
	}
	if maxID >= 0 {
		p.workers[maxID]()
		delete(p.workers, maxID)
		slog.Info("worker scaled down", "worker_id", maxID, "remaining", len(p.workers))
	}
}

// ---------------------------------------------------------------------------
// Consumer
// ---------------------------------------------------------------------------

func consumeMessages(ctx context.Context, ch *amqp.Channel) error {
	prefetch := appruntime.EnvInt("WORKER_PREFETCH", 20)
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return fmt.Errorf("qos: %w", err)
	}
	deliveries, err := ch.Consume(appruntime.QueueMessageSend, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}

	type deliveryResult struct {
		delivery amqp.Delivery
		request  contracts.SendMessageRequest
		err      error
		requeue  bool
	}

	results := make(chan deliveryResult, prefetch)
	inFlight := 0

	for {
		var incoming <-chan amqp.Delivery
		if inFlight < prefetch {
			incoming = deliveries
		}

		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-incoming:
			if !ok {
				return errors.New("delivery channel closed")
			}
			inFlight++
			go func(d amqp.Delivery) {
				var req contracts.SendMessageRequest
				if err := appruntime.DecodeJSON(d, &req); err != nil {
					results <- deliveryResult{delivery: d, err: err}
					return
				}
				err := processWithChannel(ctx, ch, req)
				results <- deliveryResult{delivery: d, request: req, err: err, requeue: err != nil}
			}(delivery)
		case result := <-results:
			inFlight--
			if result.err == nil {
				_ = result.delivery.Ack(false)
			} else if result.requeue {
				slog.Error("process failed, requeuing",
					"campaign_id", result.request.CampaignID,
					"user_id", result.request.UserID,
					"channel", result.request.ChannelCode,
					"error", result.err)
				_ = result.delivery.Nack(false, true)
			} else {
				_ = result.delivery.Nack(false, false)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

func sendOnce(w http.ResponseWriter, r *http.Request) {
	var req contracts.SendMessageRequest
	if err := httpapi.ReadJSON(r, &req); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = campaigns.IdempotencyKey(req.CampaignID, req.UserID, req.ChannelCode)
	}
	pubMu.Lock()
	ch := pubCh
	pubMu.Unlock()
	if err := processWithChannel(r.Context(), ch, req); err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "processed", "idempotency_key": req.IdempotencyKey})
}

// ---------------------------------------------------------------------------
// Core processing
// ---------------------------------------------------------------------------

func processWithChannel(ctx context.Context, ch *amqp.Channel, req contracts.SendMessageRequest) error {
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = campaigns.IdempotencyKey(req.CampaignID, req.UserID, req.ChannelCode)
	}
	active, err := campaignProcessingActive(ctx, req.CampaignID)
	if err != nil {
		return err
	}
	if !active {
		return nil
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
	if ch == nil {
		return nil
	}
	eventType := "message.sent"
	if status == "failed" {
		eventType = "message.failed"
	}
	event := contracts.MessageStatusEvent{
		Type: eventType, CampaignID: req.CampaignID, TotalMessages: campaignTotal(ctx, req.CampaignID),
		UserID: req.UserID, ChannelCode: req.ChannelCode, Status: status,
		ErrorCode: result.ErrorCode, ErrorMessage: result.Error, Attempt: req.Attempt,
		IdempotencyKey: req.IdempotencyKey, FinishedAt: result.FinishedAt,
	}
	if err := appruntime.PublishJSON(ctx, ch, appruntime.ExchangeMessageStatus, eventType, event); err != nil {
		return err
	}
	if status == "failed" && req.Attempt < config.RetryLimit {
		req.Attempt++
		return appruntime.PublishJSON(ctx, ch, "", appruntime.QueueMessageRetry, req)
	}
	if status == "failed" {
		return appruntime.PublishJSON(ctx, ch, "", appruntime.QueueMessageDLQ, req)
	}
	return nil
}

// ---------------------------------------------------------------------------
// DB helpers
// ---------------------------------------------------------------------------

func campaignProcessingActive(ctx context.Context, campaignID string) (bool, error) {
	if db == nil || campaignID == "" {
		return true, nil
	}
	var status string
	if err := db.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1`, campaignID).Scan(&status); err != nil {
		return false, err
	}
	return canProcessCampaignStatus(status), nil
}

func canProcessCampaignStatus(status string) bool {
	return status == campaigns.StatusRunning || status == campaigns.StatusRetrying
}

func campaignTotal(ctx context.Context, campaignID string) int {
	if db == nil {
		return 0
	}
	var total int
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
