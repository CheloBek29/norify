package main

import "testing"

func TestWorkerCachePayloadFromChannel(t *testing.T) {
	payload := workerCachePayloadFromChannel(channelResponse{
		Code:               "email",
		Enabled:            true,
		SuccessProbability: 0.96,
		MinDelaySeconds:    2,
		MaxDelaySeconds:    60,
		MaxParallelism:     180,
		RetryLimit:         3,
	})

	if payload.Code != "email" || !payload.Enabled {
		t.Fatalf("unexpected code/enabled: %#v", payload)
	}
	if payload.SuccessProbability != 0.96 {
		t.Fatalf("success probability = %v", payload.SuccessProbability)
	}
	if payload.MinDelaySeconds != 2 || payload.MaxDelaySeconds != 60 {
		t.Fatalf("delay = %d..%d", payload.MinDelaySeconds, payload.MaxDelaySeconds)
	}
	if payload.MaxParallelism != 180 || payload.RetryLimit != 3 {
		t.Fatalf("parallelism/retry = %d/%d", payload.MaxParallelism, payload.RetryLimit)
	}
	if payload.Source != "channel-service" {
		t.Fatalf("source = %q", payload.Source)
	}
}
