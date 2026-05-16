ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS specific_recipients jsonb NOT NULL DEFAULT '[]';
