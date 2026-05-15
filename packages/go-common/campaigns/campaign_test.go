package campaigns

import "testing"

func TestTotalMessages(t *testing.T) {
	if got := TotalMessages(50000, []string{"email", "sms", "telegram"}); got != 150000 {
		t.Fatalf("unexpected total messages: %d", got)
	}
}

func TestIdempotencyKeyStable(t *testing.T) {
	a := IdempotencyKey("campaign-1", "user-1", "email")
	b := IdempotencyKey("campaign-1", "user-1", "email")
	c := IdempotencyKey("campaign-1", "user-1", "sms")
	if a != b {
		t.Fatal("idempotency key must be stable")
	}
	if a == c {
		t.Fatal("idempotency key must include channel")
	}
}

func TestBatchDispatch(t *testing.T) {
	batches := BuildDispatchBatches([]string{"u1", "u2", "u3", "u4", "u5"}, []string{"email", "sms"}, 2)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	if batches[0].TotalMessages != 4 {
		t.Fatalf("expected first batch to contain 4 messages, got %d", batches[0].TotalMessages)
	}
}

func TestBatchDispatchDefaultsToFiftyThousandRecipients(t *testing.T) {
	users := make([]string, 50001)
	for i := range users {
		users[i] = "user"
	}
	batches := BuildDispatchBatches(users, []string{"email"}, 0)
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}
	if got := len(batches[0].UserIDs); got != 50000 {
		t.Fatalf("expected default batch to contain 50000 recipients, got %d", got)
	}
	if got := len(batches[1].UserIDs); got != 1 {
		t.Fatalf("expected tail batch to contain 1 recipient, got %d", got)
	}
}
