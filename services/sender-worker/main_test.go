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
