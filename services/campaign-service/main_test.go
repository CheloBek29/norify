package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSwitchChannelDisabled(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/campaigns/cmp-1/switch-channel", nil)
	rec := httptest.NewRecorder()

	switchChannel(rec, req, "cmp-1")

	if rec.Code != http.StatusConflict {
		t.Fatalf("switchChannel status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "campaign_switch_channel_disabled") {
		t.Fatalf("switchChannel response should explain disabled action, got %s", rec.Body.String())
	}
}

func TestCanStartCampaign(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{campaigns.StatusCreated, true},
		{campaigns.StatusStopped, true},
		{campaigns.StatusRunning, false},
		{campaigns.StatusRetrying, false},
		{campaigns.StatusFinished, false},
		{campaigns.StatusCancelled, false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := canStartCampaign(tt.status); got != tt.want {
				t.Fatalf("canStartCampaign(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}
