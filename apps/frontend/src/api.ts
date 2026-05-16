export type Role = "manager" | "admin";
export type CampaignStatus = "created" | "running" | "retrying" | "stopped" | "cancelled" | "finished";
export type DeliveryStatus = "queued" | "sent" | "failed" | "cancelled";

export type Manager = {
  id: string;
  email: string;
  role: Role;
  active: boolean;
};

export type Template = {
  id: string;
  name: string;
  body: string;
  variables: string[];
  version: number;
  updatedAt: string;
};

export type TemplateVariable = {
  name: string;
  type: string;
  source: string;
};

export type Channel = {
  code: string;
  name: string;
  enabled: boolean;
  successProbability: number;
  minDelaySeconds: number;
  maxDelaySeconds: number;
  maxParallelism: number;
  retryLimit: number;
  degraded?: boolean;
  deliveryTotal?: number;
  deliverySent?: number;
  deliveryFailed?: number;
  deliveryQueued?: number;
  deliveryCancelled?: number;
  deliverySuccessRate?: number | null;
  averageAttempt?: number | null;
};

export type AudienceFilter = {
  minAge: number;
  maxAge: number;
  gender: "any" | "female" | "male";
  location: string;
  segment: string;
  tags: string[];
  activity: "any" | "active_7d" | "active_30d" | "inactive_90d";
  registeredAfter: string;
};

export type Campaign = {
  id: string;
  name: string;
  templateId: string;
  templateName: string;
  status: CampaignStatus;
  filters: AudienceFilter;
  selectedChannels: string[];
  totalRecipients: number;
  totalMessages: number;
  processed: number;
  success: number;
  failed: number;
  cancelled: number;
  p95DispatchMs: number;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  archivedAt?: string;
};

export type Delivery = {
  id: string;
  campaignId: string;
  userId: string;
  channelCode: string;
  status: DeliveryStatus;
  attempt: number;
  errorCode?: string;
  errorMessage?: string;
  finishedAt?: string;
};

export type SystemEvent = {
  id: string;
  service: string;
  level: "info" | "warn" | "error";
  type: string;
  message: string;
  createdAt: string;
};

export type ActionableError = {
  title: string;
  description: string;
  impact: string;
  actions: { code: "retry" | "switch_channel" | "cancel_campaign"; label: string }[];
};

export type ErrorGroup = {
  id: string;
  campaignId: string;
  channelCode: string;
  errorCode: string;
  errorMessage: string;
  failedCount: number;
  maxAttempt: number;
  firstSeenAt: string;
  lastSeenAt: string;
  impact: string;
  recommendedActions: { code: "retry" | "switch_channel" | "cancel_group"; label: string }[];
};

export type ServiceHealthStatus = "checking" | "ready" | "down";

export type ServiceHealth = {
  id: string;
  name: string;
  url: string;
  status: ServiceHealthStatus;
  latencyMs: number;
  checkedAt: string;
  detail: string;
};

export const credentials: Record<Role, { email: string; password: string }> = {
  admin: { email: "admin@example.com", password: "admin123" },
  manager: { email: "manager@example.com", password: "manager123" },
};

export const templatesSeed: Template[] = [
  {
    id: "tpl-reactivation",
    name: "Реактивация клиента",
    body: "Здравствуйте, {{first_name}}. Для вас доступно новое персональное предложение.",
    variables: ["first_name"],
    version: 3,
    updatedAt: "2026-05-13T09:10:00Z",
  },
  {
    id: "tpl-order",
    name: "Статус заказа",
    body: "Заказ {{order_id}} обновлен. Проверьте детали в приложении.",
    variables: ["order_id"],
    version: 7,
    updatedAt: "2026-05-12T15:42:00Z",
  },
  {
    id: "tpl-service",
    name: "Сервисное уведомление",
    body: "{{first_name}}, мы обновили условия обслуживания в вашем регионе.",
    variables: ["first_name"],
    version: 1,
    updatedAt: "2026-05-10T11:05:00Z",
  },
];

export const templateVariablesSeed: TemplateVariable[] = [
  { name: "id", type: "text", source: "users" },
  { name: "email", type: "text", source: "users" },
  { name: "phone", type: "text", source: "users" },
  { name: "telegram_id", type: "text", source: "users" },
  { name: "vk_id", type: "text", source: "users" },
  { name: "custom_app_id", type: "text", source: "users" },
  { name: "age", type: "integer", source: "users" },
  { name: "gender", type: "text", source: "users" },
  { name: "location", type: "text", source: "users" },
  { name: "tags", type: "text[]", source: "users" },
  { name: "created_at", type: "timestamp", source: "users" },
];

export const channelsSeed: Channel[] = [
  { code: "email", name: "Email", enabled: true, successProbability: 0.96, minDelaySeconds: 2, maxDelaySeconds: 60, maxParallelism: 180, retryLimit: 3 },
  { code: "sms", name: "SMS", enabled: true, successProbability: 0.91, minDelaySeconds: 2, maxDelaySeconds: 90, maxParallelism: 90, retryLimit: 3 },
  { code: "telegram", name: "Telegram", enabled: true, successProbability: 0.82, minDelaySeconds: 2, maxDelaySeconds: 120, maxParallelism: 120, retryLimit: 3, degraded: true },
  { code: "whatsapp", name: "WhatsApp", enabled: true, successProbability: 0.9, minDelaySeconds: 2, maxDelaySeconds: 120, maxParallelism: 100, retryLimit: 3 },
  { code: "vk", name: "VK", enabled: true, successProbability: 0.89, minDelaySeconds: 2, maxDelaySeconds: 120, maxParallelism: 100, retryLimit: 3 },
  { code: "max", name: "MAX", enabled: false, successProbability: 0.93, minDelaySeconds: 2, maxDelaySeconds: 140, maxParallelism: 70, retryLimit: 2 },
  { code: "custom_app", name: "Custom App", enabled: true, successProbability: 0.98, minDelaySeconds: 2, maxDelaySeconds: 45, maxParallelism: 220, retryLimit: 3 },
];

export const managersSeed: Manager[] = [
  { id: "admin-1", email: "admin@example.com", role: "admin", active: true },
  { id: "manager-1", email: "manager@example.com", role: "manager", active: true },
  { id: "manager-2", email: "retention@example.com", role: "manager", active: true },
];

export const defaultFilter: AudienceFilter = {
  minAge: 20,
  maxAge: 45,
  gender: "any",
  location: "Moscow",
  segment: "retail",
  tags: ["retail", "vip"],
  activity: "active_30d",
  registeredAfter: "2025-01-01",
};

export const campaignsSeed: Campaign[] = [
  {
    id: "cmp-spring",
    name: "Весенняя реактивация",
    templateId: "tpl-reactivation",
    templateName: "Реактивация клиента",
    status: "running",
    filters: defaultFilter,
    selectedChannels: ["email", "telegram", "custom_app"],
    totalRecipients: 50000,
    totalMessages: 150000,
    processed: 5120,
    success: 4781,
    failed: 339,
    cancelled: 0,
    p95DispatchMs: 942,
    createdAt: "2026-05-13T08:55:00Z",
    startedAt: "2026-05-13T09:00:00Z",
  },
  {
    id: "cmp-admin",
    name: "Админское уведомление",
    templateId: "tpl-service",
    templateName: "Сервисное уведомление",
    status: "created",
    filters: { ...defaultFilter, location: "all", tags: ["service"] },
    selectedChannels: ["email", "sms", "telegram", "whatsapp", "vk", "custom_app"],
    totalRecipients: 50000,
    totalMessages: 300000,
    processed: 0,
    success: 0,
    failed: 0,
    cancelled: 0,
    p95DispatchMs: 0,
    createdAt: "2026-05-13T10:10:00Z",
  },
];

export const deliveriesSeed: Delivery[] = [
  { id: "d1", campaignId: "cmp-spring", userId: "user-00001", channelCode: "email", status: "sent", attempt: 1, finishedAt: "2026-05-13T09:01:10Z" },
  { id: "d2", campaignId: "cmp-spring", userId: "user-00001", channelCode: "telegram", status: "failed", attempt: 3, errorCode: "channel_timeout", errorMessage: "Telegram adapter timeout", finishedAt: "2026-05-13T09:03:44Z" },
  { id: "d3", campaignId: "cmp-spring", userId: "user-00002", channelCode: "custom_app", status: "sent", attempt: 1, finishedAt: "2026-05-13T09:02:02Z" },
  { id: "d4", campaignId: "cmp-spring", userId: "user-00003", channelCode: "email", status: "queued", attempt: 1 },
];

export const eventsSeed: SystemEvent[] = [
  { id: "e1", service: "campaign-service", level: "info", type: "campaign.started", message: "Campaign cmp-spring started", createdAt: "2026-05-13T09:00:00Z" },
  { id: "e2", service: "dispatcher-service", level: "info", type: "batch.dispatched", message: "150 batches queued in 942 ms p95", createdAt: "2026-05-13T09:00:01Z" },
  { id: "e3", service: "sender-worker", level: "warn", type: "channel.degraded", message: "Telegram timeout rate above threshold", createdAt: "2026-05-13T09:03:44Z" },
];

export const currentError: ActionableError = {
  title: "Telegram временно недоступен",
  description: "Часть сообщений не была отправлена через Telegram из-за превышения таймаута канала.",
  impact: "Остальные каналы продолжают отправку. Ошибка затронула 842 сообщения.",
  actions: [
    { code: "retry", label: "Повторить" },
    { code: "switch_channel", label: "Сменить канал" },
    { code: "cancel_campaign", label: "Отменить" },
  ],
};

export const errorGroupsSeed: ErrorGroup[] = [
  {
    id: "telegram-timeout",
    campaignId: "cmp-spring",
    channelCode: "telegram",
    errorCode: "channel_timeout",
    errorMessage: "Telegram adapter timeout",
    failedCount: 339,
    maxAttempt: 3,
    firstSeenAt: "2026-05-13T09:03:44Z",
    lastSeenAt: "2026-05-13T09:07:12Z",
    impact: "Затронуто 339 сообщений. Основная очередь продолжает обработку.",
    recommendedActions: [
      { code: "retry", label: "Повторить группу" },
      { code: "switch_channel", label: "Вставить через другой канал" },
      { code: "cancel_group", label: "Закрыть группу" },
    ],
  },
];

export function audiencePreview(filter: AudienceFilter): number {
  let count = 50000;
  if (filter.location !== "all") count -= 11800;
  if (filter.gender !== "any") count -= 9000;
  if (filter.segment === "b2b") count -= 17500;
  if (filter.activity === "active_7d") count -= 8300;
  if (filter.activity === "inactive_90d") count -= 21400;
  count -= Math.max(0, filter.minAge - 18) * 180;
  count -= Math.max(0, 65 - filter.maxAge) * 80;
  count += filter.tags.includes("vip") ? 2400 : 0;
  return Math.max(3200, Math.min(50000, count));
}

export function progressPercent(campaign: Campaign): number {
  const totalMessages = effectiveTotalMessages(campaign);
  if (totalMessages === 0) return 0;
  return Math.min(100, Math.round((campaign.processed / totalMessages) * 10000) / 100);
}

export function effectiveTotalMessages(campaign: Campaign): number {
  if (campaign.totalMessages > 0) return campaign.totalMessages;
  return campaign.totalRecipients * campaign.selectedChannels.length;
}

const api = {
  auth: "http://localhost:8081",
  users: "http://localhost:8082",
  templates: "http://localhost:8083",
  channels: "http://localhost:8084",
  campaigns: "http://localhost:8085",
  dispatcher: "http://localhost:8086",
  sender: "http://localhost:8087",
  errors: "http://localhost:8088",
  status: "http://localhost:8090",
};

export const serviceHealthTargets = [
  { id: "auth-service", name: "auth-service", url: `${api.auth}/health/ready` },
  { id: "user-service", name: "user-service", url: `${api.users}/health/ready` },
  { id: "template-service", name: "template-service", url: `${api.templates}/health/ready` },
  { id: "channel-service", name: "channel-service", url: `${api.channels}/health/ready` },
  { id: "campaign-service", name: "campaign-service", url: `${api.campaigns}/health/ready` },
  { id: "dispatcher-service", name: "dispatcher-service", url: `${api.dispatcher}/health/ready` },
  { id: "sender-worker", name: "sender-worker", url: `${api.sender}/health/ready` },
  { id: "notification-error-service", name: "notification-error-service", url: `${api.errors}/health/ready` },
  { id: "status-service", name: "status-service", url: `${api.status}/health/ready` },
];

export async function fetchServiceHealth(): Promise<ServiceHealth[]> {
  return Promise.all(serviceHealthTargets.map(checkServiceHealth));
}

export type WorkerStats = {
  activeWorkers: number;
  minWorkers: number;
  maxWorkers: number;
  queueDepth: number;
};

export async function fetchWorkerStats(): Promise<WorkerStats | null> {
  try {
    const response = await fetch(`${api.sender}/worker/stats`, { signal: AbortSignal.timeout(2000) });
    if (!response.ok) return null;
    const data = await response.json();
    return {
      activeWorkers: data.active_workers ?? 0,
      minWorkers: data.min_workers ?? 0,
      maxWorkers: data.max_workers ?? 0,
      queueDepth: data.queue_depth ?? 0,
    };
  } catch {
    return null;
  }
}

async function checkServiceHealth(target: (typeof serviceHealthTargets)[number]): Promise<ServiceHealth> {
  const startedAt = performance.now();
  const checkedAt = new Date().toISOString();
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), 1800);
  try {
    const response = await fetch(target.url, { signal: controller.signal });
    const latencyMs = Math.round(performance.now() - startedAt);
    if (!response.ok) {
      return { ...target, status: "down", latencyMs, checkedAt, detail: `HTTP ${response.status}` };
    }
    const payload = await response.json().catch(() => ({}));
    const status = String(payload.status ?? payload.ready ?? "ready").toLowerCase();
    const ready = status === "ready" || status === "live" || status === "true";
    return { ...target, status: ready ? "ready" : "down", latencyMs, checkedAt, detail: ready ? "ready" : status };
  } catch (error) {
    const latencyMs = Math.round(performance.now() - startedAt);
    const detail = error instanceof Error ? error.message : "unreachable";
    return { ...target, status: "down", latencyMs, checkedAt, detail };
  } finally {
    window.clearTimeout(timeout);
  }
}

export async function backendLogin(email: string, password: string): Promise<{ email: string; role: Role }> {
  const response = await fetch(`${api.auth}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email, password }),
  });
  if (!response.ok) throw new Error("login_failed");
  const payload = await response.json();
  const claims = decodeClaims(payload.access_token);
  return { email: claims.email, role: claims.role };
}

export async function fetchCampaigns(): Promise<Campaign[]> {
  const response = await fetch(`${api.campaigns}/campaigns`);
  if (!response.ok) throw new Error("campaigns_unavailable");
  const payload = await response.json();
  return payload.map(normalizeCampaign);
}

export async function fetchDeliveries(campaignId: string): Promise<Delivery[]> {
  const response = await fetch(`${api.campaigns}/campaigns/${campaignId}/deliveries`);
  if (!response.ok) throw new Error("deliveries_unavailable");
  const payload = await response.json();
  return payload.map(normalizeDelivery);
}

export async function fetchErrorGroups(campaignId: string): Promise<ErrorGroup[]> {
  const response = await fetch(`${api.campaigns}/campaigns/${campaignId}/error-groups`);
  if (!response.ok) throw new Error("error_groups_unavailable");
  const payload = await response.json();
  return payload.map(normalizeErrorGroup);
}

export async function fetchTemplates(): Promise<Template[]> {
  const response = await fetch(`${api.templates}/templates`);
  if (!response.ok) throw new Error("templates_unavailable");
  const payload = await response.json();
  return payload.map((item: Record<string, unknown>) => ({
    id: String(item.id ?? item.ID),
    name: String(item.name ?? item.Name),
    body: String(item.body ?? item.Body),
    variables: (item.variables ?? item.Variables ?? []) as string[],
    version: Number(item.version ?? item.Version ?? 1),
    updatedAt: String(item.updated_at ?? item.UpdatedAt ?? new Date().toISOString()),
  }));
}

export async function fetchTemplateVariables(): Promise<TemplateVariable[]> {
  const response = await fetch(`${api.templates}/templates/variables`);
  if (!response.ok) throw new Error("template_variables_unavailable");
  const payload = await response.json();
  return payload.map((item: Record<string, unknown>) => ({
    name: String(item.name ?? item.column_name ?? item.ColumnName),
    type: String(item.type ?? item.data_type ?? item.DataType ?? "text"),
    source: String(item.source ?? item.table_name ?? item.TableName ?? "users"),
  }));
}

export async function fetchChannels(): Promise<Channel[]> {
  const response = await fetch(`${api.channels}/channels`);
  if (!response.ok) throw new Error("channels_unavailable");
  const payload = await response.json();
  return payload.map((item: Record<string, unknown>) => ({
    code: String(item.code ?? item.Code),
    name: String(item.name ?? item.Name ?? item.code ?? item.Code),
    enabled: Boolean(item.enabled ?? item.Enabled),
    successProbability: Number(item.success_probability ?? item.SuccessProbability ?? 0.92),
    minDelaySeconds: Number(item.min_delay_seconds ?? item.MinDelaySeconds ?? 2),
    maxDelaySeconds: Number(item.max_delay_seconds ?? item.MaxDelaySeconds ?? 300),
    maxParallelism: Number(item.max_parallelism ?? item.MaxParallelism ?? 100),
    retryLimit: Number(item.retry_limit ?? item.RetryLimit ?? 3),
    deliveryTotal: Number(item.delivery_total ?? item.DeliveryTotal ?? 0),
    deliverySent: Number(item.delivery_sent ?? item.DeliverySent ?? 0),
    deliveryFailed: Number(item.delivery_failed ?? item.DeliveryFailed ?? 0),
    deliveryQueued: Number(item.delivery_queued ?? item.DeliveryQueued ?? 0),
    deliveryCancelled: Number(item.delivery_cancelled ?? item.DeliveryCancelled ?? 0),
    deliverySuccessRate: optionalNumber(item.delivery_success_rate ?? item.DeliverySuccessRate),
    averageAttempt: optionalNumber(item.average_attempt ?? item.AverageAttempt),
  }));
}

export function campaignWebSocketURL(campaignId: string): string {
  return `${api.status.replace(/^http/, "ws")}/ws/campaigns/${campaignId}`;
}

export function operationsWebSocketURL(): string {
  return `${api.status.replace(/^http/, "ws")}/ws/ops`;
}

export function normalizeCampaign(item: Record<string, unknown>): Campaign {
  const snapshot = (item.snapshot ?? item.Snapshot ?? {}) as Record<string, unknown>;
  const selectedChannels = (item.selected_channels ?? item.SelectedChannels ?? []) as string[];
  const totalRecipients = Number(item.total_recipients ?? item.TotalRecipients ?? 0);
  const rawTotalMessages = Number(item.total_messages ?? item.TotalMessages ?? 0);
  const totalMessages = rawTotalMessages > 0 ? rawTotalMessages : totalRecipients * selectedChannels.length;
  const success = Number(item.success_count ?? item.SuccessCount ?? snapshot.success ?? 0);
  const failed = Number(item.failed_count ?? item.FailedCount ?? snapshot.failed ?? 0);
  const cancelled = Number(item.cancelled_count ?? item.CancelledCount ?? snapshot.cancelled ?? 0);
  return {
    id: String(item.id ?? item.ID),
    name: String(item.name ?? item.Name),
    templateId: String(item.template_id ?? item.TemplateID ?? ""),
    templateName: String(item.template_name ?? item.TemplateName ?? ""),
    status: String(item.status ?? item.Status ?? snapshot.status ?? "created") as CampaignStatus,
    filters: (item.filters ?? item.Filters ?? defaultFilter) as AudienceFilter,
    selectedChannels,
    totalRecipients,
    totalMessages,
    processed: Number(item.sent_count ?? item.SentCount ?? snapshot.processed ?? success + failed + cancelled),
    success,
    failed,
    cancelled,
    p95DispatchMs: Number(item.p95_dispatch_ms ?? item.p95DispatchMs ?? 0),
    createdAt: String(item.created_at ?? item.CreatedAt ?? new Date().toISOString()),
    startedAt: optionalString(item.started_at ?? item.StartedAt),
    finishedAt: optionalString(item.finished_at ?? item.FinishedAt),
    archivedAt: optionalString(item.archived_at ?? item.ArchivedAt),
  };
}

function normalizeDelivery(item: Record<string, unknown>): Delivery {
  return {
    id: String(item.id ?? item.ID),
    campaignId: String(item.campaign_id ?? item.CampaignID),
    userId: String(item.user_id ?? item.UserID),
    channelCode: String(item.channel_code ?? item.ChannelCode),
    status: String(item.status ?? item.Status) as DeliveryStatus,
    attempt: Number(item.attempt ?? item.Attempt ?? 1),
    errorCode: optionalString(item.error_code ?? item.ErrorCode),
    errorMessage: optionalString(item.error_message ?? item.ErrorMessage),
    finishedAt: optionalString(item.finished_at ?? item.FinishedAt),
  };
}

function normalizeErrorGroup(item: Record<string, unknown>): ErrorGroup {
  const recommendedActions = (item.recommended_actions ?? item.RecommendedActions ?? []) as ErrorGroup["recommendedActions"];
  return {
    id: String(item.id ?? item.ID),
    campaignId: String(item.campaign_id ?? item.CampaignID),
    channelCode: String(item.channel_code ?? item.ChannelCode),
    errorCode: String(item.error_code ?? item.ErrorCode ?? ""),
    errorMessage: String(item.error_message ?? item.ErrorMessage ?? ""),
    failedCount: Number(item.failed_count ?? item.FailedCount ?? 0),
    maxAttempt: Number(item.max_attempt ?? item.MaxAttempt ?? 0),
    firstSeenAt: String(item.first_seen_at ?? item.FirstSeenAt ?? ""),
    lastSeenAt: String(item.last_seen_at ?? item.LastSeenAt ?? ""),
    impact: String(item.impact ?? item.Impact ?? ""),
    recommendedActions,
  };
}

function optionalString(value: unknown): string | undefined {
  if (value === null || value === undefined || value === "") return undefined;
  return String(value);
}

function optionalNumber(value: unknown): number | null {
  if (value === null || value === undefined || value === "") return null;
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function decodeClaims(token: string): { email: string; role: Role } {
  const payload = token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/");
  const padded = payload.padEnd(Math.ceil(payload.length / 4) * 4, "=");
  const claims = JSON.parse(window.atob(padded));
  return { email: claims.email, role: claims.role };
}
