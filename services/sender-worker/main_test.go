package main

import "testing"

func TestCanProcessCampaignStatus(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"running", true},
		{"retrying", true},
		{"created", false},
		{"stopped", false},
		{"cancelled", false},
		{"finished", false},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := canProcessCampaignStatus(tt.status); got != tt.want {
				t.Fatalf("canProcessCampaignStatus(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestSenderWorkerCurrentMainIdempotencyGuardKnownRisk(t *testing.T) {
	t.Skip("known main gap: processWithChannel calls the provider stub before checking terminal/stale delivery state; needs a DB-backed pre-send claim/guard")
}
