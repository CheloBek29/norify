package main

import "testing"

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
