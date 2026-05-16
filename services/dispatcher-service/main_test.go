package main

import (
	"testing"

	"github.com/norify/platform/packages/contracts"
)

func TestDispatchRecipientWindowKeepsCampaignTicksSmall(t *testing.T) {
	req := contracts.CampaignDispatchRequest{
		TotalRecipients:  100,
		SelectedChannels: []string{"email", "sms", "telegram"},
		StartRecipient:   1,
	}

	window := dispatchRecipientWindow(req, 90)

	if window.Start != 1 {
		t.Fatalf("expected first recipient 1, got %d", window.Start)
	}
	if window.End != 30 {
		t.Fatalf("expected 30 recipients for 90 messages and 3 channels, got %d", window.End)
	}
	if window.NextStart != 31 {
		t.Fatalf("expected continuation at 31, got %d", window.NextStart)
	}
}

func TestDispatchRecipientWindowResumesFromContinuation(t *testing.T) {
	req := contracts.CampaignDispatchRequest{
		TotalRecipients:  100,
		SelectedChannels: []string{"email", "sms"},
		StartRecipient:   41,
	}

	window := dispatchRecipientWindow(req, 40)

	if window.Start != 41 || window.End != 60 {
		t.Fatalf("unexpected resumed window: %#v", window)
	}
	if window.NextStart != 61 {
		t.Fatalf("expected next continuation at 61, got %d", window.NextStart)
	}
}

func TestDispatchRecipientWindowClampsTail(t *testing.T) {
	req := contracts.CampaignDispatchRequest{
		TotalRecipients:  45,
		SelectedChannels: []string{"email", "sms"},
		StartRecipient:   41,
	}

	window := dispatchRecipientWindow(req, 40)

	if window.Start != 41 || window.End != 45 {
		t.Fatalf("unexpected tail window: %#v", window)
	}
	if window.NextStart != 0 {
		t.Fatalf("tail window must not continue, got %d", window.NextStart)
	}
}

func TestDispatchRecipientWindowHandlesSmallMessageBudget(t *testing.T) {
	req := contracts.CampaignDispatchRequest{
		TotalRecipients:  3,
		SelectedChannels: []string{"email", "sms", "telegram"},
		StartRecipient:   1,
	}

	window := dispatchRecipientWindow(req, 1)

	if window.Start != 1 || window.End != 1 {
		t.Fatalf("small message budget must still include one recipient, got %#v", window)
	}
	if window.NextStart != 2 {
		t.Fatalf("expected continuation at 2, got %d", window.NextStart)
	}
}

func TestDispatchRecipientWindowDefaultsWhenChannelsMissing(t *testing.T) {
	req := contracts.CampaignDispatchRequest{
		TotalRecipients: 5,
		StartRecipient:  0,
	}

	window := dispatchRecipientWindow(req, 2)

	if window.Start != 1 || window.End != 2 {
		t.Fatalf("missing channels should default to one channel window, got %#v", window)
	}
	if window.NextStart != 3 {
		t.Fatalf("expected continuation at 3, got %d", window.NextStart)
	}
}

func TestShouldThrottleSendQueue(t *testing.T) {
	if !shouldThrottleSendQueue(300, 300) {
		t.Fatalf("queue at the limit should throttle")
	}
	if shouldThrottleSendQueue(299, 300) {
		t.Fatalf("queue below the limit should continue")
	}
}

func TestCanDispatchCampaignStatus(t *testing.T) {
	if !canDispatchCampaignStatus("running") {
		t.Fatalf("running campaign should dispatch")
	}
	if !canDispatchCampaignStatus("retrying") {
		t.Fatalf("retrying campaign should dispatch")
	}
	if canDispatchCampaignStatus("stopped") {
		t.Fatalf("stopped campaign must not dispatch")
	}
	if canDispatchCampaignStatus("cancelled") {
		t.Fatalf("cancelled campaign must not dispatch")
	}
}

func TestUserIDFormatting(t *testing.T) {
	tests := map[int]string{
		1:      "user-00001",
		42:     "user-00042",
		50000:  "user-50000",
		123456: "user-23456",
	}
	for input, want := range tests {
		if got := userID(input); got != want {
			t.Fatalf("userID(%d) = %q, want %q", input, got, want)
		}
	}
}
