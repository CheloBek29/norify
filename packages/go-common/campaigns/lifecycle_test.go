package campaigns

import (
	"testing"
	"time"
)

func TestProgressSnapshot(t *testing.T) {
	progress := Progress{CampaignID: "c1", TotalMessages: 100, Success: 42, Failed: 8, Cancelled: 0}
	snapshot := progress.Snapshot()
	if snapshot.Processed != 50 {
		t.Fatalf("expected processed 50, got %d", snapshot.Processed)
	}
	if snapshot.ProgressPercent != 50 {
		t.Fatalf("expected 50%%, got %.2f", snapshot.ProgressPercent)
	}
	if snapshot.Status != StatusRunning {
		t.Fatalf("expected running status, got %s", snapshot.Status)
	}
}

func TestProgressSnapshotFinished(t *testing.T) {
	progress := Progress{CampaignID: "c1", TotalMessages: 10, Success: 7, Failed: 3}
	if got := progress.Snapshot().Status; got != StatusFinished {
		t.Fatalf("expected finished, got %s", got)
	}
}

func TestCampaignActions(t *testing.T) {
	progress := Progress{CampaignID: "c1", TotalMessages: 10, Success: 4, Failed: 3}
	progress.RetryFailed()
	if progress.Failed != 0 || progress.TotalMessages != 13 {
		t.Fatalf("retry should move failed messages back into queue: %#v", progress)
	}
	progress.Cancel()
	if progress.Snapshot().Status != StatusCancelled {
		t.Fatalf("cancel should mark snapshot cancelled: %#v", progress.Snapshot())
	}
}

func TestDispatchP95RoundsFastSamplesUpToVisibleMilliseconds(t *testing.T) {
	samples := []time.Duration{
		1200 * time.Microsecond,
		2 * time.Millisecond,
		9 * time.Millisecond,
		5 * time.Millisecond,
		4 * time.Millisecond,
	}
	if got := DispatchP95Milliseconds(samples); got != 9 {
		t.Fatalf("expected p95 9 ms, got %d", got)
	}

	if got := DispatchP95Milliseconds([]time.Duration{300 * time.Microsecond}); got != 1 {
		t.Fatalf("sub-millisecond dispatch should still be visible, got %d", got)
	}
}
