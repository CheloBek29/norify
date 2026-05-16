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

func TestWorkerPoolDefaultsToOneWorkerPerContainer(t *testing.T) {
	t.Setenv("WORKER_MIN_POOL", "")
	t.Setenv("WORKER_MAX_POOL", "")

	pool := newWorkerPool()

	if pool.min != 1 {
		t.Fatalf("default min pool should keep one worker per container, got %d", pool.min)
	}
	if pool.max != 1 {
		t.Fatalf("default max pool should keep one worker per container, got %d", pool.max)
	}
	if target := pool.targetSize(1000); target != 1 {
		t.Fatalf("queue depth must scale containers, not in-process workers; got target %d", target)
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
