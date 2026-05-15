ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS archived_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_campaigns_active_created_at
  ON campaigns(created_at DESC)
  WHERE archived_at IS NULL;
