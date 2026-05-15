package channels

import (
	"context"
	"testing"
	"time"
)

func TestRegistryReturnsOnlyEnabledChannels(t *testing.T) {
	registry := NewRegistry([]Config{
		{Code: "email", Enabled: true, SuccessProbability: 1, MinDelay: time.Millisecond, MaxDelay: time.Millisecond},
		{Code: "sms", Enabled: false, SuccessProbability: 1, MinDelay: time.Millisecond, MaxDelay: time.Millisecond},
	})

	got := registry.EnabledCodes()
	if len(got) != 1 || got[0] != "email" {
		t.Fatalf("unexpected enabled channels: %#v", got)
	}
}

func TestAdapterFailureIsIsolated(t *testing.T) {
	registry := NewRegistry([]Config{
		{Code: "email", Enabled: true, SuccessProbability: 1, MinDelay: time.Millisecond, MaxDelay: time.Millisecond},
		{Code: "sms", Enabled: true, SuccessProbability: 0, MinDelay: time.Millisecond, MaxDelay: time.Millisecond},
	})

	emailResult := registry.Adapter("email").Send(context.Background(), Message{RecipientID: "u1", Body: "ok"})
	smsResult := registry.Adapter("sms").Send(context.Background(), Message{RecipientID: "u1", Body: "ok"})

	if emailResult.Status != StatusSuccess {
		t.Fatalf("email should succeed: %#v", emailResult)
	}
	if smsResult.Status != StatusFailed {
		t.Fatalf("sms should fail independently: %#v", smsResult)
	}
}
