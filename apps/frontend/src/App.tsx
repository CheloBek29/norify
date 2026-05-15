import { type Dispatch, type FormEvent, type SetStateAction, useEffect, useRef, useState } from "react";
import {
  ActionableError,
  AudienceFilter,
  Campaign,
  Channel,
  Delivery,
  ErrorGroup,
  Manager,
  Role,
  ServiceHealth,
  SystemEvent,
  Template,
  audiencePreview,
  campaignsSeed,
  channelsSeed,
  credentials,
  currentError,
  defaultFilter,
  deliveriesSeed,
  errorGroupsSeed,
  eventsSeed,
  managersSeed,
  progressPercent,
  templatesSeed,
  backendLogin,
  campaignWebSocketURL,
  fetchCampaigns,
  fetchChannels,
  fetchDeliveries,
  fetchErrorGroups,
  fetchServiceHealth,
  fetchTemplates,
  normalizeCampaign,
  operationsWebSocketURL,
  serviceHealthTargets,
} from "./api";

type Screen = "dashboard" | "campaigns" | "create" | "templates" | "channels" | "deliveries" | "stats" | "managers" | "health" | "logs";
type CampaignCommand = "start" | "stop" | "retry" | "switch_channel" | "cancel_campaign";

const navigation: { id: Screen; label: string; icon: IconName; adminOnly?: boolean }[] = [
  { id: "dashboard", label: "Dashboard", icon: "dashboard" },
  { id: "campaigns", label: "Campaigns", icon: "campaign" },
  { id: "create", label: "Create", icon: "plus" },
  { id: "templates", label: "Templates", icon: "template" },
  { id: "channels", label: "Channels", icon: "channel" },
  { id: "deliveries", label: "Deliveries", icon: "table" },
  { id: "stats", label: "Stats", icon: "chart" },
  { id: "managers", label: "Managers", icon: "users", adminOnly: true },
  { id: "health", label: "Health", icon: "pulse", adminOnly: true },
  { id: "logs", label: "Logs", icon: "log", adminOnly: true },
];

type Session = { email: string; role: Role } | null;

type WizardState = {
  name: string;
  templateId: string;
  filter: AudienceFilter;
  selectedChannels: string[];
};

export function App() {
  const [session, setSession] = useLocalState<Session>("norify-session", null);
  const [screen, setScreen] = useState<Screen>("dashboard");
  const [templates, setTemplates] = useLocalState<Template[]>("norify-templates", templatesSeed);
  const [channels, setChannels] = useLocalState<Channel[]>("norify-channels", channelsSeed);
  const [campaigns, setCampaigns] = useLocalState<Campaign[]>("norify-campaigns", campaignsSeed);
  const [deliveries, setDeliveries] = useLocalState<Delivery[]>("norify-deliveries", deliveriesSeed);
  const [errorGroups, setErrorGroups] = useLocalState<ErrorGroup[]>("norify-error-groups", errorGroupsSeed);
  const [events, setEvents] = useLocalState<SystemEvent[]>("norify-events", eventsSeed);
  const [managers, setManagers] = useLocalState<Manager[]>("norify-managers", managersSeed);
  const [healthChecks, setHealthChecks] = useState<ServiceHealth[]>(initialHealthChecks);
  const [selectedCampaignId, setSelectedCampaignId] = useState(campaigns[0]?.id ?? "");
  const [apiState, setApiState] = useState("connecting");
  const opsSocketRef = useRef<WebSocket | null>(null);
  const pendingOpsRef = useRef(new Map<string, { resolve: (value: Record<string, unknown>) => void; reject: (error: Error) => void }>());
  const selectedCampaign = campaigns.find((campaign) => campaign.id === selectedCampaignId) ?? campaigns[0];
  const activeError = selectedCampaign && selectedCampaign.failed > 0 ? buildError(selectedCampaign) : currentError;

  useEffect(() => {
    if (!session) return;
    void refreshBackendData();
  }, [session]);

  async function refreshBackendData() {
    try {
      const [nextCampaigns, nextTemplates, nextChannels] = await Promise.all([fetchCampaigns(), fetchTemplates(), fetchChannels()]);
      if (nextCampaigns.length > 0) {
        setCampaigns(nextCampaigns);
        setSelectedCampaignId((current) => current || nextCampaigns[0].id);
      }
      if (nextTemplates.length > 0) setTemplates(nextTemplates);
      if (nextChannels.length > 0) setChannels(nextChannels);
      setApiState("live backend");
    } catch {
      setApiState("local fallback");
    }
  }

  useEffect(() => {
    if (!session) return;
    const socket = new WebSocket(operationsWebSocketURL());
    opsSocketRef.current = socket;
    socket.onopen = () => setApiState("websocket commands");
    socket.onmessage = (event) => {
      try {
        const message = JSON.parse(event.data) as Record<string, unknown>;
        applyRealtimeMessage(message);
        const requestID = String(message.request_id ?? "");
        const pending = pendingOpsRef.current.get(requestID);
        if (pending) {
          pendingOpsRef.current.delete(requestID);
          if (message.type === "command.error") pending.reject(new Error(String(message.error ?? "command_failed")));
          else pending.resolve(message);
        }
      } catch {
        // ignore malformed websocket payloads
      }
    };
    socket.onclose = () => {
      if (opsSocketRef.current === socket) opsSocketRef.current = null;
      setApiState((current) => current === "websocket commands" ? "websocket reconnecting" : current);
    };
    return () => {
      socket.close();
      if (opsSocketRef.current === socket) opsSocketRef.current = null;
    };
  }, [session]);

  useEffect(() => {
    if (!session || campaigns.length === 0) return;
    let closed = false;
    const sockets = campaigns.map((campaign) => {
      const socket = new WebSocket(campaignWebSocketURL(campaign.id));
      socket.onmessage = (event) => {
        try {
          const snapshot = JSON.parse(event.data);
          applyCampaignSnapshot(snapshot);
          if (snapshot.campaign_id === selectedCampaignId) {
            void fetchErrorGroups(snapshot.campaign_id).then((groups) => {
              if (!closed) setErrorGroups(groups);
            }).catch(() => undefined);
            void fetchDeliveries(snapshot.campaign_id).then((rows) => {
              if (!closed && rows.length > 0) setDeliveries(rows);
            }).catch(() => undefined);
          }
        } catch {
          // ignore malformed websocket payloads
        }
      };
      socket.onopen = () => setApiState("websocket live");
      return socket;
    });
    return () => {
      closed = true;
      sockets.forEach((socket) => socket.close());
    };
  }, [session, campaigns.map((campaign) => campaign.id).sort().join("|"), selectedCampaignId, setCampaigns, setDeliveries, setErrorGroups]);

  useEffect(() => {
    if (apiState !== "local fallback") return;
    const timer = window.setInterval(() => {
      setCampaigns((items) =>
        items.map((campaign) => {
          if (campaign.status !== "running" && campaign.status !== "retrying") return campaign;
          if (campaign.totalMessages <= 0) return campaign;
          const remaining = campaign.totalMessages - campaign.processed;
          if (remaining <= 0) return { ...campaign, status: "finished", finishedAt: new Date().toISOString() };
          const step = Math.min(remaining, Math.max(260, Math.floor(campaign.totalMessages * 0.012)));
          const failed = Math.round(step * (campaign.selectedChannels.includes("telegram") ? 0.068 : 0.028));
          const success = step - failed;
          const processed = campaign.processed + step;
          return {
            ...campaign,
            status: processed >= campaign.totalMessages ? "finished" : campaign.status,
            processed,
            success: campaign.success + success,
            failed: campaign.failed + failed,
            p95DispatchMs: campaign.p95DispatchMs || 910,
            finishedAt: processed >= campaign.totalMessages ? new Date().toISOString() : campaign.finishedAt,
          };
        }),
      );
    }, 1200);
    return () => window.clearInterval(timer);
  }, [apiState, setCampaigns]);

  if (!session) {
    return <Login onLogin={setSession} />;
  }
  const userSession = session;

  const visibleNavigation = navigation.filter((item) => !item.adminOnly || userSession.role === "admin");

  function addEvent(level: SystemEvent["level"], type: string, message: string, service = "frontend") {
    setEvents((items) => [
      { id: id("event"), service, level, type, message, createdAt: new Date().toISOString() },
      ...items.slice(0, 49),
    ]);
  }

  function sendOpsCommand(type: string, payload: Record<string, unknown>) {
    const socket = opsSocketRef.current;
    if (!socket || socket.readyState !== 1 || typeof socket.send !== "function") {
      return Promise.reject(new Error("websocket_unavailable"));
    }
    const requestID = id("cmd");
    socket.send(JSON.stringify({ id: requestID, type, payload }));
    return new Promise<Record<string, unknown>>((resolve, reject) => {
      pendingOpsRef.current.set(requestID, { resolve, reject });
      window.setTimeout(() => {
        const pending = pendingOpsRef.current.get(requestID);
        if (!pending) return;
        pendingOpsRef.current.delete(requestID);
        reject(new Error("websocket_timeout"));
      }, 8000);
    });
  }

  function applyRealtimeMessage(message: Record<string, unknown>) {
    if (message.type === "campaign.upsert" && message.campaign) {
      const campaign = normalizeCampaign(message.campaign as Record<string, unknown>);
      setCampaigns((items) => [campaign, ...items.filter((item) => item.id !== campaign.id)]);
      setSelectedCampaignId((current) => current || campaign.id);
    }
    if (message.type === "error_group.resolved") {
      const groupID = String(message.group_id ?? "");
      if (message.campaign) {
        const campaign = normalizeCampaign(message.campaign as Record<string, unknown>);
        setCampaigns((items) => items.map((item) => item.id === campaign.id ? campaign : item));
      }
      if (groupID) setErrorGroups((items) => items.filter((item) => item.id !== groupID));
    }
    if (message.type === "channel.upsert" && message.channel) {
      const raw = message.channel as Record<string, unknown>;
      setChannels((items) => items.map((channel) => channel.code === String(raw.code ?? raw.Code) ? { ...channel, ...normalizeChannelPatch(raw) } : channel));
    }
    if (message.type === "template.upsert" && message.template) {
      const template = normalizeTemplatePatch(message.template as Record<string, unknown>);
      setTemplates((items) => items.some((item) => item.id === template.id) ? items.map((item) => item.id === template.id ? template : item) : [template, ...items]);
    }
    if (message.type === "manager.upsert" && message.manager) {
      const manager = normalizeManagerPatch(message.manager as Record<string, unknown>);
      setManagers((items) => items.some((item) => item.email === manager.email) ? items.map((item) => item.email === manager.email ? { ...item, ...manager } : item) : [manager, ...items]);
    }
    if (message.type === "health.snapshot" && Array.isArray(message.services)) {
      setHealthChecks(message.services.map((item) => normalizeHealthPatch(item as Record<string, unknown>)));
    }
  }

  function applyCampaignSnapshot(snapshot: Record<string, unknown>) {
    setCampaigns((items) => items.map((campaign) => {
      if (campaign.id !== snapshot.campaign_id) return campaign;
      const nextStatus = String(snapshot.status ?? campaign.status) as Campaign["status"];
      const frozenStatus = campaign.status === "stopped" || campaign.status === "cancelled" || campaign.status === "finished";
      const staleActiveSnapshot = nextStatus === "running" || nextStatus === "retrying";
      if (frozenStatus && staleActiveSnapshot) return campaign;
      return {
        ...campaign,
        status: nextStatus,
        totalMessages: Number(snapshot.total_messages ?? campaign.totalMessages),
        processed: Number(snapshot.processed ?? campaign.processed),
        success: Number(snapshot.success ?? campaign.success),
        failed: Number(snapshot.failed ?? campaign.failed),
        cancelled: Number(snapshot.cancelled ?? campaign.cancelled),
      };
    }));
  }

  async function createCampaign(wizard: WizardState) {
    const template = templates.find((item) => item.id === wizard.templateId) ?? templates[0];
    const totalRecipients = audiencePreview(wizard.filter);
    const totalMessages = totalRecipients * wizard.selectedChannels.length;
    try {
      const result = await sendOpsCommand("campaign.create", {
        name: wizard.name,
        template_id: template.id,
        filters: wizard.filter,
        selected_channels: wizard.selectedChannels,
        total_recipients: totalRecipients,
      });
      const backendCampaign = normalizeCampaign((result.campaign ?? result) as Record<string, unknown>);
      setCampaigns((items) => [backendCampaign, ...items.filter((item) => item.id !== backendCampaign.id)]);
      setSelectedCampaignId(backendCampaign.id);
      setApiState("websocket commands");
      addEvent("info", "campaign.started", `Backend campaign ${backendCampaign.name} started`, "campaign-service");
      return;
    } catch {
      setApiState("local fallback");
    }
    const campaign: Campaign = {
      id: id("cmp"),
      name: wizard.name.trim() || "Новая кампания",
      templateId: template.id,
      templateName: template.name,
      status: "running",
      filters: wizard.filter,
      selectedChannels: wizard.selectedChannels,
      totalRecipients,
      totalMessages,
      processed: 0,
      success: 0,
      failed: 0,
      cancelled: 0,
      p95DispatchMs: Math.min(1320, 720 + wizard.selectedChannels.length * 54),
      createdAt: new Date().toISOString(),
      startedAt: new Date().toISOString(),
    };
    setCampaigns((items) => [campaign, ...items]);
    setDeliveries((items) => [...createDeliveries(campaign), ...items]);
    setErrorGroups((items) => [...buildLocalErrorGroups(campaign), ...items]);
    setSelectedCampaignId(campaign.id);
    addEvent("info", "campaign.started", `Campaign ${campaign.name} queued with ${totalMessages.toLocaleString()} messages`, "campaign-service");
  }

  function updateTemplate(template: Template) {
    setTemplates((items) => {
      const exists = items.some((item) => item.id === template.id);
      if (exists) return items.map((item) => (item.id === template.id ? template : item));
      return [template, ...items];
    });
    void sendOpsCommand("template.save", { template }).catch(() => undefined);
    addEvent("info", "template.saved", `Template ${template.name} saved`, "template-service");
  }

  function updateChannel(code: string, patch: Partial<Channel>) {
    if (userSession.role !== "admin") return;
    const current = channels.find((channel) => channel.code === code);
    const updated = current ? { ...current, ...patch } : patch;
    setChannels((items) => items.map((channel) => (channel.code === code ? { ...channel, ...patch } : channel)));
    void sendOpsCommand("channel.update", { code, channel: updated }).catch(() => undefined);
    addEvent("info", "channel.updated", `${code} settings changed`, "channel-service");
  }

  function addManager(email: string, role: Role) {
    if (userSession.role !== "admin" || !email.includes("@")) return;
    setManagers((items) => [{ id: id("manager"), email, role, active: true }, ...items]);
    void sendOpsCommand("manager.add", { email, role }).catch(() => undefined);
    addEvent("info", "manager.created", `${email} added as ${role}`, "auth-service");
  }

  async function handleCampaignAction(action: CampaignCommand, campaignId = selectedCampaign?.id) {
    const targetCampaign = campaigns.find((campaign) => campaign.id === campaignId);
    if (!targetCampaign) return;
    void sendOpsCommand("campaign.action", { campaign_id: targetCampaign.id, action }).then((message) => {
      if (message.campaign) {
        const updated = normalizeCampaign(message.campaign as Record<string, unknown>);
        setCampaigns((items) => items.map((campaign) => campaign.id === updated.id ? updated : campaign));
      }
      setApiState("websocket commands");
    }).catch(() => setApiState("local fallback"));
    setCampaigns((items) =>
      items.map((campaign) => {
        if (campaign.id !== targetCampaign.id) return campaign;
        if (action === "start") return { ...campaign, status: "running", startedAt: new Date().toISOString() };
        if (action === "stop") return { ...campaign, status: "stopped" };
        if (action === "retry") {
          const retryCount = campaign.failed;
          return { ...campaign, status: "retrying", totalMessages: campaign.totalMessages + retryCount, failed: 0 };
        }
        if (action === "switch_channel") {
          const selectedChannels = campaign.selectedChannels.includes("telegram")
            ? campaign.selectedChannels.map((channel) => (channel === "telegram" ? "sms" : channel))
            : [...new Set([...campaign.selectedChannels, "email"])];
          return { ...campaign, status: "retrying", selectedChannels, totalMessages: campaign.totalRecipients * selectedChannels.length, failed: 0 };
        }
        const cancelled = Math.max(0, campaign.totalMessages - campaign.processed);
        return { ...campaign, status: "cancelled", cancelled, processed: campaign.totalMessages, finishedAt: new Date().toISOString() };
      }),
    );
    if (action !== "start" && action !== "stop") {
      setErrorGroups((items) => items.filter((group) => group.campaignId !== targetCampaign.id));
    }
    addEvent("warn", `campaign.${action}`, `${targetCampaign.name}: ${action}`, "campaign-service");
  }

  async function handleErrorGroupAction(group: ErrorGroup, action: "retry" | "switch_channel" | "cancel_group", toChannel?: string) {
    if (!selectedCampaign) return;
    const targetChannel = toChannel ?? channels.find((channel) => channel.enabled && channel.code !== group.channelCode)?.code ?? "";
    void sendOpsCommand("error_group.action", {
      campaign_id: selectedCampaign.id,
      group_id: group.id,
      action,
      to_channel: targetChannel,
    }).then((message) => {
      if (message.campaign) {
        const campaign = normalizeCampaign(message.campaign as Record<string, unknown>);
        setCampaigns((items) => items.map((item) => item.id === campaign.id ? campaign : item));
      }
      setApiState("websocket commands");
    }).catch(() => setApiState("local fallback"));
    setErrorGroups((items) => items.filter((item) => item.id !== group.id));
    setCampaigns((items) => items.map((campaign) => {
      if (campaign.id !== selectedCampaign.id) return campaign;
      const failed = Math.max(0, campaign.failed - group.failedCount);
      const processed = action === "cancel_group" ? campaign.processed : Math.max(0, campaign.processed - group.failedCount);
      const cancelled = action === "cancel_group" ? campaign.cancelled + group.failedCount : campaign.cancelled;
      return {
        ...campaign,
        status: failed === 0 && processed >= campaign.totalMessages ? "finished" : "retrying",
        processed,
        failed,
        cancelled,
        selectedChannels: action === "switch_channel" && targetChannel ? [...new Set([...campaign.selectedChannels, targetChannel])] : campaign.selectedChannels,
      };
    }));
    addEvent("warn", `error_group.${action}`, `${group.channelCode}/${group.errorCode}: группа обработана локально`, "campaign-service");
  }

  async function refreshHealthViaWebSocket() {
    const fallback = new Promise<ServiceHealth[]>((resolve, reject) => {
      window.setTimeout(() => {
        void fetchServiceHealth().then(resolve).catch(reject);
      }, 250);
    });
    try {
      const result = await Promise.race([
        sendOpsCommand("health.check", {}).then((message) => Array.isArray(message.services) ? message.services.map((item) => normalizeHealthPatch(item as Record<string, unknown>)) : fetchServiceHealth()),
        fallback,
      ]);
      setHealthChecks(result);
      return result;
    } catch {
      const result = await fetchServiceHealth();
      setHealthChecks(result);
      return result;
    }
  }

  return (
    <main className="appShell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brandMark">N</span>
          <span><strong>Norify</strong><small>Notification platform</small></span>
        </div>
        <nav className="navList">
          {visibleNavigation.map((item) => (
            <button key={item.id} className={screen === item.id ? "navItem active" : "navItem"} onClick={() => setScreen(item.id)}>
              <Icon name={item.icon} /> <span>{item.label}</span>
            </button>
          ))}
        </nav>
        <div className="sessionBox">
          <span>{userSession.email}</span>
          <strong>{userSession.role}</strong>
          <button onClick={() => setSession(null)}><Icon name="logout" /> Logout</button>
        </div>
      </aside>

      <section className="workspace">
        <header className="topbar">
          <div>
            <h1>{titleFor(screen)}</h1>
            <p>{subtitleFor(screen)}</p>
          </div>
          <div className="topbarActions">
            <span className="apiState">{apiState}</span>
            <select value={selectedCampaign?.id ?? ""} onChange={(event) => setSelectedCampaignId(event.target.value)}>
              {campaigns.map((campaign) => <option key={campaign.id} value={campaign.id}>{campaign.name}</option>)}
            </select>
            <button className="primary" onClick={() => setScreen("create")}><Icon name="plus" /> New campaign</button>
          </div>
        </header>

        {screen === "dashboard" && selectedCampaign && (
          <Dashboard
            campaign={selectedCampaign}
            channels={channels}
            error={activeError}
            errorGroups={errorGroups.filter((group) => group.campaignId === selectedCampaign.id)}
            onAction={handleCampaignAction}
            onGroupAction={handleErrorGroupAction}
          />
        )}
        {screen === "campaigns" && <CampaignList campaigns={campaigns} selectedId={selectedCampaign?.id} onSelect={(id) => setSelectedCampaignId(id)} onAction={(campaignId, action) => handleCampaignAction(action, campaignId)} />}
        {screen === "create" && <CreateCampaign templates={templates} channels={channels} onCreate={createCampaign} />}
        {screen === "templates" && <Templates templates={templates} onSave={updateTemplate} />}
        {screen === "channels" && <Channels channels={channels} role={userSession.role} onUpdate={updateChannel} />}
        {screen === "deliveries" && selectedCampaign && <Deliveries deliveries={deliveries.filter((delivery) => delivery.campaignId === selectedCampaign.id)} />}
        {screen === "stats" && <Stats campaigns={campaigns} channels={channels} />}
        {screen === "managers" && <Managers role={userSession.role} managers={managers} onAdd={addManager} />}
        {screen === "health" && <Health events={events} checks={healthChecks} onRefresh={refreshHealthViaWebSocket} />}
        {screen === "logs" && <Logs events={events} />}
      </section>
    </main>
  );
}

function Login({ onLogin }: { onLogin: (session: { email: string; role: Role }) => void }) {
  const [email, setEmail] = useState(credentials.admin.email);
  const [password, setPassword] = useState(credentials.admin.password);
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    try {
      onLogin(await backendLogin(email, password));
      return;
    } catch {
      // Fall back to local demo credentials when backend is not started.
    }
    const role = (Object.keys(credentials) as Role[]).find((item) => credentials[item].email === email && credentials[item].password === password);
    if (!role) {
      setError("Неверный email или пароль");
      return;
    }
    onLogin({ email, role });
  }

  return (
    <main className="loginShell">
      <section className="loginPanel">
        <div className="brand loginBrand"><span className="brandMark">N</span><span><strong>Norify</strong><small>Notification platform</small></span></div>
        <form onSubmit={submit} className="loginForm">
          <h1>Вход в кабинет</h1>
          <label>Email<input value={email} onChange={(event) => setEmail(event.target.value)} /></label>
          <label>Пароль<input type="password" value={password} onChange={(event) => setPassword(event.target.value)} /></label>
          {error && <div className="inlineError">{error}</div>}
          <button className="primary"><Icon name="login" /> Login</button>
        </form>
        <div className="loginHints">
          <button type="button" onClick={() => { setEmail(credentials.admin.email); setPassword(credentials.admin.password); }}>admin@example.com</button>
          <button type="button" onClick={() => { setEmail(credentials.manager.email); setPassword(credentials.manager.password); }}>manager@example.com</button>
        </div>
      </section>
    </main>
  );
}

function Dashboard({
  campaign,
  channels,
  error,
  errorGroups,
  onAction,
  onGroupAction,
}: {
  campaign: Campaign;
  channels: Channel[];
  error: ActionableError;
  errorGroups: ErrorGroup[];
  onAction: (action: CampaignCommand) => void;
  onGroupAction: (group: ErrorGroup, action: "retry" | "switch_channel" | "cancel_group", toChannel?: string) => void;
}) {
  const percent = progressPercent(campaign);
  const pending = Math.max(0, campaign.totalMessages - campaign.processed);
  return (
    <div className="opsGrid">
      <section className="panel campaignHeaderPanel wide">
        <div className="campaignTitleBlock">
          <div>
            <span className={`status status-${campaign.status}`}>{campaign.status}</span>
            <h2>{campaign.name}</h2>
            <p className="muted">{campaign.templateName || campaign.templateId} · {campaign.selectedChannels.join(" / ")}</p>
          </div>
        </div>
        <div className="campaignControlBar">
          <CampaignPlayerControls campaign={campaign} onAction={onAction} />
          {campaign.failed > 0 && (
            <div className="recoveryActions" aria-label="Campaign recovery actions">
              <button onClick={() => onAction("retry")}><Icon name="retry" /> Retry failed</button>
              <button onClick={() => onAction("switch_channel")}><Icon name="switch" /> Switch channel</button>
            </div>
          )}
        </div>

        <div className="progressStack">
          <div className="progressTop">
            <strong>{percent}%</strong>
            <span>{campaign.processed.toLocaleString()} / {campaign.totalMessages.toLocaleString()}</span>
          </div>
          <div className="progressLine"><div style={{ width: `${percent}%` }} /></div>
        </div>

        <div className="campaignMetaGrid">
          <Metric label="Pending" value={pending.toLocaleString()} />
          <Metric label="Success" value={campaign.success.toLocaleString()} tone="success" />
          <Metric label="Failed" value={campaign.failed.toLocaleString()} tone="danger" />
          <Metric label="Recipients" value={campaign.totalRecipients.toLocaleString()} />
          <Metric label="Channels" value={String(campaign.selectedChannels.length)} />
          <Metric label="p95 dispatch" value={formatP95Dispatch(campaign)} />
        </div>
      </section>

      <section className="panel">
        <div className="panelHeader"><h2>Dispatch lanes</h2><span>{campaign.selectedChannels.length} active</span></div>
        <div className="splitGrid compact">
          {campaign.selectedChannels.map((channel, index) => (
            <div key={channel} className="splitRow">
              <span>{channel}</span>
              <div><i style={{ width: `${Math.max(18, 88 - index * 9)}%` }} /></div>
              <strong>{Math.round((campaign.success / Math.max(1, campaign.selectedChannels.length)) * (1 - index * 0.04)).toLocaleString()}</strong>
            </div>
          ))}
          </div>
      </section>

      <ErrorGroupsPanel
        campaign={campaign}
        channels={channels}
        error={error}
        groups={errorGroups}
        onAction={onAction}
        onGroupAction={onGroupAction}
      />
    </div>
  );
}

function CampaignPlayerControls({ campaign, onAction }: { campaign: Campaign; onAction: (action: CampaignCommand) => void }) {
  const isRunning = campaign.status === "running" || campaign.status === "retrying";
  const isComplete = campaign.status === "cancelled" || campaign.status === "finished";
  const canStart = campaign.status === "created" || campaign.status === "stopped";
  const startLabel = campaign.status === "stopped" ? "Продолжить" : "Запустить";

  return (
    <div className="transportControls" aria-label="Campaign player controls">
      <button className="primary" disabled={!canStart} onClick={() => onAction("start")}>
        <Icon name="play" /> {startLabel}
      </button>
      <button disabled={!isRunning} onClick={() => onAction("stop")}>
        <Icon name="stop" /> Остановить
      </button>
      <button className="danger" disabled={isComplete} onClick={() => onAction("cancel_campaign")}>
        <Icon name="cancel" /> Отменить
      </button>
    </div>
  );
}

function ErrorGroupsPanel({
  campaign,
  channels,
  error,
  groups,
  onAction,
  onGroupAction,
}: {
  campaign: Campaign;
  channels: Channel[];
  error: ActionableError;
  groups: ErrorGroup[];
  onAction: (action: CampaignCommand) => void;
  onGroupAction: (group: ErrorGroup, action: "retry" | "switch_channel" | "cancel_group", toChannel?: string) => void;
}) {
  return (
    <section className="panel errorGroupsPanel">
      <div className="panelHeader">
        <h2>Группы ошибок</h2>
        <span className="queueMode"><Icon name="alert" /> realtime queue</span>
      </div>
      {groups.length > 0 ? (
        <>
          <p className="muted">Основная кампания не прерывается; выбранные сообщения вставляются в текущую очередь с повышенным приоритетом.</p>
          <div className="errorGroups">
            {groups.map((group) => <ErrorGroupCard key={group.id} group={group} channels={channels} onAction={onGroupAction} />)}
          </div>
        </>
      ) : (
        <div className="emptyState">
          <strong>Нет активных групп ошибок</strong>
          <span>Основная отправка продолжается. Новые сбои появятся здесь отдельными группами для точечного решения.</span>
          {campaign.failed > 0 && (
            <div className="buttonRow">
              <button onClick={() => onAction("retry")}><Icon name="retry" /> {error.actions[0].label}</button>
              <button onClick={() => onAction("switch_channel")}><Icon name="switch" /> {error.actions[1].label}</button>
              <button className="danger" onClick={() => onAction("cancel_campaign")}><Icon name="stop" /> {error.actions[2].label}</button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function ErrorGroupCard({
  group,
  channels,
  onAction,
}: {
  group: ErrorGroup;
  channels: Channel[];
  onAction: (group: ErrorGroup, action: "retry" | "switch_channel" | "cancel_group", toChannel?: string) => void;
}) {
  const alternativeChannels = channels.filter((channel) => channel.enabled && channel.code !== group.channelCode);
  const [selectedChannel, setSelectedChannel] = useState(alternativeChannels[0]?.code ?? "");
  const actions = group.recommendedActions.length > 0 ? group.recommendedActions : [
    { code: "retry", label: "Повторить группу" },
    { code: "switch_channel", label: "Вставить через другой канал" },
    { code: "cancel_group", label: "Закрыть группу" },
  ];
  return (
    <article className="errorGroupCard">
      <div className="groupHeader">
        <div>
          <strong>{group.channelCode}</strong>
          <span>{group.errorCode || "delivery_failed"}</span>
        </div>
        <b>{group.failedCount.toLocaleString()}</b>
      </div>
      <p>{group.errorMessage || "Ошибка доставки без сообщения адаптера"}</p>
      <div className="groupMeta">
        <span>attempt {group.maxAttempt}</span>
        <span>{formatDate(group.firstSeenAt)} → {formatDate(group.lastSeenAt)}</span>
      </div>
      <div className="groupImpact">{group.impact}</div>
      <div className="groupActions">
        {actions.map((action) => {
          const code = action.code as "retry" | "switch_channel" | "cancel_group";
          if (code === "switch_channel") {
            return (
              <div key={code} className="switchChannelAction">
                <label>Альтернативный канал
                  <select value={selectedChannel} onChange={(event) => setSelectedChannel(event.target.value)}>
                    {alternativeChannels.map((channel) => <option key={channel.code} value={channel.code}>{channel.name}</option>)}
                  </select>
                </label>
                <button disabled={!selectedChannel} onClick={() => onAction(group, code, selectedChannel)}>
                  <Icon name="switch" /> Вставить
                </button>
              </div>
            );
          }
          return (
            <button key={code} className={code === "cancel_group" ? "danger" : ""} onClick={() => onAction(group, code)}>
              <Icon name={code === "retry" ? "retry" : "stop"} /> {action.label}
            </button>
          );
        })}
      </div>
    </article>
  );
}

function CampaignList({ campaigns, selectedId, onSelect, onAction }: { campaigns: Campaign[]; selectedId?: string; onSelect: (id: string) => void; onAction: (campaignId: string, action: "start" | "retry" | "cancel_campaign") => void }) {
  return (
    <section className="panel wide campaignListPanel">
      <div className="panelHeader"><h2>Campaign queue</h2><span>{campaigns.filter((campaign) => campaign.status === "running" || campaign.status === "retrying").length} active / {campaigns.length} total</span></div>
      <div className="tableWrap">
        <table>
          <thead><tr><th>Name</th><th>Status</th><th>Progress</th><th>Audience</th><th>Messages</th><th>p95</th><th /></tr></thead>
          <tbody>
            {campaigns.map((campaign) => (
              <tr key={campaign.id} className={selectedId === campaign.id ? "selectedRow" : ""}>
                <td><button className="linkButton" onClick={() => onSelect(campaign.id)}>{campaign.name}</button></td>
                <td><span className={`status status-${campaign.status}`}>{campaign.status}</span></td>
                <td>
                  <div className="tableProgress">
                    <div className="progressLine"><div style={{ width: `${progressPercent(campaign)}%` }} /></div>
                    <span>{progressPercent(campaign)}%</span>
                  </div>
                </td>
                <td>{campaign.totalRecipients.toLocaleString()}</td>
                <td>{campaign.totalMessages.toLocaleString()}</td>
                <td>{formatP95Dispatch(campaign)}</td>
                <td className="tableActions">
                  {campaign.status === "created" && <button onClick={() => onAction(campaign.id, "start")}><Icon name="play" /> Start</button>}
                  {campaign.failed > 0 && <button onClick={() => onAction(campaign.id, "retry")}><Icon name="retry" /> Retry</button>}
                  {(campaign.status === "running" || campaign.status === "retrying") && <button className="danger" onClick={() => onAction(campaign.id, "cancel_campaign")}><Icon name="stop" /> Cancel</button>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function CreateCampaign({ templates, channels, onCreate }: { templates: Template[]; channels: Channel[]; onCreate: (wizard: WizardState) => void | Promise<void> }) {
  const enabledChannels = channels.filter((channel) => channel.enabled);
  const [wizard, setWizard] = useState<WizardState>({
    name: "Новая кампания",
    templateId: templates[0]?.id ?? "",
    filter: defaultFilter,
    selectedChannels: enabledChannels.slice(0, 3).map((channel) => channel.code),
  });
  const preview = audiencePreview(wizard.filter);
  const totalMessages = preview * wizard.selectedChannels.length;
  const canStart = wizard.name.trim().length > 2 && wizard.templateId && wizard.selectedChannels.length > 0;

  function toggleChannel(code: string) {
    setWizard((current) => ({
      ...current,
      selectedChannels: current.selectedChannels.includes(code)
        ? current.selectedChannels.filter((item) => item !== code)
        : [...current.selectedChannels, code],
    }));
  }

  return (
    <div className="createLayout">
      <section className="panel">
        <div className="panelHeader"><h2>Campaign</h2><Icon name="campaign" /></div>
        <label>Name<input value={wizard.name} onChange={(event) => setWizard({ ...wizard, name: event.target.value })} /></label>
        <label>Template
          <select value={wizard.templateId} onChange={(event) => setWizard({ ...wizard, templateId: event.target.value })}>
            {templates.map((template) => <option key={template.id} value={template.id}>{template.name} · v{template.version}</option>)}
          </select>
        </label>
        <div className="templatePreview">{templates.find((template) => template.id === wizard.templateId)?.body}</div>
      </section>

      <section className="panel">
        <div className="panelHeader"><h2>Audience</h2><strong>{preview.toLocaleString()}</strong></div>
        <div className="fieldGrid">
          <label>Min age<input type="number" value={wizard.filter.minAge} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, minAge: Number(event.target.value) } })} /></label>
          <label>Max age<input type="number" value={wizard.filter.maxAge} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, maxAge: Number(event.target.value) } })} /></label>
        </div>
        <label>Gender
          <select value={wizard.filter.gender} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, gender: event.target.value as AudienceFilter["gender"] } })}>
            <option value="any">any</option><option value="female">female</option><option value="male">male</option>
          </select>
        </label>
        <label>Location
          <select value={wizard.filter.location} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, location: event.target.value } })}>
            <option value="all">all</option><option value="Moscow">Moscow</option><option value="Kazan">Kazan</option><option value="Saint Petersburg">Saint Petersburg</option>
          </select>
        </label>
        <label>Tags<input value={wizard.filter.tags.join(", ")} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, tags: event.target.value.split(",").map((tag) => tag.trim()).filter(Boolean) } })} /></label>
      </section>

      <section className="panel">
        <div className="panelHeader"><h2>Channels</h2><strong>{totalMessages.toLocaleString()}</strong></div>
        <div className="choiceList">
          {enabledChannels.map((channel) => (
            <button key={channel.code} className={wizard.selectedChannels.includes(channel.code) ? "choice selected" : "choice"} onClick={() => toggleChannel(channel.code)}>
              <span><Icon name="channel" /> {channel.name}</span>
              <small>{Math.round(channel.successProbability * 100)}%</small>
            </button>
          ))}
        </div>
        <button className="primary startButton" disabled={!canStart} onClick={() => onCreate(wizard)}><Icon name="play" /> Start campaign</button>
      </section>
    </div>
  );
}

function Templates({ templates, onSave }: { templates: Template[]; onSave: (template: Template) => void }) {
  const initialTemplate = templates[0] ?? createBlankTemplate();
  const [editing, setEditing] = useState<Template>(initialTemplate);
  const [query, setQuery] = useState("");
  const declaredVariables = normalizeVariables(editing.variables.join(", "));
  const detectedVariables = extractTemplateVariables(editing.body);
  const missingVariables = detectedVariables.filter((variable) => !declaredVariables.includes(variable));
  const unusedVariables = declaredVariables.filter((variable) => !detectedVariables.includes(variable));
  const preview = renderTemplatePreview(editing.body);
  const isValid = editing.name.trim().length > 1 && editing.body.trim().length > 0 && missingVariables.length === 0;
  const visibleTemplates = templates.filter((template) => {
    const haystack = `${template.name} ${template.body} ${template.variables.join(" ")}`.toLowerCase();
    return haystack.includes(query.trim().toLowerCase());
  });

  function updateVariables(value: string) {
    setEditing({ ...editing, variables: normalizeVariables(value) });
  }

  function insertVariable(variable: string) {
    const token = `{{${variable}}}`;
    setEditing((current) => ({
      ...current,
      body: current.body.includes(token) ? current.body : `${current.body.trimEnd()} ${token}`.trim(),
    }));
  }

  function saveTemplate() {
    if (!isValid) return;
    onSave({ ...editing, variables: declaredVariables, version: editing.version + 1, updatedAt: new Date().toISOString() });
  }

  return (
    <div className="templatesLayout">
      <section className="panel templateLibraryPanel">
        <div className="panelHeader">
          <h2>Template library</h2>
          <span>{templates.length} total</span>
        </div>
        <label>Search<input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="name, text, variable" /></label>
        <div className="templateList">
          {visibleTemplates.map((template) => (
            <button key={template.id} className={editing.id === template.id ? "templateListItem selected" : "templateListItem"} onClick={() => setEditing(template)}>
              <span className="templateListTop"><strong>{template.name}</strong><b>v{template.version}</b></span>
              <span className="templateSnippet">{template.body}</span>
              <span className="templateMiniChips">{template.variables.map((variable) => <small key={variable}>{variable}</small>)}</span>
            </button>
          ))}
          {visibleTemplates.length === 0 && <div className="emptyState"><strong>Ничего не найдено</strong><span>Измените поиск или создайте новый шаблон.</span></div>}
        </div>
      </section>

      <section className="panel templateComposerPanel">
        <div className="panelHeader">
          <h2>Template composer</h2>
          <button onClick={() => setEditing(createBlankTemplate())}><Icon name="plus" /> New</button>
        </div>
        <div className="fieldGrid">
          <label>Name<input value={editing.name} onChange={(event) => setEditing({ ...editing, name: event.target.value })} /></label>
          <label>Version<input readOnly value={`v${editing.version}`} /></label>
        </div>
        <label>Message body<textarea className="templateBodyInput" value={editing.body} onChange={(event) => setEditing({ ...editing, body: event.target.value })} /></label>

        <div className="templateVariableGrid">
          <div>
            <div className="templateSectionLabel">Detected in body</div>
            <div className="variableChips">
              {detectedVariables.length > 0 ? detectedVariables.map((variable) => <span key={variable} className="variableChip detected">{variable}</span>) : <span className="variableChip mutedChip">none</span>}
            </div>
          </div>
          <div>
            <div className="templateSectionLabel">Declared variables</div>
            <input value={declaredVariables.join(", ")} onChange={(event) => updateVariables(event.target.value)} />
          </div>
        </div>

        {declaredVariables.length > 0 && (
          <div>
            <div className="templateSectionLabel">Quick insert</div>
            <div className="variableChips">
              {declaredVariables.map((variable) => (
                <button key={variable} className="variableChip actionChip" onClick={() => insertVariable(variable)}>{variable}</button>
              ))}
            </div>
          </div>
        )}

        <div className={isValid ? "templateValidation valid" : "templateValidation invalid"}>
          {isValid ? "Ready to save" : missingVariables.length > 0 ? `Не объявлены: ${missingVariables.join(", ")}` : "Заполните название и текст"}
          {unusedVariables.length > 0 && <span>Не используются: {unusedVariables.join(", ")}</span>}
        </div>

        <button className="primary saveTemplateButton" disabled={!isValid} onClick={saveTemplate}><Icon name="save" /> Save version</button>
      </section>

      <section className="panel templatePreviewPanel">
        <div className="panelHeader">
          <h2>Live preview</h2>
          <span>{editing.body.length} chars</span>
        </div>
        <div className="messagePreview">
          <div className="messageBubble">{preview || "..."}</div>
        </div>
        <div className="templateMetaGrid">
          <Metric label="variables" value={String(declaredVariables.length)} />
          <Metric label="detected" value={String(detectedVariables.length)} />
          <Metric label="version" value={`v${editing.version}`} />
        </div>
        <div className="previewSamples">
          {Object.entries(templateSampleValues).slice(0, 4).map(([key, value]) => (
            <div key={key}><span>{key}</span><strong>{value}</strong></div>
          ))}
        </div>
      </section>
    </div>
  );
}

const templateVariablePattern = /\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g;
const templateSampleValues: Record<string, string> = {
  first_name: "Анна",
  order_id: "48291",
  promo_code: "SPRING20",
  city: "Москва",
};

function createBlankTemplate(): Template {
  return {
    id: id("tpl"),
    name: "Новый шаблон",
    body: "Здравствуйте, {{first_name}}",
    variables: ["first_name"],
    version: 0,
    updatedAt: new Date().toISOString(),
  };
}

function normalizeVariables(value: string | string[]): string[] {
  const raw = Array.isArray(value) ? value : value.split(",");
  return [...new Set(raw.map((item) => item.trim()).filter(Boolean))];
}

function extractTemplateVariables(body: string): string[] {
  const variables = new Set<string>();
  for (const match of body.matchAll(templateVariablePattern)) {
    variables.add(match[1]);
  }
  return [...variables];
}

function renderTemplatePreview(body: string): string {
  return body.replace(templateVariablePattern, (_, variable: string) => templateSampleValues[variable] ?? `[${variable}]`);
}

function Channels({ channels, role, onUpdate }: { channels: Channel[]; role: Role; onUpdate: (code: string, patch: Partial<Channel>) => void }) {
  return (
    <section className="panel wide">
      <div className="panelHeader"><h2>Channel registry</h2><span>{role === "admin" ? "live delivery stats" : "read only"}</span></div>
      <div className="channelGrid">
        {channels.map((channel) => (
          <article key={channel.code} className={channel.degraded ? "channelCard degraded" : "channelCard"}>
            <div className="channelTop"><strong>{channel.name}</strong><span className={channel.enabled ? "status status-running" : "status status-cancelled"}>{channel.enabled ? "enabled" : "disabled"}</span></div>
            <div className="compactMetrics">
              <Metric label="success" value={formatChannelSuccess(channel)} />
              <Metric label="parallel" value={String(channel.maxParallelism)} />
              <Metric label="avg attempt" value={formatAverageAttempt(channel)} />
            </div>
            <div className="channelStatsMeta">
              <span>{(channel.deliveryTotal ?? 0).toLocaleString()} total</span>
              <span>{(channel.deliverySent ?? 0).toLocaleString()} sent</span>
              <span>{(channel.deliveryFailed ?? 0).toLocaleString()} failed</span>
              <span>{(channel.deliveryQueued ?? 0).toLocaleString()} queued</span>
            </div>
            <label>Configured probability<input disabled={role !== "admin"} type="range" min="0.5" max="1" step="0.01" value={channel.successProbability} onChange={(event) => onUpdate(channel.code, { successProbability: Number(event.target.value) })} /></label>
            <div className="buttonRow">
              <button disabled={role !== "admin"} onClick={() => onUpdate(channel.code, { enabled: !channel.enabled })}>{channel.enabled ? <Icon name="stop" /> : <Icon name="play" />} {channel.enabled ? "Disable" : "Enable"}</button>
              <button disabled={role !== "admin"} onClick={() => onUpdate(channel.code, { degraded: !channel.degraded })}><Icon name="pulse" /> {channel.degraded ? "Clear" : "Degrade"}</button>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

function formatChannelSuccess(channel: Channel) {
  if (!channel.deliveryTotal || channel.deliverySuccessRate === null || channel.deliverySuccessRate === undefined) return "no data";
  return `${Math.round(channel.deliverySuccessRate * 100)}%`;
}

function formatAverageAttempt(channel: Channel) {
  if (!channel.deliveryTotal || channel.averageAttempt === null || channel.averageAttempt === undefined) return "no data";
  return channel.averageAttempt.toFixed(channel.averageAttempt >= 10 ? 0 : 1);
}

function normalizeChannelPatch(raw: Record<string, unknown>): Partial<Channel> {
  const code = String(raw.code ?? raw.Code ?? "");
  return {
    code,
    name: String(raw.name ?? raw.Name ?? code),
    enabled: Boolean(raw.enabled ?? raw.Enabled),
    successProbability: Number(raw.success_probability ?? raw.SuccessProbability ?? 0.92),
    minDelaySeconds: Number(raw.min_delay_seconds ?? raw.MinDelaySeconds ?? 2),
    maxDelaySeconds: Number(raw.max_delay_seconds ?? raw.MaxDelaySeconds ?? 300),
    maxParallelism: Number(raw.max_parallelism ?? raw.MaxParallelism ?? 100),
    retryLimit: Number(raw.retry_limit ?? raw.RetryLimit ?? 3),
    deliveryTotal: Number(raw.delivery_total ?? raw.DeliveryTotal ?? 0),
    deliverySent: Number(raw.delivery_sent ?? raw.DeliverySent ?? 0),
    deliveryFailed: Number(raw.delivery_failed ?? raw.DeliveryFailed ?? 0),
    deliveryQueued: Number(raw.delivery_queued ?? raw.DeliveryQueued ?? 0),
    deliveryCancelled: Number(raw.delivery_cancelled ?? raw.DeliveryCancelled ?? 0),
    deliverySuccessRate: optionalNumber(raw.delivery_success_rate ?? raw.DeliverySuccessRate),
    averageAttempt: optionalNumber(raw.average_attempt ?? raw.AverageAttempt),
  };
}

function normalizeTemplatePatch(raw: Record<string, unknown>): Template {
  return {
    id: String(raw.id ?? raw.ID ?? ""),
    name: String(raw.name ?? raw.Name ?? "Новый шаблон"),
    body: String(raw.body ?? raw.Body ?? ""),
    variables: (raw.variables ?? raw.Variables ?? []) as string[],
    version: Number(raw.version ?? raw.Version ?? 1),
    updatedAt: String(raw.updated_at ?? raw.UpdatedAt ?? new Date().toISOString()),
  };
}

function normalizeManagerPatch(raw: Record<string, unknown>): Manager {
  const email = String(raw.email ?? raw.Email ?? "");
  return {
    id: String(raw.id ?? raw.ID ?? email),
    email,
    role: String(raw.role ?? raw.Role ?? "manager") as Role,
    active: Boolean(raw.active ?? raw.Active ?? true),
  };
}

function normalizeHealthPatch(raw: Record<string, unknown>): ServiceHealth {
  return {
    id: String(raw.id ?? raw.ID ?? raw.name ?? raw.Name ?? ""),
    name: String(raw.name ?? raw.Name ?? raw.id ?? raw.ID ?? ""),
    url: String(raw.url ?? raw.URL ?? ""),
    status: String(raw.status ?? raw.Status ?? "checking") as ServiceHealth["status"],
    latencyMs: Number(raw.latency_ms ?? raw.latencyMs ?? raw.LatencyMs ?? 0),
    checkedAt: String(raw.checked_at ?? raw.checkedAt ?? raw.CheckedAt ?? new Date().toISOString()),
    detail: String(raw.detail ?? raw.Detail ?? ""),
  };
}

function optionalNumber(value: unknown): number | null {
  if (value === null || value === undefined || value === "") return null;
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function Deliveries({ deliveries }: { deliveries: Delivery[] }) {
  return (
    <section className="panel wide">
      <div className="panelHeader"><h2>Delivery results</h2><span>{deliveries.length} visible rows</span></div>
      <div className="tableWrap">
        <table>
          <thead><tr><th>User</th><th>Channel</th><th>Status</th><th>Attempt</th><th>Error</th><th>Finished</th></tr></thead>
          <tbody>
            {deliveries.map((delivery) => (
              <tr key={delivery.id}>
                <td>{delivery.userId}</td><td>{delivery.channelCode}</td><td><span className={`delivery delivery-${delivery.status}`}>{delivery.status}</span></td><td>{delivery.attempt}</td><td>{delivery.errorMessage ?? "—"}</td><td>{formatDate(delivery.finishedAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Stats({ campaigns, channels }: { campaigns: Campaign[]; channels: Channel[] }) {
  const totals = campaigns.reduce((acc, campaign) => ({
    messages: acc.messages + campaign.totalMessages,
    success: acc.success + campaign.success,
    failed: acc.failed + campaign.failed,
    active: acc.active + (campaign.status === "running" || campaign.status === "retrying" ? 1 : 0),
  }), { messages: 0, success: 0, failed: 0, active: 0 });
  return (
    <div className="pageGrid">
      <section className="panel wide">
        <div className="stats">
          <Metric label="total messages" value={totals.messages.toLocaleString()} />
          <Metric label="success" value={totals.success.toLocaleString()} tone="success" />
          <Metric label="failed" value={totals.failed.toLocaleString()} tone="danger" />
          <Metric label="active campaigns" value={String(totals.active)} />
        </div>
      </section>
      <section className="panel wide">
        <div className="panelHeader"><h2>Channel health</h2><span>{channels.filter((channel) => channel.enabled).length} enabled</span></div>
        <div className="barChart">
          {channels.map((channel) => {
            const rate = channel.deliveryTotal && channel.deliverySuccessRate !== null && channel.deliverySuccessRate !== undefined ? channel.deliverySuccessRate : 0;
            return <div key={channel.code}><span>{channel.code}</span><i style={{ width: `${Math.max(2, rate * 100)}%` }} /><strong>{formatChannelSuccess(channel)}</strong></div>;
          })}
        </div>
      </section>
    </div>
  );
}

function Managers({ role, managers, onAdd }: { role: Role; managers: Manager[]; onAdd: (email: string, role: Role) => void }) {
  const [email, setEmail] = useState("new.manager@example.com");
  const [newRole, setNewRole] = useState<Role>("manager");
  if (role !== "admin") return <AccessDenied />;
  return (
    <div className="twoColumn">
      <section className="panel">
        <div className="panelHeader"><h2>Add manager</h2><Icon name="users" /></div>
        <label>Email<input value={email} onChange={(event) => setEmail(event.target.value)} /></label>
        <label>Role<select value={newRole} onChange={(event) => setNewRole(event.target.value as Role)}><option value="manager">manager</option><option value="admin">admin</option></select></label>
        <button className="primary" onClick={() => onAdd(email, newRole)}><Icon name="plus" /> Add</button>
      </section>
      <section className="panel">
        <div className="panelHeader"><h2>RBAC</h2><span>{managers.length} accounts</span></div>
        <div className="listStack">{managers.map((manager) => <div key={manager.id} className="listItem static"><span><strong>{manager.email}</strong><small>{manager.active ? "active" : "blocked"}</small></span><span>{manager.role}</span></div>)}</div>
      </section>
    </div>
  );
}

function Health({ events, checks, onRefresh }: { events: SystemEvent[]; checks: ServiceHealth[]; onRefresh: () => Promise<ServiceHealth[]> }) {
  const [refreshing, setRefreshing] = useState(false);
  const onRefreshRef = useRef(onRefresh);
  const readyCount = checks.filter((check) => check.status === "ready").length;
  const downCount = checks.filter((check) => check.status === "down").length;

  useEffect(() => {
    onRefreshRef.current = onRefresh;
  }, [onRefresh]);

  async function refreshHealth() {
    setRefreshing(true);
    try {
      await onRefreshRef.current();
    } finally {
      setRefreshing(false);
    }
  }

  useEffect(() => {
    void refreshHealth();
    const timer = window.setInterval(() => void refreshHealth(), 15000);
    return () => window.clearInterval(timer);
  }, []);

  return (
    <div className="pageGrid">
      <section className="panel wide">
        <div className="panelHeader">
          <h2>Microservice readiness</h2>
          <div className="buttonRow tight">
            <span className="healthSummary">{readyCount} ready · {downCount} down</span>
            <button onClick={() => void refreshHealth()} disabled={refreshing}><Icon name="pulse" /> Refresh</button>
          </div>
        </div>
        <div className="healthGrid">
          {checks.map((check) => (
            <div key={check.id} className={`healthItem health-${check.status}`}>
              <span className={`dot ${check.status === "down" ? "dangerDot" : check.status === "checking" ? "warn" : ""}`} />
              <div>
                <strong>{check.name}</strong>
                <small>{check.detail}</small>
              </div>
              <b className={`statusBadge statusBadge-${check.status}`}>{check.status}</b>
            </div>
          ))}
        </div>
        <div className="tableWrap healthTable">
          <table>
            <thead><tr><th>Service</th><th>Status</th><th>Latency</th><th>Endpoint</th><th>Checked</th></tr></thead>
            <tbody>
              {checks.map((check) => (
                <tr key={check.id}>
                  <td>{check.name}</td>
                  <td><span className={`statusBadge statusBadge-${check.status}`}>{check.status}</span></td>
                  <td>{formatLatency(check.latencyMs)}</td>
                  <td className="serviceEndpoint">{check.url}</td>
                  <td>{formatDate(check.checkedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      <section className="panel wide">
        <div className="panelHeader"><h2>Recent system events</h2><span>{events.length}</span></div>
        <EventList events={events.slice(0, 5)} />
      </section>
    </div>
  );
}

function Logs({ events }: { events: SystemEvent[] }) {
  return <section className="panel wide"><div className="panelHeader"><h2>System events</h2><span>structured logs</span></div><EventList events={events} /></section>;
}

function EventList({ events }: { events: SystemEvent[] }) {
  return <div className="eventList">{events.map((event) => <div key={event.id} className={`event event-${event.level}`}><span>{event.level}</span><strong>{event.type}</strong><p>{event.message}</p><small>{event.service} · {formatDate(event.createdAt)}</small></div>)}</div>;
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: "success" | "danger" }) {
  return <div className={`metric ${tone ?? ""}`}><span>{label}</span><strong>{value}</strong></div>;
}

function AccessDenied() {
  return <section className="panel"><div className="panelHeader"><h2>Forbidden</h2><Icon name="alert" /></div><p>Недостаточно прав для административной операции.</p></section>;
}

function useLocalState<T>(key: string, initial: T): [T, Dispatch<SetStateAction<T>>] {
  const [value, setValue] = useState<T>(() => {
    const storage = getStorage();
    const raw = storage.getItem(key);
    if (!raw) return initial;
    try {
      return JSON.parse(raw) as T;
    } catch {
      return initial;
    }
  });
  useEffect(() => {
    getStorage().setItem(key, JSON.stringify(value));
  }, [key, value]);
  return [value, setValue];
}

const memoryStorage = new Map<string, string>();

function getStorage(): Pick<Storage, "getItem" | "setItem"> {
  if (typeof window !== "undefined" && window.localStorage && typeof window.localStorage.getItem === "function") {
    return window.localStorage;
  }
  return {
    getItem: (key: string) => memoryStorage.get(key) ?? null,
    setItem: (key: string, value: string) => {
      memoryStorage.set(key, value);
    },
  };
}

function buildError(campaign: Campaign): ActionableError {
  return {
    title: "Есть неуспешные отправки",
    description: `Кампания ${campaign.name} получила ошибки доставки в одном или нескольких каналах.`,
    impact: `Затронуто ${campaign.failed.toLocaleString()} сообщений. Остальные отправки продолжаются.`,
    actions: currentError.actions,
  };
}

function initialHealthChecks(): ServiceHealth[] {
  return serviceHealthTargets.map((target) => ({
    ...target,
    status: "checking",
    latencyMs: 0,
    checkedAt: new Date().toISOString(),
    detail: "checking",
  }));
}

function createDeliveries(campaign: Campaign): Delivery[] {
  const rows: Delivery[] = [];
  for (let i = 0; i < 24; i += 1) {
    const channel = campaign.selectedChannels[i % campaign.selectedChannels.length];
    rows.push({
      id: id("delivery"),
      campaignId: campaign.id,
      userId: `user-${String(i + 1).padStart(5, "0")}`,
      channelCode: channel,
      status: i % 7 === 0 ? "failed" : i % 5 === 0 ? "queued" : "sent",
      attempt: i % 7 === 0 ? 3 : 1,
      errorCode: i % 7 === 0 ? "stub_delivery_failed" : undefined,
      errorMessage: i % 7 === 0 ? `${channel} returned failed status` : undefined,
      finishedAt: i % 5 === 0 ? undefined : new Date().toISOString(),
    });
  }
  return rows;
}

function buildLocalErrorGroups(campaign: Campaign): ErrorGroup[] {
  const failedCount = Math.max(1, Math.round(campaign.totalMessages * 0.03));
  const channelCode = campaign.selectedChannels.includes("telegram") ? "telegram" : campaign.selectedChannels[0] ?? "email";
  const now = new Date().toISOString();
  return [{
    id: id("group"),
    campaignId: campaign.id,
    channelCode,
    errorCode: "stub_delivery_failed",
    errorMessage: `${channelCode} returned failed status`,
    failedCount,
    maxAttempt: 3,
    firstSeenAt: now,
    lastSeenAt: now,
    impact: `Затронуто ${failedCount.toLocaleString()} сообщений. Основная очередь продолжает обработку.`,
    recommendedActions: [
      { code: "retry", label: "Повторить группу" },
      { code: "switch_channel", label: "Вставить через другой канал" },
      { code: "cancel_group", label: "Закрыть группу" },
    ],
  }];
}

function id(prefix: string) {
  return `${prefix}-${Math.random().toString(36).slice(2, 9)}`;
}

function formatDate(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat("ru-RU", { hour: "2-digit", minute: "2-digit", day: "2-digit", month: "2-digit" }).format(new Date(value));
}

function formatLatency(value: number) {
  return value > 0 ? `${value} ms` : "—";
}

function formatP95Dispatch(campaign: Campaign) {
  if (campaign.p95DispatchMs > 0) return `${campaign.p95DispatchMs} ms`;
  if (campaign.status === "created") return "pending";
  if (campaign.status === "running" || campaign.status === "retrying") return "measuring";
  return "no samples";
}

function titleFor(screen: Screen) {
  const titles: Record<Screen, string> = {
    dashboard: "Campaign control",
    campaigns: "Campaigns",
    create: "Create campaign",
    templates: "Templates",
    channels: "Channels",
    deliveries: "Deliveries",
    stats: "Statistics",
    managers: "Managers",
    health: "Health",
    logs: "System logs",
  };
  return titles[screen];
}

function subtitleFor(screen: Screen) {
  const subtitles: Record<Screen, string> = {
    dashboard: "Realtime dispatch, delivery status, and actionable failures.",
    campaigns: "Queue, cancel, retry, and inspect campaign runs.",
    create: "Audience, template, and channel selection in one flow.",
    templates: "Versioned notification copy with variable validation.",
    channels: "Adapter registry, limits, probabilities, and availability.",
    deliveries: "Per-user and per-channel delivery results.",
    stats: "Aggregated throughput and channel quality.",
    managers: "Role-based manager administration.",
    health: "Service readiness, queues, and websocket state.",
    logs: "Audit and system events.",
  };
  return subtitles[screen];
}

type IconName = "dashboard" | "campaign" | "plus" | "template" | "channel" | "table" | "chart" | "users" | "pulse" | "log" | "logout" | "login" | "alert" | "retry" | "switch" | "stop" | "play" | "save" | "cancel";

function Icon({ name }: { name: IconName }) {
  const paths: Record<IconName, string> = {
    dashboard: "M4 5h7v7H4zM13 5h7v4h-7zM13 11h7v8h-7zM4 14h7v5H4z",
    campaign: "M4 6h16M6 10h12M8 14h8M10 18h4",
    plus: "M12 5v14M5 12h14",
    template: "M6 4h12v16H6zM9 8h6M9 12h6M9 16h3",
    channel: "M5 12a7 7 0 0 1 14 0M8 12a4 4 0 0 1 8 0M11 12a1 1 0 0 1 2 0M12 13v5",
    table: "M4 5h16v14H4zM4 10h16M10 5v14",
    chart: "M5 19V9M12 19V5M19 19v-7",
    users: "M8 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM3 20a5 5 0 0 1 10 0M17 11a2.5 2.5 0 1 0 0-5M15 15a4 4 0 0 1 6 4",
    pulse: "M4 12h4l2-6 4 12 2-6h4",
    log: "M6 4h12v16H6zM9 8h6M9 12h6M9 16h4",
    logout: "M10 6H6v12h4M13 9l3 3-3 3M8 12h8",
    login: "M14 6h4v12h-4M11 9l3 3-3 3M4 12h10",
    alert: "M12 4l9 16H3zM12 9v4M12 17h.01",
    retry: "M6 9a6 6 0 0 1 10-3l2 2M18 6v5h-5M18 15a6 6 0 0 1-10 3l-2-2M6 18v-5h5",
    switch: "M7 7h11l-3-3M18 7l-3 3M17 17H6l3 3M6 17l3-3",
    stop: "M6 6h12v12H6z",
    play: "M8 5v14l11-7z",
    save: "M5 4h12l2 2v14H5zM8 4v6h8M8 18h8",
    cancel: "M6 6l12 12M18 6L6 18",
  };
  return <svg className="icon" viewBox="0 0 24 24" aria-hidden="true"><path d={paths[name]} /></svg>;
}
