package runtime

import (
	"testing"

	"github.com/norify/platform/packages/contracts"
)

func TestPublishingOptionsUseIdempotencyMetadata(t *testing.T) {
	req := contracts.SendMessageRequest{
		CampaignID:     "cmp-1",
		UserID:         "user-1",
		ChannelCode:    "email",
		IdempotencyKey: "cmp-1:user-1:email",
	}

	options := MessagePublishOptions(req)

	if options.MessageID != req.IdempotencyKey {
		t.Fatalf("message id must mirror idempotency key, got %q", options.MessageID)
	}
	if options.CorrelationID != req.CampaignID {
		t.Fatalf("correlation id must link to campaign, got %q", options.CorrelationID)
	}
	if options.Headers["x-idempotency-key"] != req.IdempotencyKey {
		t.Fatalf("missing idempotency header: %#v", options.Headers)
	}
}
