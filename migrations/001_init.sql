CREATE TABLE IF NOT EXISTS users (
  id text PRIMARY KEY,
  email text NOT NULL,
  phone text,
  telegram_id text,
  vk_id text,
  custom_app_id text,
  age int NOT NULL,
  gender text NOT NULL,
  location text NOT NULL,
  tags text[] NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS managers (
  id text PRIMARY KEY,
  email text NOT NULL UNIQUE,
  password_hash text NOT NULL,
  role text NOT NULL CHECK (role IN ('manager', 'admin')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS templates (
  id text PRIMARY KEY,
  name text NOT NULL,
  body text NOT NULL,
  variables text[] NOT NULL DEFAULT '{}',
  created_by text REFERENCES managers(id),
  version int NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS channels (
  id text PRIMARY KEY,
  code text NOT NULL UNIQUE,
  name text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  success_probability numeric(5,4) NOT NULL DEFAULT 0.9200,
  min_delay_seconds int NOT NULL DEFAULT 2,
  max_delay_seconds int NOT NULL DEFAULT 300,
  max_parallelism int NOT NULL DEFAULT 100,
  retry_limit int NOT NULL DEFAULT 3,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS campaigns (
  id text PRIMARY KEY,
  name text NOT NULL,
  template_id text REFERENCES templates(id),
  created_by text REFERENCES managers(id),
  status text NOT NULL,
  filters jsonb NOT NULL DEFAULT '{}',
  selected_channels jsonb NOT NULL DEFAULT '[]',
  specific_recipients jsonb NOT NULL DEFAULT '[]',
  total_recipients int NOT NULL DEFAULT 0,
  total_messages int NOT NULL DEFAULT 0,
  sent_count int NOT NULL DEFAULT 0,
  success_count int NOT NULL DEFAULT 0,
  failed_count int NOT NULL DEFAULT 0,
  cancelled_count int NOT NULL DEFAULT 0,
  p95_dispatch_ms int NOT NULL DEFAULT 0,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS message_deliveries (
  id text PRIMARY KEY,
  campaign_id text NOT NULL REFERENCES campaigns(id),
  user_id text NOT NULL,
  channel_code text NOT NULL,
  message_body text NOT NULL,
  status text NOT NULL,
  error_code text,
  error_message text,
  attempt int NOT NULL DEFAULT 1,
  idempotency_key text NOT NULL UNIQUE,
  queued_at timestamptz,
  sent_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_logs (
  id text PRIMARY KEY,
  actor_id text,
  actor_role text NOT NULL,
  action text NOT NULL,
  entity_type text NOT NULL,
  entity_id text,
  payload jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS system_events (
  id text PRIMARY KEY,
  service_name text NOT NULL,
  level text NOT NULL,
  event_type text NOT NULL,
  message text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_users_age_gender_location ON users(age, gender, location);
CREATE INDEX IF NOT EXISTS idx_users_tags ON users USING GIN(tags);
CREATE INDEX IF NOT EXISTS idx_campaigns_created_by_created_at ON campaigns(created_by, created_at);
CREATE INDEX IF NOT EXISTS idx_campaigns_status ON campaigns(status);
CREATE INDEX IF NOT EXISTS idx_message_deliveries_campaign_status ON message_deliveries(campaign_id, status);
CREATE INDEX IF NOT EXISTS idx_message_deliveries_campaign_user_channel ON message_deliveries(campaign_id, user_id, channel_code);
CREATE UNIQUE INDEX IF NOT EXISTS idx_message_deliveries_idempotency_key ON message_deliveries(idempotency_key);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor_created_at ON audit_logs(actor_id, created_at);

INSERT INTO managers (id, email, password_hash, role)
VALUES
  ('admin-1', 'admin@example.com', 'c2RjLW5vdC11c2Vk', 'admin'),
  ('manager-1', 'manager@example.com', 'c2RjLW5vdC11c2Vk', 'manager')
ON CONFLICT (id) DO NOTHING;

INSERT INTO templates (id, name, body, variables, created_by, version)
VALUES
  ('tpl-reactivation', 'Реактивация клиента', 'Здравствуйте, {{first_name}}. Для вас доступно новое персональное предложение.', ARRAY['first_name'], 'manager-1', 3),
  ('tpl-order', 'Статус заказа', 'Заказ {{order_id}} обновлен. Проверьте детали в приложении.', ARRAY['order_id'], 'manager-1', 7),
  ('tpl-service', 'Сервисное уведомление', '{{first_name}}, мы обновили условия обслуживания в вашем регионе.', ARRAY['first_name'], 'admin-1', 1)
ON CONFLICT (id) DO NOTHING;

INSERT INTO channels (id, code, name, success_probability, min_delay_seconds, max_delay_seconds, max_parallelism, retry_limit)
VALUES
  ('channel-email', 'email', 'Email', 0.9600, 2, 60, 180, 3),
  ('channel-sms', 'sms', 'SMS', 0.9100, 2, 90, 90, 3),
  ('channel-telegram', 'telegram', 'Telegram', 0.8200, 2, 120, 120, 3),
  ('channel-whatsapp', 'whatsapp', 'WhatsApp', 0.9000, 2, 120, 100, 3),
  ('channel-vk', 'vk', 'VK', 0.8900, 2, 120, 100, 3),
  ('channel-max', 'max', 'MAX', 0.9300, 2, 140, 70, 2),
  ('channel-custom-app', 'custom_app', 'Custom App', 0.9800, 2, 45, 220, 3)
ON CONFLICT (code) DO UPDATE SET
  name = EXCLUDED.name,
  success_probability = EXCLUDED.success_probability,
  min_delay_seconds = EXCLUDED.min_delay_seconds,
  max_delay_seconds = EXCLUDED.max_delay_seconds,
  max_parallelism = EXCLUDED.max_parallelism,
  retry_limit = EXCLUDED.retry_limit;

INSERT INTO users (id, email, phone, telegram_id, vk_id, custom_app_id, age, gender, location, tags)
SELECT
  'user-' || lpad(gs::text, 5, '0'),
  'user' || lpad(gs::text, 5, '0') || '@example.com',
  '+7999' || lpad(gs::text, 7, '0'),
  'tg' || gs::text,
  'vk' || gs::text,
  'app' || gs::text,
  18 + (gs % 45),
  CASE WHEN gs % 2 = 0 THEN 'female' ELSE 'male' END,
  (ARRAY['Moscow', 'Kazan', 'Saint Petersburg', 'Novosibirsk'])[1 + (gs % 4)],
  CASE
    WHEN gs % 5 = 0 THEN ARRAY['vip', 'retail']
    WHEN gs % 3 = 0 THEN ARRAY['b2b']
    ELSE ARRAY['retail']
  END
FROM generate_series(1, 50000) AS gs
ON CONFLICT (id) DO NOTHING;
