CREATE TABLE IF NOT EXISTS campaign_dispatch_state (
  campaign_id text PRIMARY KEY REFERENCES campaigns(id) ON DELETE CASCADE,
  next_recipient int NOT NULL DEFAULT 1,
  lease_start int,
  lease_end int,
  lease_until timestamptz,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_campaign_dispatch_state_recoverable
  ON campaign_dispatch_state (completed_at, lease_until, updated_at);

INSERT INTO campaign_dispatch_state (campaign_id, next_recipient, completed_at)
SELECT
  c.id,
  1,
  CASE
    WHEN c.status IN ('finished', 'cancelled') THEN COALESCE(c.finished_at, now())
    ELSE NULL
  END
FROM campaigns c
WHERE c.status IN ('running', 'retrying', 'stopped', 'created', 'finished', 'cancelled')
ON CONFLICT (campaign_id) DO NOTHING;
