package campaigns

import (
	"crypto/sha256"
	"encoding/hex"
)

type DispatchBatch struct {
	UserIDs         []string `json:"user_ids"`
	Channels        []string `json:"channels"`
	TotalMessages   int      `json:"total_messages"`
	IdempotencySeed string   `json:"idempotency_seed"`
}

const DefaultDispatchBatchSize = 50000

func TotalMessages(recipients int, channels []string) int {
	if recipients < 0 {
		return 0
	}
	return recipients * len(channels)
}

func IdempotencyKey(campaignID, userID, channel string) string {
	sum := sha256.Sum256([]byte(campaignID + ":" + userID + ":" + channel))
	return hex.EncodeToString(sum[:])
}

func BuildDispatchBatches(userIDs []string, channels []string, batchSize int) []DispatchBatch {
	if batchSize <= 0 {
		batchSize = DefaultDispatchBatchSize
	}
	batches := make([]DispatchBatch, 0, (len(userIDs)+batchSize-1)/batchSize)
	for start := 0; start < len(userIDs); start += batchSize {
		end := start + batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}
		chunk := append([]string(nil), userIDs[start:end]...)
		batches = append(batches, DispatchBatch{
			UserIDs:         chunk,
			Channels:        append([]string(nil), channels...),
			TotalMessages:   TotalMessages(len(chunk), channels),
			IdempotencySeed: IdempotencyKey("batch", chunk[0], channels[0]),
		})
	}
	return batches
}
