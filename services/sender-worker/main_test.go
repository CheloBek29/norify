package main

import (
	"testing"
	"time"

	"github.com/norify/platform/packages/contracts"
)

func TestCanProcessCampaignStatus(t *testing.T) {
	if !canProcessCampaignStatus("running") {
		t.Fatalf("running campaign should process sends")
	}
	if !canProcessCampaignStatus("retrying") {
		t.Fatalf("retrying campaign should process sends")
	}
	if canProcessCampaignStatus("stopped") {
		t.Fatalf("stopped campaign must not process queued sends")
	}
	if canProcessCampaignStatus("cancelled") {
		t.Fatalf("cancelled campaign must not process queued sends")
	}
	if canProcessCampaignStatus("finished") {
		t.Fatalf("finished campaign must not process queued sends")
	}
}

func TestWorkerPoolDefaultsScaleInsideContainer(t *testing.T) {
	t.Setenv("WORKER_CONTROL_MODE", "")
	t.Setenv("WORKER_MIN_POOL", "")
	t.Setenv("WORKER_MAX_POOL", "")

	pool := newWorkerPool()

	if pool.min != 1 {
		t.Fatalf("default min pool should keep one worker per container, got %d", pool.min)
	}
	if pool.max != 20 {
		t.Fatalf("default max pool should allow in-process scale-out, got %d", pool.max)
	}
	if target := pool.targetSize(1000); target != pool.max {
		t.Fatalf("queue depth must scale in-process workers to max; got target %d, max %d", target, pool.max)
	}
}

func TestWorkerPoolDoesNotInternallyScaleUnderKubernetesHPA(t *testing.T) {
	t.Setenv("WORKER_CONTROL_MODE", "kubernetes")
	t.Setenv("WORKER_MIN_POOL", "5")
	t.Setenv("WORKER_MAX_POOL", "20")

	pool := newWorkerPool()

	if pool.min != 1 || pool.max != 1 {
		t.Fatalf("kubernetes HPA mode must keep one in-process worker per pod, got %d..%d", pool.min, pool.max)
	}
	if target := pool.targetSize(1000); target != 1 {
		t.Fatalf("kubernetes HPA mode must not add in-process workers, got target %d", target)
	}
}

func TestWorkerPoolNormalizesInvalidBounds(t *testing.T) {
	t.Setenv("WORKER_CONTROL_MODE", "")
	t.Setenv("WORKER_MIN_POOL", "5")
	t.Setenv("WORKER_MAX_POOL", "2")

	pool := newWorkerPool()

	if pool.min != 5 || pool.max != 5 {
		t.Fatalf("pool bounds = %d..%d, want 5..5", pool.min, pool.max)
	}
}

func TestWorkerPrefetchDefaultsToUsefulParallelism(t *testing.T) {
	t.Setenv("WORKER_PREFETCH", "")

	if got := workerPrefetch(); got != 20 {
		t.Fatalf("workerPrefetch() = %d, want 20", got)
	}
}

func TestWorkerPrefetchClampsInvalidValues(t *testing.T) {
	t.Setenv("WORKER_PREFETCH", "0")

	if got := workerPrefetch(); got != 1 {
		t.Fatalf("workerPrefetch() = %d, want 1", got)
	}
}

func TestPostCommitPublishFailureDoesNotRequeueProcessedDelivery(t *testing.T) {
	if err := acknowledgePostCommitPublishError(contracts.SendMessageRequest{CampaignID: "cmp-1", IdempotencyKey: "key-1"}, assertErr("rabbit publish failed")); err != nil {
		t.Fatalf("post-commit publish failure must ack processed delivery, got %v", err)
	}
}

func TestFailureRoutePrioritizesFreshCampaignTrafficOverRetries(t *testing.T) {
	route, ok := nextFailureRoute(contracts.SendMessageRequest{Attempt: 1}, defaultChannelConfig("email"))

	if !ok {
		t.Fatalf("failed delivery should route to retry")
	}
	if route.queue != "message.send.retry" {
		t.Fatalf("route queue = %q, want retry", route.queue)
	}
	if route.priority >= freshSendPriority {
		t.Fatalf("retry priority %d must stay below fresh send priority %d", route.priority, freshSendPriority)
	}
	if route.request.Attempt != 2 {
		t.Fatalf("retry attempt = %d, want 2", route.request.Attempt)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestShouldFinishCampaignOnlyForActiveCompleteCampaigns(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		processed     int
		totalMessages int
		want          bool
	}{
		{name: "running complete finishes", status: "running", processed: 10, totalMessages: 10, want: true},
		{name: "retrying over complete finishes", status: "retrying", processed: 11, totalMessages: 10, want: true},
		{name: "running incomplete stays active", status: "running", processed: 9, totalMessages: 10, want: false},
		{name: "stopped complete is not overwritten", status: "stopped", processed: 10, totalMessages: 10, want: false},
		{name: "cancelled complete is not overwritten", status: "cancelled", processed: 10, totalMessages: 10, want: false},
		{name: "zero total does not finish", status: "running", processed: 1, totalMessages: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldFinishCampaign(tt.status, tt.processed, tt.totalMessages)
			if got != tt.want {
				t.Fatalf("shouldFinishCampaign(%q, %d, %d) = %v, want %v", tt.status, tt.processed, tt.totalMessages, got, tt.want)
			}
		})
	}
}

func TestCampaignProgressEventUsesDerivedCounters(t *testing.T) {
	event := campaignProgressEvent(campaignCounterSnapshot{
		CampaignID:    "cmp-1",
		Status:        "running",
		TotalMessages: 4,
		Processed:     3,
		Success:       2,
		Failed:        1,
		Cancelled:     0,
		P95DispatchMs: 12,
	})

	if event.Processed != 3 || event.Success != 2 || event.Failed != 1 || event.ProgressPercent != 75 {
		t.Fatalf("unexpected progress event: %#v", event)
	}
}

func TestCanApplyDeliveryResult(t *testing.T) {
	tests := []struct {
		name            string
		previousStatus  string
		previousAttempt int
		nextAttempt     int
		want            bool
	}{
		{name: "new delivery can be inserted", previousStatus: "", previousAttempt: 0, nextAttempt: 1, want: true},
		{name: "queued delivery can be completed", previousStatus: "queued", previousAttempt: 1, nextAttempt: 1, want: true},
		{name: "failed delivery can be repaired by higher attempt", previousStatus: "failed", previousAttempt: 1, nextAttempt: 2, want: true},
		{name: "failed delivery ignores stale same attempt", previousStatus: "failed", previousAttempt: 2, nextAttempt: 2, want: false},
		{name: "sent delivery is terminal", previousStatus: "sent", previousAttempt: 1, nextAttempt: 2, want: false},
		{name: "cancelled delivery is terminal", previousStatus: "cancelled", previousAttempt: 1, nextAttempt: 2, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canApplyDeliveryResult(tt.previousStatus, tt.previousAttempt, tt.nextAttempt)
			if got != tt.want {
				t.Fatalf("canApplyDeliveryResult(%q, %d, %d) = %v, want %v", tt.previousStatus, tt.previousAttempt, tt.nextAttempt, got, tt.want)
			}
		})
	}
}

func TestCanSendDelivery(t *testing.T) {
	tests := []struct {
		name            string
		previousStatus  string
		previousAttempt int
		nextAttempt     int
		want            bool
	}{
		{name: "new delivery can send", previousStatus: "", previousAttempt: 0, nextAttempt: 1, want: true},
		{name: "claimed retry can send when attempt advances", previousStatus: "queued", previousAttempt: 1, nextAttempt: 2, want: true},
		{name: "queued duplicate same attempt skips", previousStatus: "queued", previousAttempt: 2, nextAttempt: 2, want: false},
		{name: "failed retry can send when attempt advances", previousStatus: "failed", previousAttempt: 1, nextAttempt: 2, want: true},
		{name: "failed duplicate same attempt skips", previousStatus: "failed", previousAttempt: 2, nextAttempt: 2, want: false},
		{name: "sent terminal skips", previousStatus: "sent", previousAttempt: 1, nextAttempt: 2, want: false},
		{name: "cancelled terminal skips", previousStatus: "cancelled", previousAttempt: 1, nextAttempt: 2, want: false},
		{name: "processing owner skips conservative duplicate", previousStatus: "processing", previousAttempt: 1, nextAttempt: 1, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canSendDelivery(tt.previousStatus, tt.previousAttempt, tt.nextAttempt)
			if got != tt.want {
				t.Fatalf("canSendDelivery(%q, %d, %d) = %v, want %v", tt.previousStatus, tt.previousAttempt, tt.nextAttempt, got, tt.want)
			}
		})
	}
}

func TestChannelConfigCacheKey(t *testing.T) {
	if got := channelConfigCacheKey("telegram"); got != "channel-config:telegram" {
		t.Fatalf("channelConfigCacheKey = %q", got)
	}
}

func TestDeliveryLockKey(t *testing.T) {
	if got := deliveryLockKey("cmp:user:email"); got != "delivery-lock:cmp:user:email" {
		t.Fatalf("deliveryLockKey = %q", got)
	}
}

func TestConfigFromWorkerCacheUsesRedisPayload(t *testing.T) {
	fallback := defaultChannelConfig("telegram")
	got := configFromWorkerCache(contracts.WorkerChannelConfig{
		Code:               "telegram",
		Enabled:            true,
		SuccessProbability: 0.77,
		MinDelaySeconds:    4,
		MaxDelaySeconds:    9,
		MaxParallelism:     42,
		RetryLimit:         5,
		Source:             "redis",
	}, fallback)

	if got.Code != "telegram" || !got.Enabled {
		t.Fatalf("unexpected code/enabled: %#v", got)
	}
	if got.SuccessProbability != 0.77 {
		t.Fatalf("success probability = %v", got.SuccessProbability)
	}
	if got.MinDelay != 4*time.Second || got.MaxDelay != 9*time.Second {
		t.Fatalf("delay = %s..%s", got.MinDelay, got.MaxDelay)
	}
	if got.MaxParallelism != 42 || got.RetryLimit != 5 {
		t.Fatalf("parallelism/retry = %d/%d", got.MaxParallelism, got.RetryLimit)
	}
}

func TestLocalChannelConfigCacheExpires(t *testing.T) {
	resetLocalChannelConfigCache()
	t.Setenv("CHANNEL_CONFIG_LOCAL_TTL_SECONDS", "1")
	rememberLocalChannelConfig("email", defaultChannelConfig("email"))
	if _, ok := cachedLocalChannelConfig("email"); !ok {
		t.Fatalf("expected cached config")
	}
	channelConfigCache.items["email"] = channelConfigCacheEntry{expiresAt: time.Now().Add(-time.Second)}
	if _, ok := cachedLocalChannelConfig("email"); ok {
		t.Fatalf("expired config must miss")
	}
}
