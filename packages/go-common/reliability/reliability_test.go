package reliability

import "testing"

func TestRetryPolicyMovesToDLQAfterLimit(t *testing.T) {
	policy := RetryPolicy{Limit: 3}
	if policy.Decide(1) != DecisionRetry {
		t.Fatal("attempt 1 should retry")
	}
	if policy.Decide(3) != DecisionDLQ {
		t.Fatal("attempt 3 should move to DLQ")
	}
}

func TestErrorActionBuilder(t *testing.T) {
	err := BuildChannelError("telegram", 842)
	if err.Title == "" || len(err.Actions) != 3 {
		t.Fatalf("unexpected action error: %#v", err)
	}
	if err.Actions[0].Code != "retry" || err.Actions[1].Code != "switch_channel" || err.Actions[2].Code != "cancel_campaign" {
		t.Fatalf("unexpected actions: %#v", err.Actions)
	}
}
