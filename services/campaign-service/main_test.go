package main

import (
	"testing"
	"time"

	"github.com/norify/platform/packages/go-common/campaigns"
)

func TestRollbackStatusAfterStartPublishFailure(t *testing.T) {
	startedAt := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		before      Campaign
		wantStatus  string
		wantStarted *time.Time
	}{
		{
			name:        "created campaign returns to draft state",
			before:      Campaign{Status: campaigns.StatusCreated},
			wantStatus:  campaigns.StatusCreated,
			wantStarted: nil,
		},
		{
			name:        "stopped campaign stays resumable",
			before:      Campaign{Status: campaigns.StatusStopped, StartedAt: &startedAt},
			wantStatus:  campaigns.StatusStopped,
			wantStarted: &startedAt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rollbackStateAfterStartFailure(tt.before)
			if got.Status != tt.wantStatus {
				t.Fatalf("unexpected rollback status: got %q want %q", got.Status, tt.wantStatus)
			}
			if tt.wantStarted == nil && got.StartedAt != nil {
				t.Fatalf("started_at must be cleared, got %v", got.StartedAt)
			}
			if tt.wantStarted != nil && (got.StartedAt == nil || !got.StartedAt.Equal(*tt.wantStarted)) {
				t.Fatalf("started_at must be preserved, got %v want %v", got.StartedAt, tt.wantStarted)
			}
		})
	}
}

func TestCanStartCampaign(t *testing.T) {
	if !canStartCampaign(campaigns.StatusCreated) {
		t.Fatalf("created campaign should be startable")
	}
	if !canStartCampaign(campaigns.StatusStopped) {
		t.Fatalf("stopped campaign should be resumable")
	}
	if canStartCampaign(campaigns.StatusFinished) {
		t.Fatalf("finished campaign must not be startable")
	}
	if canStartCampaign(campaigns.StatusCancelled) {
		t.Fatalf("cancelled campaign must not be startable")
	}
}
