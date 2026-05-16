package main

import "testing"

func TestBuildSnapshotUsesRealDeliveryRows(t *testing.T) {
	snapshot := buildSnapshot(
		[]campaignAggregateRow{
			{TotalMessages: 100, Processed: 80, Success: 72, Failed: 6, Cancelled: 2, Active: 1, P95DispatchMs: 42},
		},
		[]channelAggregateRow{
			{Code: "email", Total: 50, Sent: 47, Failed: 3, Queued: 0, Cancelled: 0, AverageAttempt: floatPtr(1.2)},
			{Code: "sms", Total: 30, Sent: 25, Failed: 3, Queued: 2, Cancelled: 0, AverageAttempt: floatPtr(1.8)},
		},
		[]realtimeAggregateRow{
			{Bucket: "08:40", Sent: 10, Failed: 1},
			{Bucket: "08:41", Sent: 14, Failed: 2},
		},
		7,
	)

	if snapshot.Source != "postgres" {
		t.Fatalf("source = %q", snapshot.Source)
	}
	if snapshot.Totals.Messages != 100 || snapshot.Totals.Processed != 80 {
		t.Fatalf("totals = %#v", snapshot.Totals)
	}
	if snapshot.Totals.Pending != 20 || snapshot.Totals.QueueDepth != 7 {
		t.Fatalf("pending/queue = %d/%d", snapshot.Totals.Pending, snapshot.Totals.QueueDepth)
	}
	if snapshot.Totals.SuccessRate == nil || *snapshot.Totals.SuccessRate != 0.9 {
		t.Fatalf("success rate = %#v", snapshot.Totals.SuccessRate)
	}
	if len(snapshot.Channels) != 2 {
		t.Fatalf("channels len = %d", len(snapshot.Channels))
	}
	if snapshot.Channels[0].SuccessRate == nil || *snapshot.Channels[0].SuccessRate != 0.94 {
		t.Fatalf("email success rate = %#v", snapshot.Channels[0].SuccessRate)
	}
	if snapshot.Realtime[1].Sent != 14 || snapshot.Realtime[1].Failed != 2 {
		t.Fatalf("realtime = %#v", snapshot.Realtime)
	}
}

func TestBuildSnapshotKeepsEmptyChannelsExplicit(t *testing.T) {
	snapshot := buildSnapshot(
		nil,
		[]channelAggregateRow{{Code: "telegram"}},
		nil,
		-1,
	)

	if snapshot.Totals.SuccessRate != nil {
		t.Fatalf("empty success rate = %#v", snapshot.Totals.SuccessRate)
	}
	if snapshot.Channels[0].SuccessRate != nil {
		t.Fatalf("empty channel success rate = %#v", snapshot.Channels[0].SuccessRate)
	}
	if snapshot.Channels[0].Total != 0 {
		t.Fatalf("empty channel total = %d", snapshot.Channels[0].Total)
	}
}

func floatPtr(value float64) *float64 {
	return &value
}
