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

func TestDispatchRecipientWindowUsesSpecificRecipientCount(t *testing.T) {
	req := contracts.CampaignDispatchRequest{
		TotalRecipients:  100,
		SelectedChannels: []string{"email", "sms"},
		SpecificRecipients: []contracts.CampaignRecipient{
			{UserID: "vip-1", Channels: []string{"email", "sms"}},
			{UserID: "vip-2", Channels: []string{"email", "sms"}},
			{UserID: "vip-3", Channels: []string{"email", "sms"}},
		},
	}

	window := dispatchRecipientWindow(req, 4)

	if window.Start != 1 || window.End != 2 {
		t.Fatalf("specific recipients should window over explicit list, got %#v", window)
	}
	if window.NextStart != 3 {
		t.Fatalf("expected continuation at explicit recipient 3, got %d", window.NextStart)
	}
}

func TestBuildSendMessagesUsesSpecificRecipientIDs(t *testing.T) {
	req := contracts.CampaignDispatchRequest{
		CampaignID:       "cmp-1",
		MessageBody:      "hello",
		SelectedChannels: []string{"email", "sms"},
		SpecificRecipients: []contracts.CampaignRecipient{
			{UserID: "user-100", Channels: []string{"email"}},
			{UserID: "user-250", Channels: []string{"sms"}},
		},
	}

	messages := buildSendMessages(req, recipientWindow{Start: 1, End: 2})

	if len(messages) != 2 {
		t.Fatalf("expected exact per-recipient messages, got %d", len(messages))
	}
	if messages[0].UserID != "user-100" || messages[0].ChannelCode != "email" {
		t.Fatalf("first message should use selected user/channel, got %#v", messages[0])
	}
	if messages[1].UserID != "user-250" || messages[1].ChannelCode != "sms" {
		t.Fatalf("second message should use selected user/channel, got %#v", messages[1])
	}
}

func TestDispatchTotalMessagesUsesSpecificRecipientChannels(t *testing.T) {
	req := contracts.CampaignDispatchRequest{
		TotalRecipients:  99,
		SelectedChannels: []string{"email", "sms", "telegram"},
		SpecificRecipients: []contracts.CampaignRecipient{
			{UserID: "user-1", Channels: []string{"email"}},
			{UserID: "user-2", Channels: []string{"sms", "telegram"}},
		},
	}

	if got := dispatchTotalMessages(req); got != 3 {
		t.Fatalf("dispatchTotalMessages() = %d, want 3", got)
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
