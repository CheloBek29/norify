package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/norify/platform/packages/contracts"
	"github.com/norify/platform/packages/go-common/campaigns"
	"github.com/norify/platform/packages/go-common/channels"
	"github.com/norify/platform/packages/go-common/httpapi"
	appruntime "github.com/norify/platform/packages/go-common/runtime"
	amqp "github.com/rabbitmq/amqp091-go"
)

var db *pgxpool.Pool

// pubOps is a shared publisher used by the HTTP handler.
// It is managed by the reconnect loop and accessed under pubMu.
var (
	pubOps *amqpChannelOps
	pubMu  sync.Mutex
)

const (
	freshSendPriority      uint8 = 5
	automaticRetryPriority uint8 = 1
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var err error
	db, err = appruntime.OpenPostgres(ctx)
	appruntime.LogStartup("sender-worker postgres", err)

	// Keep a dedicated publishing channel alive for the HTTP handler.
	go appruntime.RunWithReconnect(ctx, "sender-worker-pub", func(ctx context.Context, ch *amqp.Channel) error {
		pubMu.Lock()
		pubOps = newAMQPChannelOps(ch)
		pubMu.Unlock()
		defer func() {
			pubMu.Lock()
			pubOps = nil
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
	minWorkers := appruntime.EnvInt("WORKER_MIN_POOL", 1)
	maxWorkers := appruntime.EnvInt("WORKER_MAX_POOL", 20)
	if minWorkers < 1 {
		minWorkers = 1
	}
	if maxWorkers < minWorkers {
		maxWorkers = minWorkers
	}
	if strings.EqualFold(appruntime.Env("WORKER_CONTROL_MODE", ""), "kubernetes") {
		minWorkers = 1
		maxWorkers = minWorkers
	}
	return &workerPool{
		workers: make(map[int]context.CancelFunc),
		min:     minWorkers,
		max:     maxWorkers,
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

	p.mu.Lock()
	defer p.mu.Unlock()

	target := p.targetSizeLocked(depth)
	for len(p.workers) < target {
		p.launch(parentCtx)
	}
	for len(p.workers) > target {
		p.killOne()
	}
}

// targetSize maps queue depth to desired worker count using linear interpolation.
func (p *workerPool) targetSize(depth int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.targetSizeLocked(depth)
}

func (p *workerPool) targetSizeLocked(depth int) int {
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
	channelOps := newAMQPChannelOps(ch)
	prefetch := workerPrefetch()
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
				err := processWithPublisher(ctx, channelOps, req)
				results <- deliveryResult{delivery: d, request: req, err: err, requeue: err != nil}
			}(delivery)
		case result := <-results:
			inFlight--
			if result.err == nil {
				_ = channelOps.Ack(result.delivery)
			} else if result.requeue {
				slog.Error("process failed, requeuing",
					"campaign_id", result.request.CampaignID,
					"user_id", result.request.UserID,
					"channel", result.request.ChannelCode,
					"error", result.err)
				_ = channelOps.Nack(result.delivery, true)
			} else {
				_ = channelOps.Nack(result.delivery, false)
			}
		}
	}
}

func workerPrefetch() int {
	prefetch := appruntime.EnvInt("WORKER_PREFETCH", 20)
	if prefetch < 1 {
		return 1
	}
	return prefetch
}

type amqpChannelOps struct {
	ch *amqp.Channel
	mu sync.Mutex
}

func newAMQPChannelOps(ch *amqp.Channel) *amqpChannelOps {
	if ch == nil {
		return nil
	}
	return &amqpChannelOps{ch: ch}
}

func (ops *amqpChannelOps) Ack(delivery amqp.Delivery) error {
	ops.mu.Lock()
	defer ops.mu.Unlock()
	return delivery.Ack(false)
}

func (ops *amqpChannelOps) Nack(delivery amqp.Delivery, requeue bool) error {
	ops.mu.Lock()
	defer ops.mu.Unlock()
	return delivery.Nack(false, requeue)
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
	ops := pubOps
	pubMu.Unlock()
	if err := processWithPublisher(r.Context(), ops, req); err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "processed", "idempotency_key": req.IdempotencyKey})
}

// ---------------------------------------------------------------------------
// Core processing
// ---------------------------------------------------------------------------

func processWithChannel(ctx context.Context, ch *amqp.Channel, req contracts.SendMessageRequest) error {
	return processWithPublisher(ctx, newAMQPChannelOps(ch), req)
}

func processWithPublisher(ctx context.Context, publisher *amqpChannelOps, req contracts.SendMessageRequest) error {
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = campaigns.IdempotencyKey(req.CampaignID, req.UserID, req.ChannelCode)
	}
	locked, release := acquireDeliveryLock(ctx, req.IdempotencyKey)
	if !locked {
		slog.Info("skip duplicate delivery already locked", "campaign_id", req.CampaignID, "idempotency_key", req.IdempotencyKey, "attempt", req.Attempt)
		return nil
	}
	defer release()
	active, err := campaignProcessingActive(ctx, req.CampaignID)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	sendable, reason, err := shouldSendDelivery(ctx, req)
	if err != nil {
		return err
	}
	if !sendable {
		slog.Info("skip duplicate or stale delivery before provider send", "campaign_id", req.CampaignID, "idempotency_key", req.IdempotencyKey, "attempt", req.Attempt, "reason", reason)
		return nil
	}
	config := channelConfig(ctx, req.ChannelCode)
	registry := channels.NewRegistry([]channels.Config{config})
	result := registry.Adapter(req.ChannelCode).Send(ctx, channels.Message{RecipientID: req.UserID, Body: req.MessageBody})
	status := "sent"
	if result.Status == channels.StatusFailed {
		status = "failed"
	}
	writeResult, err := writeDelivery(ctx, req, status, result)
	if err != nil {
		return err
	}
	var progressEvent *contracts.CampaignProgressEvent
	if writeResult.Applied {
		snapshot, err := refreshCampaignCounters(ctx, req.CampaignID)
		if err != nil {
			return err
		}
		event := campaignProgressEvent(snapshot)
		progressEvent = &event
	}
	if publisher == nil {
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
	if err := publishDeliverySideEffects(ctx, publisher, req, status, config, eventType, event, progressEvent); err != nil {
		return acknowledgePostCommitPublishError(req, err)
	}
	return nil
}

type failureRoute struct {
	queue    string
	priority uint8
	request  contracts.SendMessageRequest
}

func publishDeliverySideEffects(ctx context.Context, publisher *amqpChannelOps, req contracts.SendMessageRequest, status string, config channels.Config, eventType string, event contracts.MessageStatusEvent, progressEvent *contracts.CampaignProgressEvent) error {
	var firstErr error
	rememberErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	rememberErr(publisher.PublishJSON(ctx, appruntime.ExchangeMessageStatus, eventType, 0, event))
	if status == "failed" {
		if route, ok := nextFailureRoute(req, config); ok {
			rememberErr(publisher.PublishJSON(ctx, "", route.queue, route.priority, route.request))
		}
	}
	if progressEvent != nil {
		rememberErr(publisher.PublishJSON(ctx, appruntime.ExchangeCampaignStatus, "campaign.progress", 0, *progressEvent))
	}
	return firstErr
}

func nextFailureRoute(req contracts.SendMessageRequest, config channels.Config) (failureRoute, bool) {
	if req.Attempt < config.RetryLimit {
		req.Attempt++
		return failureRoute{queue: appruntime.QueueMessageRetry, priority: automaticRetryPriority, request: req}, true
	}
	return failureRoute{queue: appruntime.QueueMessageDLQ, priority: automaticRetryPriority, request: req}, true
}

func acknowledgePostCommitPublishError(req contracts.SendMessageRequest, err error) error {
	if err != nil {
		slog.Error("delivery side-effect publish failed after db commit; acking processed delivery",
			"campaign_id", req.CampaignID,
			"user_id", req.UserID,
			"channel", req.ChannelCode,
			"idempotency_key", req.IdempotencyKey,
			"error", err)
	}
	return nil
}

func (ops *amqpChannelOps) PublishJSON(ctx context.Context, exchange, routingKey string, priority uint8, payload any) error {
	if ops == nil || ops.ch == nil {
		return nil
	}
	publishCtx, cancel := context.WithTimeout(ctx, workerPublishTimeout())
	defer cancel()
	ops.mu.Lock()
	defer ops.mu.Unlock()
	return appruntime.PublishJSONPriority(publishCtx, ops.ch, exchange, routingKey, priority, payload)
}

func workerPublishTimeout() time.Duration {
	milliseconds := appruntime.EnvInt("WORKER_PUBLISH_TIMEOUT_MS", 2000)
	if milliseconds <= 0 {
		milliseconds = 2000
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// ---------------------------------------------------------------------------
// DB helpers
// ---------------------------------------------------------------------------

func shouldSendDelivery(ctx context.Context, req contracts.SendMessageRequest) (bool, string, error) {
	if db == nil || req.IdempotencyKey == "" {
		return true, "no_db_state", nil
	}
	var previousStatus string
	var previousAttempt int
	err := db.QueryRow(ctx, `SELECT status, attempt FROM message_deliveries WHERE idempotency_key = $1`, req.IdempotencyKey).Scan(&previousStatus, &previousAttempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, "new_delivery", nil
	}
	if err != nil {
		return false, "", err
	}
	if canSendDelivery(previousStatus, previousAttempt, req.Attempt) {
		return true, "sendable_state", nil
	}
	return false, "status=" + previousStatus, nil
}

func canSendDelivery(previousStatus string, previousAttempt, nextAttempt int) bool {
	if previousStatus == "" {
		return true
	}
	if previousStatus == "queued" {
		return nextAttempt > previousAttempt
	}
	if previousStatus == "failed" {
		return nextAttempt > previousAttempt
	}
	return false
}

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

func deliveryLockKey(idempotencyKey string) string {
	return "delivery-lock:" + idempotencyKey
}

func deliveryLockTTL() time.Duration {
	seconds := appruntime.EnvInt("DELIVERY_LOCK_TTL_SECONDS", 30)
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

func acquireDeliveryLock(ctx context.Context, idempotencyKey string) (bool, func()) {
	if idempotencyKey == "" {
		return true, func() {}
	}
	client, err := appruntime.NewRedisClientFromEnv()
	if err != nil {
		return true, func() {}
	}
	key := deliveryLockKey(idempotencyKey)
	value := newID("worker-lock")
	acquired, err := client.SetNXEX(ctx, key, deliveryLockTTL(), value)
	if err != nil {
		return true, func() {}
	}
	if !acquired {
		return false, func() {}
	}
	return true, func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.DelIfValue(releaseCtx, key, value)
	}
}

type channelConfigCacheEntry struct {
	config    channels.Config
	expiresAt time.Time
}

var channelConfigCache = struct {
	sync.Mutex
	items map[string]channelConfigCacheEntry
}{items: map[string]channelConfigCacheEntry{}}

func channelConfig(ctx context.Context, code string) channels.Config {
	if config, ok := cachedLocalChannelConfig(code); ok {
		return config
	}
	fallback := defaultChannelConfig(code)
	if config, ok := cachedRedisChannelConfig(ctx, code, fallback); ok {
		rememberLocalChannelConfig(code, config)
		return config
	}
	config, loadedFromPostgres := postgresChannelConfig(ctx, code, fallback)
	rememberLocalChannelConfig(code, config)
	if loadedFromPostgres {
		cacheRedisChannelConfig(ctx, config)
	}
	return config
}

func defaultChannelConfig(code string) channels.Config {
	minDelay := time.Duration(appruntime.EnvInt("CHANNEL_STUB_MIN_DELAY_SECONDS", 2)) * time.Second
	maxDelay := time.Duration(appruntime.EnvInt("CHANNEL_STUB_MAX_DELAY_SECONDS", 300)) * time.Second
	return channels.Config{Code: code, Enabled: true, SuccessProbability: 0.92, MinDelay: minDelay, MaxDelay: maxDelay, MaxParallelism: 100, RetryLimit: 3}
}

func postgresChannelConfig(ctx context.Context, code string, fallback channels.Config) (channels.Config, bool) {
	if db == nil {
		return fallback, false
	}
	config := fallback
	var minSeconds, maxSeconds int
	err := db.QueryRow(ctx, `
		SELECT success_probability::float8, min_delay_seconds, max_delay_seconds, max_parallelism, retry_limit
		FROM channels
		WHERE code = $1 AND enabled = true`, code).Scan(&config.SuccessProbability, &minSeconds, &maxSeconds, &config.MaxParallelism, &config.RetryLimit)
	if err != nil {
		return fallback, false
	}
	config.MinDelay = time.Duration(appruntime.EnvInt("CHANNEL_STUB_MIN_DELAY_SECONDS", minSeconds)) * time.Second
	config.MaxDelay = time.Duration(appruntime.EnvInt("CHANNEL_STUB_MAX_DELAY_SECONDS", maxSeconds)) * time.Second
	return config, true
}

func channelConfigCacheKey(code string) string {
	return "channel-config:" + code
}

func channelConfigLocalTTL() time.Duration {
	seconds := appruntime.EnvInt("CHANNEL_CONFIG_LOCAL_TTL_SECONDS", 5)
	if seconds <= 0 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

func channelConfigRedisTTL() time.Duration {
	seconds := appruntime.EnvInt("CHANNEL_CONFIG_CACHE_TTL_SECONDS", 60)
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func cachedLocalChannelConfig(code string) (channels.Config, bool) {
	channelConfigCache.Lock()
	defer channelConfigCache.Unlock()
	entry, ok := channelConfigCache.items[code]
	if !ok {
		return channels.Config{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(channelConfigCache.items, code)
		return channels.Config{}, false
	}
	return entry.config, true
}

func rememberLocalChannelConfig(code string, config channels.Config) {
	channelConfigCache.Lock()
	defer channelConfigCache.Unlock()
	channelConfigCache.items[code] = channelConfigCacheEntry{config: config, expiresAt: time.Now().Add(channelConfigLocalTTL())}
}

func resetLocalChannelConfigCache() {
	channelConfigCache.Lock()
	defer channelConfigCache.Unlock()
	channelConfigCache.items = map[string]channelConfigCacheEntry{}
}

func cachedRedisChannelConfig(ctx context.Context, code string, fallback channels.Config) (channels.Config, bool) {
	client, err := appruntime.NewRedisClientFromEnv()
	if err != nil {
		return channels.Config{}, false
	}
	raw, ok, err := client.Get(ctx, channelConfigCacheKey(code))
	if err != nil || !ok {
		return channels.Config{}, false
	}
	var cached contracts.WorkerChannelConfig
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		return channels.Config{}, false
	}
	return configFromWorkerCache(cached, fallback), true
}

func cacheRedisChannelConfig(ctx context.Context, config channels.Config) {
	client, err := appruntime.NewRedisClientFromEnv()
	if err != nil {
		return
	}
	payload := contracts.WorkerChannelConfig{
		Code:               config.Code,
		Enabled:            config.Enabled,
		SuccessProbability: config.SuccessProbability,
		MinDelaySeconds:    int(config.MinDelay / time.Second),
		MaxDelaySeconds:    int(config.MaxDelay / time.Second),
		MaxParallelism:     config.MaxParallelism,
		RetryLimit:         config.RetryLimit,
		Source:             "postgres",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = client.SetEX(ctx, channelConfigCacheKey(config.Code), channelConfigRedisTTL(), string(body))
}

func configFromWorkerCache(cached contracts.WorkerChannelConfig, fallback channels.Config) channels.Config {
	if cached.Code == "" {
		cached.Code = fallback.Code
	}
	if cached.SuccessProbability <= 0 {
		cached.SuccessProbability = fallback.SuccessProbability
	}
	if cached.MinDelaySeconds <= 0 {
		cached.MinDelaySeconds = int(fallback.MinDelay / time.Second)
	}
	if cached.MaxDelaySeconds <= 0 {
		cached.MaxDelaySeconds = int(fallback.MaxDelay / time.Second)
	}
	if cached.MaxParallelism <= 0 {
		cached.MaxParallelism = fallback.MaxParallelism
	}
	if cached.RetryLimit <= 0 {
		cached.RetryLimit = fallback.RetryLimit
	}
	minSeconds := appruntime.EnvInt("CHANNEL_STUB_MIN_DELAY_SECONDS", cached.MinDelaySeconds)
	maxSeconds := appruntime.EnvInt("CHANNEL_STUB_MAX_DELAY_SECONDS", cached.MaxDelaySeconds)
	return channels.Config{
		Code:               cached.Code,
		Enabled:            cached.Enabled,
		SuccessProbability: cached.SuccessProbability,
		MinDelay:           time.Duration(minSeconds) * time.Second,
		MaxDelay:           time.Duration(maxSeconds) * time.Second,
		MaxParallelism:     cached.MaxParallelism,
		RetryLimit:         cached.RetryLimit,
	}
}

type deliveryWriteResult struct {
	Applied        bool
	PreviousStatus string
}

type campaignCounterSnapshot struct {
	CampaignID    string
	Status        string
	TotalMessages int
	Processed     int
	Success       int
	Failed        int
	Cancelled     int
	P95DispatchMs int
}

func writeDelivery(ctx context.Context, req contracts.SendMessageRequest, status string, result channels.Result) (deliveryWriteResult, error) {
	var previousStatus string
	var previousAttempt int
	err := db.QueryRow(ctx, `SELECT status, attempt FROM message_deliveries WHERE idempotency_key = $1`, req.IdempotencyKey).Scan(&previousStatus, &previousAttempt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return deliveryWriteResult{}, err
	}
	if err == nil && !canApplyDeliveryResult(previousStatus, previousAttempt, req.Attempt) {
		return deliveryWriteResult{PreviousStatus: previousStatus}, nil
	}

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
		WHERE message_deliveries.status = 'queued'
		   OR (message_deliveries.status = 'failed' AND EXCLUDED.attempt > message_deliveries.attempt)`,
		newID("delivery"), req.CampaignID, req.UserID, req.ChannelCode, req.MessageBody, status, result.ErrorCode,
		result.Error, req.Attempt, req.IdempotencyKey, result.FinishedAt)
	if err != nil {
		return deliveryWriteResult{}, err
	}
	return deliveryWriteResult{Applied: tag.RowsAffected() > 0, PreviousStatus: previousStatus}, nil
}

func canApplyDeliveryResult(previousStatus string, previousAttempt, nextAttempt int) bool {
	if previousStatus == "" || previousStatus == "queued" {
		return true
	}
	if previousStatus == "failed" {
		return nextAttempt > previousAttempt
	}
	return false
}

func refreshCampaignCounters(ctx context.Context, campaignID string) (campaignCounterSnapshot, error) {
	row := db.QueryRow(ctx, `
		WITH delivery_counts AS (
			SELECT
				(count(*) FILTER (WHERE status IN ('sent', 'failed', 'cancelled')))::int AS processed,
				(count(*) FILTER (WHERE status = 'sent'))::int AS success,
				(count(*) FILTER (WHERE status = 'failed'))::int AS failed,
				(count(*) FILTER (WHERE status = 'cancelled'))::int AS cancelled
			FROM message_deliveries
			WHERE campaign_id = $1
		),
		updated AS (
			UPDATE campaigns c
			SET sent_count = delivery_counts.processed,
			    success_count = delivery_counts.success,
			    failed_count = delivery_counts.failed,
			    cancelled_count = delivery_counts.cancelled,
			    status = CASE
			        WHEN delivery_counts.processed >= c.total_messages
			          AND c.total_messages > 0
			          AND c.status IN ($3, $4) THEN $2
			        ELSE c.status
			    END,
			    finished_at = CASE
			        WHEN delivery_counts.processed >= c.total_messages
			          AND c.total_messages > 0
			          AND c.status IN ($3, $4) THEN COALESCE(c.finished_at, now())
			        WHEN c.status IN ($3, $4) THEN NULL
			        ELSE c.finished_at
			    END,
			    updated_at = now()
			FROM delivery_counts
			WHERE c.id = $1
			RETURNING c.id, c.status, c.total_messages, c.sent_count, c.success_count, c.failed_count, c.cancelled_count, c.p95_dispatch_ms
		)
		SELECT id, status, total_messages, sent_count, success_count, failed_count, cancelled_count, p95_dispatch_ms
		FROM updated`, campaignID, campaigns.StatusFinished, campaigns.StatusRunning, campaigns.StatusRetrying)

	var snapshot campaignCounterSnapshot
	err := row.Scan(
		&snapshot.CampaignID,
		&snapshot.Status,
		&snapshot.TotalMessages,
		&snapshot.Processed,
		&snapshot.Success,
		&snapshot.Failed,
		&snapshot.Cancelled,
		&snapshot.P95DispatchMs,
	)
	return snapshot, err
}

func shouldFinishCampaign(status string, processed, totalMessages int) bool {
	return (status == campaigns.StatusRunning || status == campaigns.StatusRetrying) && totalMessages > 0 && processed >= totalMessages
}

func campaignProgressEvent(snapshot campaignCounterSnapshot) contracts.CampaignProgressEvent {
	progress := 0.0
	if snapshot.TotalMessages > 0 {
		progress = float64(snapshot.Processed) / float64(snapshot.TotalMessages) * 100
		if progress > 100 {
			progress = 100
		}
	}
	return contracts.CampaignProgressEvent{
		Type:            "campaign.progress",
		CampaignID:      snapshot.CampaignID,
		Status:          snapshot.Status,
		TotalMessages:   snapshot.TotalMessages,
		Processed:       snapshot.Processed,
		Success:         snapshot.Success,
		Failed:          snapshot.Failed,
		Cancelled:       snapshot.Cancelled,
		P95DispatchMs:   snapshot.P95DispatchMs,
		ProgressPercent: progress,
		UpdatedAt:       time.Now().UTC(),
	}
}

func newID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + "-" + hex.EncodeToString(buf)
}
