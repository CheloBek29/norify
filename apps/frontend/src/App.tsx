import { type CSSProperties, type Dispatch, type FormEvent, type SetStateAction, useEffect, useRef, useState } from "react";
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
  TemplateVariable,
  audiencePreview,
  campaignsSeed,
  channelsSeed,
  credentials,
  currentError,
  defaultFilter,
  deliveriesSeed,
  effectiveTotalMessages,
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
  fetchTemplateVariables,
  normalizeCampaign,
  operationsWebSocketURL,
  serviceHealthTargets,
  templateVariablesSeed,
} from "./api";

type Screen = "dashboard" | "campaigns" | "create" | "templates" | "channels" | "deliveries" | "stats" | "managers" | "health" | "logs";
type CampaignCommand = "start" | "stop" | "retry" | "switch_channel" | "cancel_campaign" | "archive";
type ThemeName = "sky" | "pink" | "mint" | "custom";

const navigation: { id: Screen; label: string; icon: IconName; adminOnly?: boolean }[] = [
  { id: "dashboard", label: "Панель", icon: "dashboard" },
  { id: "campaigns", label: "Кампании", icon: "campaign" },
  { id: "create", label: "Создать", icon: "plus" },
  { id: "templates", label: "Шаблоны", icon: "template" },
  { id: "channels", label: "Каналы", icon: "channel" },
  { id: "deliveries", label: "Доставки", icon: "table" },
  { id: "stats", label: "Статистика", icon: "chart" },
  { id: "managers", label: "Менеджеры", icon: "users", adminOnly: true },
  { id: "health", label: "Здоровье", icon: "pulse", adminOnly: true },
  { id: "logs", label: "Логи", icon: "log", adminOnly: true },
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
  const [theme, setTheme] = useLocalState<ThemeName>("norify-theme", "sky");
  const [customColor, setCustomColor] = useLocalState("norify-custom-color", "#1f95f2");
  const [screen, setScreen] = useState<Screen>("dashboard");
  const [templates, setTemplates] = useLocalState<Template[]>("norify-templates", templatesSeed);
  const [templateVariables, setTemplateVariables] = useLocalState<TemplateVariable[]>("norify-template-variables", templateVariablesSeed);
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
  const selectedCampaignIdRef = useRef(selectedCampaignId);
  const activeCampaigns = campaigns.filter((campaign) => !campaign.archivedAt);
  const selectedCampaign = activeCampaigns.find((campaign) => campaign.id === selectedCampaignId) ?? activeCampaigns[0] ?? campaigns[0];
  const activeError = selectedCampaign && selectedCampaign.failed > 0 ? buildError(selectedCampaign) : currentError;

  useEffect(() => {
    selectedCampaignIdRef.current = selectedCampaignId;
  }, [selectedCampaignId]);

  useEffect(() => {
    if (!session) return;
    void refreshBackendData();
  }, [session]);

  async function refreshBackendData() {
    const [nextCampaigns, nextTemplates, nextChannels, nextTemplateVariables] = await Promise.allSettled([
      fetchCampaigns(),
      fetchTemplates(),
      fetchChannels(),
      fetchTemplateVariables(),
    ]);
    const hasLiveData = [nextCampaigns, nextTemplates, nextChannels, nextTemplateVariables].some((result) => result.status === "fulfilled");
    let activeCampaignId = "";
    if (nextCampaigns.status === "fulfilled" && nextCampaigns.value.length > 0) {
      setCampaigns(nextCampaigns.value);
      activeCampaignId = nextCampaigns.value.find((campaign) => !campaign.archivedAt)?.id || nextCampaigns.value[0].id;
      setSelectedCampaignId((current) => current || activeCampaignId);
    }
    if (nextTemplates.status === "fulfilled" && nextTemplates.value.length > 0) setTemplates(nextTemplates.value);
    if (nextTemplateVariables.status === "fulfilled" && nextTemplateVariables.value.length > 0) setTemplateVariables(nextTemplateVariables.value);
    if (nextChannels.status === "fulfilled" && nextChannels.value.length > 0) setChannels(nextChannels.value);
    if (hasLiveData) {
      setApiState("live backend");
    } else {
      setApiState("local fallback");
    }
    const idToFetch = activeCampaignId || (nextCampaigns.status === "fulfilled" && nextCampaigns.value[0]?.id) || "";
    if (idToFetch) {
      void fetchErrorGroups(idToFetch).then((groups) => {
        if (groups.length > 0) setErrorGroups((prev) => [...groups, ...prev.filter((g) => g.campaignId !== idToFetch)]);
      }).catch(() => undefined);
    }
  }

  useEffect(() => {
    if (!session) return;
    let stopped = false;

    function connect() {
      if (stopped) return;
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
        } catch (err) {
          console.error("ops ws parse error:", err);
        }
      };
      socket.onclose = () => {
        if (opsSocketRef.current === socket) opsSocketRef.current = null;
        setApiState((current) => current === "websocket commands" ? "websocket reconnecting" : current);
        if (!stopped) window.setTimeout(connect, 2000);
      };
    }

    connect();
    return () => {
      stopped = true;
      const socket = opsSocketRef.current;
      if (socket) {
        socket.close();
        opsSocketRef.current = null;
      }
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
          if (snapshot.campaign_id === selectedCampaignIdRef.current) {
            void fetchErrorGroups(snapshot.campaign_id).then((groups) => {
              if (!closed && groups.length > 0) setErrorGroups((prev) => [
                ...groups,
                ...prev.filter((g) => g.campaignId !== snapshot.campaign_id),
              ]);
            }).catch(() => undefined);
            void fetchDeliveries(snapshot.campaign_id).then((rows) => {
              if (!closed && rows.length > 0) setDeliveries(rows);
            }).catch(() => undefined);
          }
        } catch (err) {
          console.error("campaign ws parse error:", err);
        }
      };
      socket.onopen = () => setApiState("websocket live");
      return socket;
    });
    return () => {
      closed = true;
      sockets.forEach((socket) => socket.close());
    };
  }, [session, campaigns.map((campaign) => campaign.id).sort().join("|")]);

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
    return <Login theme={theme} customColor={customColor} onThemeChange={setTheme} onCustomColorChange={setCustomColor} onLogin={setSession} />;
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
      setCampaigns((items) => upsertCampaign(items, campaign));
      setSelectedCampaignId((current) => current || campaign.id);
    }
    if (message.type === "error_group.resolved") {
      const groupID = String(message.group_id ?? "");
      if (message.campaign) {
        const campaign = normalizeCampaign(message.campaign as Record<string, unknown>);
        setCampaigns((items) => items.map((item) => item.id === campaign.id ? mergeCampaignUpdate(item, campaign) : item));
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
      if (frozenStatus && staleActiveSnapshot) {
        return {
          ...campaign,
          p95DispatchMs: Number(snapshot.p95_dispatch_ms ?? snapshot.p95DispatchMs ?? campaign.p95DispatchMs),
        };
      }
      return {
        ...campaign,
        status: nextStatus,
        totalMessages: snapshotLooksEmpty(snapshot, campaign) ? campaign.totalMessages : Number(snapshot.total_messages ?? campaign.totalMessages),
        processed: snapshotLooksEmpty(snapshot, campaign) ? campaign.processed : Number(snapshot.processed ?? campaign.processed),
        success: snapshotLooksEmpty(snapshot, campaign) ? campaign.success : Number(snapshot.success ?? campaign.success),
        failed: snapshotLooksEmpty(snapshot, campaign) ? campaign.failed : Number(snapshot.failed ?? campaign.failed),
        cancelled: snapshotLooksEmpty(snapshot, campaign) ? campaign.cancelled : Number(snapshot.cancelled ?? campaign.cancelled),
        p95DispatchMs: Number(snapshot.p95_dispatch_ms ?? snapshot.p95DispatchMs ?? campaign.p95DispatchMs),
      };
    }));
  }

  async function createCampaign(wizard: WizardState) {
    const template = templates.find((item) => item.id === wizard.templateId) ?? templates[0];
    const totalRecipients = audiencePreview(wizard.filter);
    const totalMessages = totalRecipients * wizard.selectedChannels.length;
    const now = new Date().toISOString();
    const optimisticCampaign: Campaign = {
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
      p95DispatchMs: 0,
      createdAt: now,
      startedAt: now,
    };
    setCampaigns((items) => [optimisticCampaign, ...items]);
    setSelectedCampaignId(optimisticCampaign.id);
    setScreen("dashboard");
    addEvent("info", "campaign.started", `Campaign ${optimisticCampaign.name} queued with ${totalMessages.toLocaleString()} messages`, "campaign-service");

    try {
      await sendOpsCommand("template.save", { template }).catch(() => undefined);
      const result = await sendOpsCommand("campaign.create", {
        name: wizard.name,
        template_id: template.id,
        filters: wizard.filter,
        selected_channels: wizard.selectedChannels,
        total_recipients: totalRecipients,
      });
      const backendCampaign = normalizeCampaign((result.campaign ?? result) as Record<string, unknown>);
      setCampaigns((items) => [backendCampaign, ...items.filter((item) => item.id !== backendCampaign.id && item.id !== optimisticCampaign.id)]);
      setSelectedCampaignId(backendCampaign.id);
      setApiState("websocket commands");
      return;
    } catch {
      setApiState("local fallback");
    }
    const fallbackCampaign = { ...optimisticCampaign, p95DispatchMs: Math.min(1320, 720 + wizard.selectedChannels.length * 54) };
    setCampaigns((items) => items.map((campaign) => campaign.id === optimisticCampaign.id ? fallbackCampaign : campaign));
    setDeliveries((items) => [...createDeliveries(fallbackCampaign), ...items]);
    setErrorGroups((items) => [...buildLocalErrorGroups(fallbackCampaign), ...items]);
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
        setCampaigns((items) => items.map((campaign) => campaign.id === updated.id ? mergeCampaignUpdate(campaign, updated) : campaign));
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
        if (action === "archive") return { ...campaign, archivedAt: new Date().toISOString() };
        const cancelled = Math.max(0, campaign.totalMessages - campaign.processed);
        return { ...campaign, status: "cancelled", cancelled, processed: campaign.totalMessages, finishedAt: new Date().toISOString() };
      }),
    );
    if (action === "archive" && selectedCampaign?.id === targetCampaign.id) {
      const nextActive = campaigns.find((campaign) => campaign.id !== targetCampaign.id && !campaign.archivedAt);
      if (nextActive) setSelectedCampaignId(nextActive.id);
    }
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
    <main className="appShell" data-theme={theme} data-testid="theme-root" style={themeStyle(theme, customColor)}>
      <aside className="sidebar">
        <div className="brand">
          <span className="brandMark">N</span>
          <span><strong>Norify</strong><small>Платформа уведомлений</small></span>
        </div>
        <nav className="navList">
          {visibleNavigation.map((item) => (
            <button key={item.id} className={screen === item.id ? "navItem active" : "navItem"} onClick={() => setScreen(item.id)}>
              <Icon name={item.icon} /> <span>{item.label}</span>
            </button>
          ))}
        </nav>
        <div className="sessionBox">
          <small>Сессия</small>
          <span>{userSession.email}</span>
          <strong>{userSession.role}</strong>
          <ThemePicker value={theme} customColor={customColor} onChange={setTheme} onCustomColorChange={setCustomColor} compact />
          <button onClick={() => setSession(null)}><Icon name="logout" /> Выйти</button>
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
            <select value={selectedCampaign && !selectedCampaign.archivedAt ? selectedCampaign.id : ""} onChange={(event) => setSelectedCampaignId(event.target.value)} disabled={activeCampaigns.length === 0}>
              {activeCampaigns.map((campaign) => <option key={campaign.id} value={campaign.id}>{campaign.name}</option>)}
            </select>
            <button className="primary" onClick={() => setScreen("create")}><Icon name="plus" /> Новая кампания</button>
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
        {screen === "templates" && <Templates templates={templates} variableOptions={templateVariables} onSave={updateTemplate} />}
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

function upsertCampaign(items: Campaign[], incoming: Campaign) {
  const current = items.find((item) => item.id === incoming.id);
  const campaign = current ? mergeCampaignUpdate(current, incoming) : incoming;
  return [campaign, ...items.filter((item) => item.id !== incoming.id)];
}

function mergeCampaignUpdate(current: Campaign, incoming: Campaign): Campaign {
  const emptyProgress = current.totalMessages > 0
    && incoming.totalMessages <= 0
    && incoming.processed <= 0
    && incoming.success <= 0
    && incoming.failed <= 0
    && incoming.cancelled <= 0;
  if (!emptyProgress) return incoming;
  return {
    ...incoming,
    totalMessages: current.totalMessages,
    processed: current.processed,
    success: current.success,
    failed: current.failed,
    cancelled: current.cancelled,
    p95DispatchMs: incoming.p95DispatchMs || current.p95DispatchMs,
  };
}

function snapshotLooksEmpty(snapshot: Record<string, unknown>, current: Campaign) {
  const hasProgressFields = ["total_messages", "totalMessages", "processed", "success", "failed", "cancelled"].some((field) => field in snapshot);
  if (!hasProgressFields || current.totalMessages <= 0) return false;
  return Number(snapshot.total_messages ?? snapshot.totalMessages ?? 0) <= 0
    && Number(snapshot.processed ?? 0) <= 0
    && Number(snapshot.success ?? 0) <= 0
    && Number(snapshot.failed ?? 0) <= 0
    && Number(snapshot.cancelled ?? 0) <= 0;
}

function Login({
  theme,
  customColor,
  onThemeChange,
  onCustomColorChange,
  onLogin,
}: {
  theme: ThemeName;
  customColor: string;
  onThemeChange: (theme: ThemeName) => void;
  onCustomColorChange: (color: string) => void;
  onLogin: (session: { email: string; role: Role }) => void;
}) {
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
    <main className="loginShell" data-theme={theme} data-testid="theme-root" style={themeStyle(theme, customColor)}>
      <header className="loginWelcome">
        <h1><span>Добро пожаловать в</span> <strong>Norify</strong></h1>
      </header>
      <div className="loginStage">
      <section className="loginPanel formPanel" aria-label="Вход в личный кабинет">
        <div className="brand loginBrand"><span className="brandMark">N</span><span><strong>Norify</strong><small>Платформа уведомлений</small></span></div>
        <form onSubmit={submit} className="loginForm">
          <h2>Вход в личный кабинет</h2>
          <label>Email<input placeholder="name@company.com" value={email} onChange={(event) => setEmail(event.target.value)} /></label>
          <label>
            <span className="labelRow"><span>Пароль</span><button className="linkButton" type="button">Забыли пароль?</button></span>
            <input type="password" value={password} onChange={(event) => setPassword(event.target.value)} />
          </label>
          {error && <div className="inlineError">{error}</div>}
          <button className="primary loginAction">Продолжить</button>
          <button type="button" className="softButton loginAction">Продолжить с Google</button>
        </form>
        <div className="loginHints">
          <button type="button" onClick={() => { setEmail(credentials.admin.email); setPassword(credentials.admin.password); }}>admin@example.com</button>
          <button type="button" onClick={() => { setEmail(credentials.manager.email); setPassword(credentials.manager.password); }}>manager@example.com</button>
        </div>
      </section>
      <ThemePicker value={theme} customColor={customColor} onChange={onThemeChange} onCustomColorChange={onCustomColorChange} />
      </div>
    </main>
  );
}

const themeOptions: { name: ThemeName; label: string }[] = [
  { name: "sky", label: "Голубая тема" },
  { name: "pink", label: "Розовая тема" },
  { name: "mint", label: "Зеленая тема" },
];

function ThemePicker({
  value,
  customColor,
  onChange,
  onCustomColorChange,
  compact = false,
}: {
  value: ThemeName;
  customColor: string;
  onChange: (theme: ThemeName) => void;
  onCustomColorChange: (color: string) => void;
  compact?: boolean;
}) {
  return (
    <div className={compact ? "themePicker compact" : "themePicker"} aria-label="Выбор темы">
      {themeOptions.map((item) => (
        <button
          key={item.name}
          type="button"
          className={`themeSwatch theme-${item.name}${value === item.name ? " active" : ""}`}
          aria-label={item.label}
          aria-pressed={value === item.name}
          onClick={() => onChange(item.name)}
        />
      ))}
      <label className={`themeSwatch customColorSwatch${value === "custom" ? " active" : ""}`}>
        <input
          type="color"
          value={customColor}
          aria-label="Пользовательский цвет"
          onChange={(event) => {
            onCustomColorChange(event.target.value);
            onChange("custom");
          }}
        />
      </label>
    </div>
  );
}

function themeStyle(theme: ThemeName, customColor: string): CSSProperties | undefined {
  if (theme !== "custom") return undefined;
  const rgb = hexToRgb(customColor);
  if (!rgb) return undefined;
  const { r, g, b } = rgb;
  return {
    "--accent": customColor,
    "--accent-strong": customColor,
    "--accent-soft": `rgba(${r}, ${g}, ${b}, 0.62)`,
    "--accent-faint": `rgba(${r}, ${g}, ${b}, 0.24)`,
    "--shadow": `0 24px 70px rgba(${r}, ${g}, ${b}, 0.16)`,
    "--shadow-soft": `0 10px 30px rgba(${r}, ${g}, ${b}, 0.1)`,
    "--page-gradient": `linear-gradient(180deg, rgba(${r}, ${g}, ${b}, 0.34) 0%, rgba(${r}, ${g}, ${b}, 0.12) 58%, #ffffff 100%)`,
  } as CSSProperties;
}

function hexToRgb(value: string) {
  const match = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(value);
  if (!match) return null;
  return {
    r: Number.parseInt(match[1], 16),
    g: Number.parseInt(match[2], 16),
    b: Number.parseInt(match[3], 16),
  };
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
  const totalMessages = effectiveTotalMessages(campaign);
  const pending = Math.max(0, totalMessages - campaign.processed);
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
              <button onClick={() => onAction("retry")}><Icon name="retry" /> Повторить ошибки</button>
              <button onClick={() => onAction("switch_channel")}><Icon name="switch" /> Сменить канал</button>
            </div>
          )}
        </div>

        <div className="progressStack">
          <div className="progressTop">
            <strong>{percent}%</strong>
            <span>{campaign.processed.toLocaleString()} / {totalMessages.toLocaleString()}</span>
          </div>
          <div className="progressLine"><div style={{ width: `${percent}%` }} /></div>
        </div>

        <div className="campaignMetaGrid">
          <Metric label="В очереди" value={pending.toLocaleString()} />
          <Metric label="Успешно" value={campaign.success.toLocaleString()} tone="success" />
          <Metric label="Ошибки" value={campaign.failed.toLocaleString()} tone="danger" />
          <Metric label="Получатели" value={campaign.totalRecipients.toLocaleString()} />
          <Metric label="Каналы" value={String(campaign.selectedChannels.length)} />
          <Metric label="p95 enqueue" value={formatP95Dispatch(campaign)} />
        </div>
      </section>

      <section className="panel">
        <div className="panelHeader"><h2>Линии отправки</h2><span>{campaign.selectedChannels.length} активны</span></div>
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
        <span className="queueMode"><Icon name="alert" /> живая очередь</span>
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
        <span>попытка {group.maxAttempt}</span>
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

function CampaignList({ campaigns, selectedId, onSelect, onAction }: { campaigns: Campaign[]; selectedId?: string; onSelect: (id: string) => void; onAction: (campaignId: string, action: "start" | "retry" | "cancel_campaign" | "archive") => void }) {
  const [showArchive, setShowArchive] = useState(false);
  const activeCampaigns = campaigns.filter((campaign) => !campaign.archivedAt);
  const archivedCampaigns = campaigns.filter((campaign) => campaign.archivedAt);
  const visibleCampaigns = showArchive ? archivedCampaigns : activeCampaigns;
  return (
    <section className="panel wide campaignListPanel">
      <div className="panelHeader">
        <h2>Очередь кампаний</h2>
        <div className="buttonRow tight">
          <span>{activeCampaigns.length} активных / {archivedCampaigns.length} архив</span>
          <button className={!showArchive ? "softButton" : ""} onClick={() => setShowArchive(false)}>Активные</button>
          <button className={showArchive ? "softButton" : ""} onClick={() => setShowArchive(true)}>Показать архив</button>
        </div>
      </div>
      <div className="tableWrap">
        <table>
          <thead><tr><th>Название</th><th>Статус</th><th>Прогресс</th><th>Аудитория</th><th>Сообщения</th><th>p95 enqueue</th><th /></tr></thead>
          <tbody>
            {visibleCampaigns.map((campaign) => (
              <tr key={campaign.id} className={selectedId === campaign.id ? "selectedRow" : ""}>
                <td><button className="linkButton" onClick={() => onSelect(campaign.id)}>{campaign.name}</button></td>
                <td><span className={`status status-${campaign.archivedAt ? "archived" : campaign.status}`}>{campaign.archivedAt ? "архив" : campaign.status}</span></td>
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
                  {!campaign.archivedAt && campaign.status === "created" && <button onClick={() => onAction(campaign.id, "start")}><Icon name="play" /> Старт</button>}
                  {!campaign.archivedAt && campaign.failed > 0 && <button onClick={() => onAction(campaign.id, "retry")}><Icon name="retry" /> Повторить</button>}
                  {!campaign.archivedAt && (campaign.status === "running" || campaign.status === "retrying") && <button className="danger" onClick={() => onAction(campaign.id, "cancel_campaign")}><Icon name="stop" /> Отменить</button>}
                  {!campaign.archivedAt && campaign.status !== "created" && <button onClick={() => onAction(campaign.id, "archive")}><Icon name="archive" /> В архив</button>}
                </td>
              </tr>
            ))}
            {visibleCampaigns.length === 0 && (
              <tr><td colSpan={7}><div className="emptyState"><strong>{showArchive ? "Архив пуст" : "Нет активных кампаний"}</strong><span>Кампании из архива не удаляются физически и остаются доступны для просмотра.</span></div></td></tr>
            )}
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
      <section className="panel formPanel">
        <div className="panelHeader"><h2>Кампания</h2><Icon name="campaign" /></div>
        <div className="formStack">
          <label>Название<input value={wizard.name} onChange={(event) => setWizard({ ...wizard, name: event.target.value })} /></label>
          <label>Шаблон
            <select value={wizard.templateId} onChange={(event) => setWizard({ ...wizard, templateId: event.target.value })}>
              {templates.map((template) => <option key={template.id} value={template.id}>{template.name} · v{template.version}</option>)}
            </select>
          </label>
          <div className="templatePreview">{templates.find((template) => template.id === wizard.templateId)?.body}</div>
        </div>
      </section>

      <section className="panel formPanel">
        <div className="panelHeader"><h2>Аудитория</h2><strong>{preview.toLocaleString()}</strong></div>
        <div className="formStack">
          <div className="fieldGrid">
            <label>Возраст от<input type="number" value={wizard.filter.minAge} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, minAge: Number(event.target.value) } })} /></label>
            <label>Возраст до<input type="number" value={wizard.filter.maxAge} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, maxAge: Number(event.target.value) } })} /></label>
          </div>
          <label>Пол
            <select value={wizard.filter.gender} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, gender: event.target.value as AudienceFilter["gender"] } })}>
              <option value="any">любой</option><option value="female">женский</option><option value="male">мужской</option>
            </select>
          </label>
          <label>Город
            <select value={wizard.filter.location} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, location: event.target.value } })}>
              <option value="all">все</option><option value="Moscow">Москва</option><option value="Kazan">Казань</option><option value="Saint Petersburg">Санкт-Петербург</option>
            </select>
          </label>
          <label>Теги<input value={wizard.filter.tags.join(", ")} onChange={(event) => setWizard({ ...wizard, filter: { ...wizard.filter, tags: event.target.value.split(",").map((tag) => tag.trim()).filter(Boolean) } })} /></label>
        </div>
      </section>

      <section className="panel formPanel">
        <div className="panelHeader"><h2>Каналы</h2><strong>{totalMessages.toLocaleString()}</strong></div>
        <div className="choiceList">
          {enabledChannels.map((channel) => (
            <button key={channel.code} className={wizard.selectedChannels.includes(channel.code) ? "choice selected" : "choice"} onClick={() => toggleChannel(channel.code)}>
              <span><Icon name="channel" /> {channel.name}</span>
              <small>{Math.round(channel.successProbability * 100)}%</small>
            </button>
          ))}
        </div>
        <div className="formActions"><button className="primary startButton" disabled={!canStart} onClick={() => onCreate(wizard)}><Icon name="play" /> Запустить кампанию</button></div>
      </section>
    </div>
  );
}

const TEMPLATE_GENERATOR_URL = "http://localhost:8091";

type AIStyle = "professional" | "creative" | "luxury" | "minimal" | "ecommerce";

function AIGenerator({ onApply }: { onApply: (text: string) => void }) {
  const [step, setStep] = useState<1 | 2>(1);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [taskDesc, setTaskDesc] = useState("Создай рассылку ");
  const [style, setStyle] = useState<AIStyle>("professional");
  const [generatedText, setGeneratedText] = useState("");
  const [editMode, setEditMode] = useState(false);

  function mdToHtml(md: string): string {
    const lines = md.split("\n");
    const out: string[] = [];
    let inList = false;

    for (const raw of lines) {
      // убираем Subject line
      if (/^(\*\*)?subject/i.test(raw.trim())) continue;

      let line = raw
        .replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>")
        .replace(/\*(.+?)\*/g, "<em>$1</em>")
        .replace(/\[(.+?)\]\((.+?)\)/g, '<a href="$2">$1</a>')
        // {var} и {{var}} → красивый чип с подстановкой примера
        .replace(/\{\{?([a-zA-Z_][a-zA-Z0-9_]*)\}?\}/g, (_, v) => {
          const sample = templateSampleValues[v];
          return sample
            ? `<span class="aiVarChip" title="{{${v}}}">${sample}</span>`
            : `<span class="aiVarChip unknown" title="{{${v}}}">${v}</span>`;
        });

      // headings
      const hMatch = line.match(/^#{1,3}\s+(.+)/);
      if (hMatch) {
        if (inList) { out.push("</ul>"); inList = false; }
        out.push(`<h3>${hMatch[1]}</h3>`);
        continue;
      }

      // subject line
      if (line.toLowerCase().startsWith("<strong>subject") || line.toLowerCase().startsWith("subject")) {
        if (inList) { out.push("</ul>"); inList = false; }
        out.push(`<p class="aiSubject">${line}</p>`);
        continue;
      }

      // blank line
      if (line.trim() === "") {
        if (inList) { out.push("</ul>"); inList = false; }
        continue;
      }

      // bullet/emoji list items: lines starting with -, *, •, or emoji+space
      const listMatch = line.match(/^[-*•]\s+(.+)/) || line.match(/^([✅✔️▶️➡️🔹🔸💡📌⭐🌟])\s+(.+)/);
      if (listMatch) {
        if (!inList) { out.push("<ul>"); inList = true; }
        const content = listMatch[2] ?? listMatch[1];
        const prefix = listMatch[2] ? listMatch[1] + " " : "";
        out.push(`<li>${prefix}${content}</li>`);
        continue;
      }

      if (inList) { out.push("</ul>"); inList = false; }
      out.push(`<p>${line}</p>`);
    }

    if (inList) out.push("</ul>");
    return out.join("\n");
  }

  async function handleGenerateText() {
    setError(null);
    setLoading(true);
    try {
      const res = await fetch(`${TEMPLATE_GENERATOR_URL}/api/generate-text`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ task_description: taskDesc, style }),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.detail || "Generation failed");
      setGeneratedText(data.text);
      setEditMode(false);
      setStep(2);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Unknown error");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="aiGenerator">
      <div className="aiSteps">
        {([1, 2] as const).map((s) => (
          <div key={s} className={`aiStep${step === s ? " active" : step > s ? " done" : ""}`}>
            <span>{s}</span>
            <p>{s === 1 ? "Описание" : "Результат"}</p>
          </div>
        ))}
      </div>

      {error && <div className="aiError">{error}</div>}

      {step === 1 && (
        <div className="aiPanel">
          <h3>Опишите задачу для рассылки</h3>
          <label>Описание
            <textarea value={taskDesc} onChange={(e) => setTaskDesc(e.target.value)} rows={5}
              placeholder="Пример: Создай рассылку для интернет-магазина. Скидка 30% на смартфоны. Аудитория: 18-35 лет." />
          </label>
          <label>Стиль
            <select value={style} onChange={(e) => setStyle(e.target.value as AIStyle)}>
              <option value="professional">Профессиональный</option>
              <option value="creative">Креативный</option>
              <option value="luxury">Люкс</option>
              <option value="minimal">Минимализм</option>
              <option value="ecommerce">E-commerce</option>
            </select>
          </label>
          <button className="primary" disabled={taskDesc.length < 10 || loading} onClick={handleGenerateText}>
            {loading ? "Генерирую..." : "Сгенерировать текст →"}
          </button>
        </div>
      )}

      {step === 2 && (
        <div className="aiPanel">
          <div className="aiTextHeader">
            <h3>Готовый текст рассылки</h3>
            <button className="aiEditToggle" onClick={() => setEditMode(!editMode)}>
              {editMode ? "Предпросмотр" : "Редактировать"}
            </button>
          </div>
          {editMode ? (
            <textarea className="aiTextarea" value={generatedText} onChange={(e) => setGeneratedText(e.target.value)} rows={14} />
          ) : (
            <div className="aiTextPreview" dangerouslySetInnerHTML={{ __html: mdToHtml(generatedText) }} />
          )}
          <div className="aiActions">
            <button onClick={() => { setStep(1); setGeneratedText(""); }}>← Назад</button>
            <button onClick={() => handleGenerateText()} disabled={loading}>{loading ? "Генерирую..." : "Перегенерировать"}</button>
            <button className="primary" onClick={() => {
              // {var} → {{var}} для шаблонизатора
              const converted = generatedText.replace(/\{([a-zA-Z_][a-zA-Z0-9_]*)\}/g, "{{$1}}");
              onApply(converted);
            }}>Вставить в шаблон →</button>
          </div>
        </div>
      )}
    </div>
  );
}

function Templates({ templates, variableOptions, onSave }: { templates: Template[]; variableOptions: TemplateVariable[]; onSave: (template: Template) => void }) {
  const initialTemplate = templates[0] ?? createBlankTemplate();
  const [editing, setEditing] = useState<Template>(initialTemplate);
  const [query, setQuery] = useState("");
  const [activeTab, setActiveTab] = useState<"library" | "ai">("library");
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
      variables: normalizeVariables([...current.variables, variable]),
      body: current.body.includes(token) ? current.body : `${current.body.trimEnd()} ${token}`.trim(),
    }));
  }

  function saveTemplate() {
    if (!isValid) return;
    onSave({ ...editing, variables: declaredVariables, version: editing.version + 1, updatedAt: new Date().toISOString() });
  }

  return (
    <div>
      <div className="templatesTabs">
        <button className={activeTab === "library" ? "tabBtn active" : "tabBtn"} onClick={() => setActiveTab("library")}>Библиотека</button>
        <button className={activeTab === "ai" ? "tabBtn active" : "tabBtn"} onClick={() => setActiveTab("ai")}>AI Генератор</button>
      </div>
      {activeTab === "ai" && <AIGenerator onApply={(text) => {
        const vars = [...new Set([...text.matchAll(/\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}/g)].map(m => m[1]))];
        const tpl = { ...createBlankTemplate(), body: text, variables: vars };
        setEditing(tpl);
        onSave({ ...tpl, version: 1, updatedAt: new Date().toISOString() });
        setActiveTab("library");
      }} />}
      {activeTab === "library" && <div className="templatesLayout">
      <section className="panel formPanel templateLibraryPanel">
        <div className="panelHeader">
          <h2>Библиотека шаблонов</h2>
          <span>{templates.length} всего</span>
        </div>
        <label>Поиск<input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="название, текст, переменная" /></label>
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

      <section className="panel formPanel templateComposerPanel">
        <div className="panelHeader">
          <h2>Редактор шаблона</h2>
          <button onClick={() => setEditing(createBlankTemplate())}><Icon name="plus" /> Новый</button>
        </div>
        <div className="fieldGrid">
          <label>Название<input value={editing.name} onChange={(event) => setEditing({ ...editing, name: event.target.value })} /></label>
          <label>Версия<input readOnly value={`v${editing.version}`} /></label>
        </div>
        <label>Текст сообщения<textarea className="templateBodyInput" value={editing.body} onChange={(event) => setEditing({ ...editing, body: event.target.value })} /></label>

        <div className="templateVariableGrid">
          <div>
            <div className="templateSectionLabel">Найдены в тексте</div>
            <div className="variableChips">
              {detectedVariables.length > 0 ? detectedVariables.map((variable) => <span key={variable} className="variableChip detected">{variable}</span>) : <span className="variableChip mutedChip">none</span>}
            </div>
          </div>
          <div>
            <label className="compactLabel">Объявленные переменные
              <input value={declaredVariables.join(", ")} onChange={(event) => updateVariables(event.target.value)} />
            </label>
          </div>
        </div>

        <div>
          <div className="templateSectionLabel">Колонки PostgreSQL</div>
          <div className="variableChips postgresColumns">
            {variableOptions.map((variable) => (
              <button key={`${variable.source}.${variable.name}`} className="variableChip actionChip" title={`${variable.source}.${variable.name}: ${variable.type}`} onClick={() => insertVariable(variable.name)}>
                {variable.name}
              </button>
            ))}
          </div>
        </div>

        {declaredVariables.length > 0 && (
          <div>
            <div className="templateSectionLabel">Быстрая вставка</div>
            <div className="variableChips">
              {declaredVariables.map((variable) => (
                <button key={variable} className="variableChip actionChip" onClick={() => insertVariable(variable)}>{variable}</button>
              ))}
            </div>
          </div>
        )}

        <div className={isValid ? "templateValidation valid" : "templateValidation invalid"}>
          {isValid ? "Готово к сохранению" : missingVariables.length > 0 ? `Не объявлены: ${missingVariables.join(", ")}` : "Заполните название и текст"}
          {unusedVariables.length > 0 && <span>Не используются: {unusedVariables.join(", ")}</span>}
        </div>

        <button className="primary saveTemplateButton" disabled={!isValid} onClick={saveTemplate}><Icon name="save" /> Сохранить версию</button>
      </section>

      <section className="panel templatePreviewPanel">
        <div className="panelHeader">
          <h2>Живой предпросмотр</h2>
          <span>{editing.body.length} символов</span>
        </div>
        <div className="messagePreview">
          <div className="messageBubble">
            {preview
              ? preview.split("\n").filter(Boolean).map((line, i) => (
                  <p key={i} style={{ margin: "0 0 6px" }}>{line}</p>
                ))
              : <p style={{ margin: 0, opacity: 0.5 }}>Введите текст...</p>}
          </div>
        </div>
        <div className="templateMetaGrid">
          <Metric label="переменные" value={String(declaredVariables.length)} />
          <Metric label="найдены" value={String(detectedVariables.length)} />
          <Metric label="версия" value={`v${editing.version}`} />
        </div>
        <div className="previewSamples">
          {Object.entries(templateSampleValues).slice(0, 4).map(([key, value]) => (
            <div key={key}><span>{key}</span><strong>{value}</strong></div>
          ))}
        </div>
      </section>
    </div>}
    </div>
  );
}

const templateVariablePattern = /\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g;
const templateSampleValues: Record<string, string> = {
  first_name: "Анна",
  order_id: "48291",
  promo_code: "SPRING20",
  city: "Москва",
  email: "user@example.com",
  phone: "+79990000001",
  telegram_id: "tg1001",
  vk_id: "vk1001",
  custom_app_id: "app1001",
  age: "34",
  gender: "female",
  location: "Moscow",
  tags: "vip, retail",
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
    <section className="panel wide channelRegistryPanel">
      <div className="panelHeader">
        <h2>Реестр каналов</h2>
        <span>{role === "admin" ? "Живая статистика доставки" : "Только чтение"}</span>
      </div>
      <div className="channelGrid">
        {channels.map((channel) => (
          <article key={channel.code} className={`channelCard${channel.degraded ? " degraded" : ""}${channel.enabled ? "" : " disabled"}`}>
            <div className="channelTop">
              <div className="channelName">
                <strong>{channel.name}</strong>
                <small>{channel.code}</small>
              </div>
              <span className={channel.enabled ? "status status-running" : "status status-cancelled"}>{channel.enabled ? "Включен" : "Отключен"}</span>
            </div>
            <div className="channelMetrics">
              <Metric label="Успех доставки" value={formatChannelSuccess(channel)} />
              <Metric label="Параллелизм" value={String(channel.maxParallelism)} />
              <Metric label="Средняя попытка" value={formatAverageAttempt(channel)} />
              <Metric label="Порог успеха" value={formatProbability(channel.successProbability)} />
            </div>
            <div className="channelStatsMeta">
              <span><small>Всего</small><strong>{(channel.deliveryTotal ?? 0).toLocaleString()}</strong></span>
              <span><small>Отправлено</small><strong>{(channel.deliverySent ?? 0).toLocaleString()}</strong></span>
              <span><small>Ошибки</small><strong>{(channel.deliveryFailed ?? 0).toLocaleString()}</strong></span>
              <span><small>В очереди</small><strong>{(channel.deliveryQueued ?? 0).toLocaleString()}</strong></span>
            </div>
            <label className="channelProbability">
              <span><b>Настроенная вероятность</b><strong>{formatProbability(channel.successProbability)}</strong></span>
              <input disabled={role !== "admin"} type="range" min="0.5" max="1" step="0.01" value={channel.successProbability} onChange={(event) => onUpdate(channel.code, { successProbability: Number(event.target.value) })} />
            </label>
            <div className="buttonRow">
              <button disabled={role !== "admin"} onClick={() => onUpdate(channel.code, { enabled: !channel.enabled })}>{channel.enabled ? <Icon name="stop" /> : <Icon name="play" />} {channel.enabled ? "Отключить" : "Включить"}</button>
              <button disabled={role !== "admin"} onClick={() => onUpdate(channel.code, { degraded: !channel.degraded })}><Icon name="pulse" /> {channel.degraded ? "Стабилизировать" : "Снизить"}</button>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

function formatChannelSuccess(channel: Channel) {
  if (!channel.deliveryTotal || channel.deliverySuccessRate === null || channel.deliverySuccessRate === undefined) return "Нет данных";
  return `${Math.round(channel.deliverySuccessRate * 100)}%`;
}

function formatAverageAttempt(channel: Channel) {
  if (!channel.deliveryTotal || channel.averageAttempt === null || channel.averageAttempt === undefined) return "Нет данных";
  return channel.averageAttempt.toFixed(channel.averageAttempt >= 10 ? 0 : 1);
}

function formatProbability(value: number) {
  return `${Math.round(value * 100)}%`;
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
      <div className="panelHeader"><h2>Результаты доставки</h2><span>{deliveries.length} строк</span></div>
      <div className="tableWrap">
        <table>
          <thead><tr><th>Пользователь</th><th>Канал</th><th>Статус</th><th>Попытка</th><th>Ошибка</th><th>Завершено</th></tr></thead>
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
          <Metric label="всего сообщений" value={totals.messages.toLocaleString()} />
          <Metric label="успешно" value={totals.success.toLocaleString()} tone="success" />
          <Metric label="ошибки" value={totals.failed.toLocaleString()} tone="danger" />
          <Metric label="активные" value={String(totals.active)} />
        </div>
      </section>
      <section className="panel wide">
        <div className="panelHeader"><h2>Качество каналов</h2><span>{channels.filter((channel) => channel.enabled).length} включены</span></div>
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
      <section className="panel formPanel">
        <div className="panelHeader"><h2>Добавить менеджера</h2><Icon name="users" /></div>
        <div className="formStack">
          <label>Email<input value={email} onChange={(event) => setEmail(event.target.value)} /></label>
          <label>Роль<select value={newRole} onChange={(event) => setNewRole(event.target.value as Role)}><option value="manager">manager</option><option value="admin">admin</option></select></label>
        </div>
        <div className="formActions"><button className="primary" onClick={() => onAdd(email, newRole)}><Icon name="plus" /> Добавить</button></div>
      </section>
      <section className="panel">
        <div className="panelHeader"><h2>RBAC</h2><span>{managers.length} аккаунтов</span></div>
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
          <h2>Готовность микросервисов</h2>
          <div className="buttonRow tight">
            <span className="healthSummary">{readyCount} ready · {downCount} down</span>
            <button onClick={() => void refreshHealth()} disabled={refreshing}><Icon name="pulse" /> Обновить</button>
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
            <thead><tr><th>Сервис</th><th>Статус</th><th>Задержка</th><th>Endpoint</th><th>Проверка</th></tr></thead>
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
        <div className="panelHeader"><h2>Последние события</h2><span>{events.length}</span></div>
        <EventList events={events.slice(0, 5)} />
      </section>
    </div>
  );
}

function Logs({ events }: { events: SystemEvent[] }) {
  return <section className="panel wide"><div className="panelHeader"><h2>Системные события</h2><span>структурные логи</span></div><EventList events={events} /></section>;
}

function EventList({ events }: { events: SystemEvent[] }) {
  return <div className="eventList">{events.map((event) => <div key={event.id} className={`event event-${event.level}`}><span>{event.level}</span><strong>{event.type}</strong><p>{event.message}</p><small>{event.service} · {formatDate(event.createdAt)}</small></div>)}</div>;
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: "success" | "danger" }) {
  return <div className={`metric ${tone ?? ""}${value === "Нет данных" ? " emptyMetric" : ""}`}><span>{label}</span><strong>{value}</strong></div>;
}

function AccessDenied() {
  return <section className="panel"><div className="panelHeader"><h2>Нет доступа</h2><Icon name="alert" /></div><p>Недостаточно прав для административной операции.</p></section>;
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
  if (campaign.p95DispatchMs === 1) return "<1 ms";
  if (campaign.p95DispatchMs > 0) return `${campaign.p95DispatchMs} ms`;
  if (campaign.status === "created") return "pending";
  if (campaign.status === "running" || campaign.status === "retrying") return "measuring";
  return "no samples";
}

function titleFor(screen: Screen) {
  const titles: Record<Screen, string> = {
    dashboard: "Панель управления",
    campaigns: "Кампании",
    create: "Создание кампании",
    templates: "Шаблоны",
    channels: "Каналы",
    deliveries: "Доставки",
    stats: "Статистика",
    managers: "Менеджеры",
    health: "Здоровье",
    logs: "Системные логи",
  };
  return titles[screen];
}

function subtitleFor(screen: Screen) {
  const subtitles: Record<Screen, string> = {
    dashboard: "Живая отправка, статусы доставки и точечная обработка ошибок.",
    campaigns: "Очередь, запуск, повтор и контроль выполнения кампаний.",
    create: "Аудитория, шаблон и каналы в одном потоке.",
    templates: "Версионные тексты уведомлений с проверкой переменных.",
    channels: "Реестр адаптеров, лимиты, вероятности и доступность.",
    deliveries: "Результаты доставки по пользователям и каналам.",
    stats: "Сводная пропускная способность и качество каналов.",
    managers: "Управление менеджерами и ролями.",
    health: "Готовность сервисов, очереди и websocket-состояние.",
    logs: "Аудит и системные события.",
  };
  return subtitles[screen];
}

type IconName = "dashboard" | "campaign" | "plus" | "template" | "channel" | "table" | "chart" | "users" | "pulse" | "log" | "logout" | "login" | "alert" | "retry" | "switch" | "stop" | "play" | "save" | "cancel" | "archive";

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
    archive: "M4 7h16M6 7l1 12h10l1-12M9 11h6M8 4h8l1 3H7z",
  };
  return <svg className="icon" viewBox="0 0 24 24" aria-hidden="true"><path d={paths[name]} /></svg>;
}
