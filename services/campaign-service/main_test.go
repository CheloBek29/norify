package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/norify/platform/packages/contracts"
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

func TestDispatchRecoveryCooldownDefaultsToLeaseLength(t *testing.T) {
	t.Setenv("DISPATCH_RECOVERY_STALE_SECONDS", "")

	if got := dispatchRecoveryCooldownSeconds(); got != 30 {
		t.Fatalf("dispatchRecoveryCooldownSeconds() = %d, want 30", got)
	}
}

func TestDispatchRecoveryCooldownClampsInvalidValues(t *testing.T) {
	t.Setenv("DISPATCH_RECOVERY_STALE_SECONDS", "0")

	if got := dispatchRecoveryCooldownSeconds(); got != 1 {
		t.Fatalf("dispatchRecoveryCooldownSeconds() = %d, want 1", got)
	}
}

func TestDispatchSendPriorityBoostsZeroProgressCampaigns(t *testing.T) {
	if got := dispatchSendPriority(0); got != 9 {
		t.Fatalf("zero-progress priority = %d, want 9", got)
	}
	if got := dispatchSendPriority(1); got != 5 {
		t.Fatalf("active campaign priority = %d, want 5", got)
	}
}

func TestDispatchRecipientWindowFairQuantum(t *testing.T) {
	window := dispatchRecipientWindow(contracts.CampaignDispatchRequest{
		TotalRecipients:  100,
		SelectedChannels: []string{"email", "sms", "max"},
		StartRecipient:   31,
	}, 90)

	if window.Start != 31 || window.End != 60 || window.NextStart != 61 {
		t.Fatalf("window = %#v, want 31..60 next 61", window)
	}
}

func TestDispatchRecipientWindowUsesSpecificRecipientChannels(t *testing.T) {
	window := dispatchRecipientWindow(contracts.CampaignDispatchRequest{
		TotalRecipients:  999,
		SelectedChannels: []string{"email"},
		SpecificRecipients: []contracts.CampaignRecipient{
			{UserID: "u1", Channels: []string{"email"}},
			{UserID: "u2", Channels: []string{"email", "max"}},
			{UserID: "u3", Channels: []string{"max"}},
		},
		StartRecipient: 1,
	}, 4)

	if window.Start != 1 || window.End != 2 || window.NextStart != 3 {
		t.Fatalf("specific window = %#v, want 1..2 next 3", window)
	}
}

func TestPrepareSpecificRecipientsOwnsAudienceCounts(t *testing.T) {
	recipients, totalRecipients, totalMessages := prepareSpecificRecipients(createCampaignRequest{
		SelectedChannels: []string{"email", "sms", "telegram"},
		TotalRecipients:  999,
		SpecificRecipients: []contracts.CampaignRecipient{
			{UserID: "user-2", Channels: []string{"sms"}},
			{UserID: "user-1", Channels: []string{"email", "telegram"}},
		},
	})

	if totalRecipients != 2 {
		t.Fatalf("totalRecipients = %d, want 2", totalRecipients)
	}
	if totalMessages != 3 {
		t.Fatalf("totalMessages = %d, want 3", totalMessages)
	}
	if recipients[0].UserID != "user-2" || recipients[1].UserID != "user-1" {
		t.Fatalf("recipient order should be preserved, got %#v", recipients)
	}
}

func TestPrepareSpecificRecipientsFallsBackToSelectedChannels(t *testing.T) {
	recipients, _, totalMessages := prepareSpecificRecipients(createCampaignRequest{
		SelectedChannels: []string{"email", "sms"},
		SpecificRecipients: []contracts.CampaignRecipient{
			{UserID: "user-1"},
			{UserID: "user-1", Channels: []string{"telegram"}},
			{UserID: "user-2", Channels: []string{"email", "email"}},
		},
	})

	if len(recipients) != 2 {
		t.Fatalf("duplicate user ids should be ignored, got %#v", recipients)
	}
	if strings.Join(recipients[0].Channels, ",") != "email,sms" {
		t.Fatalf("empty per-user channels should use campaign channels, got %#v", recipients[0].Channels)
	}
	if strings.Join(recipients[1].Channels, ",") != "email" {
		t.Fatalf("per-user channels should be unique, got %#v", recipients[1].Channels)
	}
	if totalMessages != 3 {
		t.Fatalf("totalMessages = %d, want 3", totalMessages)
	}
}
