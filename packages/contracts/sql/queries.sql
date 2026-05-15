-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: ListEnabledChannels :many
SELECT code, name, enabled, success_probability, min_delay_seconds, max_delay_seconds, max_parallelism, retry_limit
FROM channels
WHERE enabled = true
ORDER BY code;

-- name: GetCampaign :one
SELECT * FROM campaigns WHERE id = $1;

-- name: UpsertMessageDelivery :exec
INSERT INTO message_deliveries (
  id, campaign_id, user_id, channel_code, message_body, status, error_code,
  error_message, attempt, idempotency_key, queued_at, sent_at, finished_at,
  created_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7,
  $8, $9, $10, $11, $12, $13,
  now(), now()
)
ON CONFLICT (idempotency_key) DO UPDATE
SET status = EXCLUDED.status,
    error_code = EXCLUDED.error_code,
    error_message = EXCLUDED.error_message,
    attempt = EXCLUDED.attempt,
    sent_at = EXCLUDED.sent_at,
    finished_at = EXCLUDED.finished_at,
    updated_at = now();

