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
  defaultFilter,
  deliveriesSeed,
  effectiveTotalMessages,
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
  fetchStatsSnapshot,
  fetchWorkerStats,
  updateWorkerBounds,
  type WorkerStats,
  fetchTemplates,
  fetchTemplateVariables,
  normalizeCampaign,
  normalizeStatsSnapshot,
  operationsWebSocketURL,
  serviceHealthTargets,
  statsStreamURL,
  type StatsSnapshot,
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

type SpecificUser = { userId: string; channels: string[] };

type WizardState = {
  name: string;
  templateId: string;
  filter: AudienceFilter;
  selectedChannels: string[];
  specificUsers?: SpecificUser[];
};

export function App() {
  const [session, setSession] = useLocalState<Session>("norify-session", null);
  const [theme, setTheme] = useLocalState<ThemeName>("norify-theme", "sky");
  const [customColor, setCustomColor] = useLocalState("norify-custom-color", "#1f95f2");
  const [screen, setScreen] = useState<Screen>(() => screenFromLocation());
  const [templates, setTemplates] = useLocalState<Template[]>("norify-templates", templatesSeed);
  const [templateVariables, setTemplateVariables] = useLocalState<TemplateVariable[]>("norify-template-variables", templateVariablesSeed);
  const [channels, setChannels] = useLocalState<Channel[]>("norify-channels", channelsSeed);
  const [campaigns, setCampaigns] = useLocalState<Campaign[]>("norify-campaigns", campaignsSeed);
  const [deliveries, setDeliveries] = useLocalState<Delivery[]>("norify-deliveries", deliveriesSeed);
  const [storedErrorGroups, setErrorGroups] = useLocalState<ErrorGroup[]>("norify-error-groups", []);
  const errorGroups = storedErrorGroups.filter(isRealErrorGroup);
  const [events, setEvents] = useLocalState<SystemEvent[]>("norify-events", eventsSeed);
  const [managers, setManagers] = useLocalState<Manager[]>("norify-managers", managersSeed);
  const [healthChecks, setHealthChecks] = useState<ServiceHealth[]>(initialHealthChecks);
  const [statsSnapshot, setStatsSnapshot] = useState<StatsSnapshot | null>(null);
  const [statsState, setStatsState] = useState("stats connecting");
  const [selectedCampaignId, setSelectedCampaignId] = useState(campaigns[0]?.id ?? "");
  const [apiState, setApiState] = useState("connecting");
  const [pendingActionKeys, setPendingActionKeys] = useState<string[]>([]);
  const [createPending, setCreatePending] = useState(false);
  const opsSocketRef = useRef<WebSocket | null>(null);
  const pendingOpsRef = useRef(new Map<string, { resolve: (value: Record<string, unknown>) => void; reject: (error: Error) => void }>());
  const selectedCampaignIdRef = useRef(selectedCampaignId);
  const activeCampaigns = campaigns.filter((campaign) => !campaign.archivedAt);
  const selectedCampaign = activeCampaigns.find((campaign) => campaign.id === selectedCampaignId) ?? activeCampaigns[0] ?? campaigns[0];
  const activeError = selectedCampaign ? buildError(selectedCampaign) : emptyActionableError();
  const backendAvailable = healthChecks.some((check) => check.status === "ready");

  useEffect(() => {
    const path = pathForScreen(screen);
    if (window.location.pathname !== path) {
      window.history.pushState({ screen }, "", path);
    }
  }, [screen]);

  useEffect(() => {
    const syncScreenFromHistory = () => setScreen(screenFromLocation());
    window.addEventListener("popstate", syncScreenFromHistory);
    return () => window.removeEventListener("popstate", syncScreenFromHistory);
  }, []);

  useEffect(() => {
    selectedCampaignIdRef.current = selectedCampaignId;
  }, [selectedCampaignId]);

  useEffect(() => {
    if (storedErrorGroups.length !== errorGroups.length) {
      setErrorGroups(errorGroups);
    }
  }, [errorGroups, setErrorGroups, storedErrorGroups.length]);

  useEffect(() => {
    if (!session) return;
    void refreshBackendData();
  }, [session]);

  useEffect(() => {
    if (!backendAvailable) return;
    setApiState((current) => current === "connecting" || current === "local fallback" ? "live backend" : current);
  }, [backendAvailable]);

  useEffect(() => {
    if (!session) return;
    let closed = false;
    setStatsState("stats connecting");
    void fetchStatsSnapshot()
      .then((snapshot) => {
        if (closed) return;
        setStatsSnapshot(snapshot);
        setStatsState("stats snapshot");
        setApiState((current) => current === "connecting" || current === "local fallback" ? "live backend" : current);
      })
      .catch(() => {
        if (!closed) setStatsState("stats unavailable");
      });

    if (typeof EventSource === "undefined") {
      return () => { closed = true; };
    }
    const stream = new EventSource(statsStreamURL());
    const applySnapshot = (event: MessageEvent<string>) => {
      try {
        if (closed) return;
        setStatsSnapshot(normalizeStatsSnapshot(JSON.parse(event.data) as Record<string, unknown>));
        setStatsState("stats live");
        setApiState((current) => current === "connecting" || current === "local fallback" ? "live backend" : current);
      } catch (error) {
        console.error("stats stream parse error:", error);
      }
    };
    stream.addEventListener("snapshot", applySnapshot);
    stream.onmessage = applySnapshot;
    stream.onopen = () => {
      if (!closed) {
        setStatsState("stats live");
        setApiState((current) => current === "connecting" || current === "local fallback" ? "live backend" : current);
      }
    };
    stream.onerror = () => {
      if (!closed) setStatsState((current) => current === "stats live" ? "stats reconnecting" : current);
    };
    return () => {
      closed = true;
      stream.close();
    };
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
        setErrorGroups((prev) => replaceCampaignErrorGroups(prev, idToFetch, groups));
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
              if (!closed) setErrorGroups((prev) => replaceCampaignErrorGroups(prev, snapshot.campaign_id, groups));
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
    addEvent("warn", "frontend.local_fallback", "Backend недоступен: локальные демо-данные не являются доказательством доставки.", "frontend");
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

  function isActionPending(key: string) {
    return pendingActionKeys.includes(key);
  }

  async function runPendingAction(key: string, operation: () => Promise<void>) {
    if (isActionPending(key)) return;
    setPendingActionKeys((items) => [...items, key]);
    try {
      await operation();
    } finally {
      setPendingActionKeys((items) => items.filter((item) => item !== key));
    }
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
    if (createPending) return;
    setCreatePending(true);
    const template = templates.find((item) => item.id === wizard.templateId) ?? templates[0];
    const totalRecipients = wizard.specificUsers?.length ? wizard.specificUsers.length : audiencePreview(wizard.filter);
    const specificRecipients = wizard.specificUsers?.map((user) => ({ user_id: user.userId, channels: user.channels })) ?? [];
    const totalMessages = specificRecipients.length > 0
      ? specificRecipients.reduce((sum, user) => sum + user.channels.length, 0)
      : totalRecipients * wizard.selectedChannels.length;

    try {
      const result = await sendOpsCommand("campaign.create", {
        name: wizard.name,
        template_id: template.id,
        filters: wizard.filter,
        selected_channels: wizard.selectedChannels,
        total_recipients: totalRecipients,
        specific_recipients: specificRecipients,
      });
      const backendCampaign = normalizeCampaign((result.campaign ?? result) as Record<string, unknown>);
      setCampaigns((items) => [backendCampaign, ...items.filter((item) => item.id !== backendCampaign.id)]);
      setSelectedCampaignId(backendCampaign.id);
      setScreen("dashboard");
      setApiState("websocket commands");
      addEvent("info", "campaign.started", `Campaign ${backendCampaign.name} queued with ${totalMessages.toLocaleString()} messages`, "campaign-service");
      return;
    } catch (error) {
      setApiState("local fallback");
      const message = error instanceof Error ? error.message : "backend_unavailable";
      addEvent("error", "campaign.create_failed", `Campaign was not created in backend: ${message}`, "campaign-service");
    } finally {
      setCreatePending(false);
    }
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
    const key = campaignActionKey(targetCampaign.id, action);
    await runPendingAction(key, async () => {
      try {
        const message = await sendOpsCommand("campaign.action", { campaign_id: targetCampaign.id, action });
        if (message.campaign) {
          const updated = normalizeCampaign(message.campaign as Record<string, unknown>);
          setCampaigns((items) => items.map((campaign) => campaign.id === updated.id ? mergeCampaignUpdate(campaign, updated) : campaign));
          if (action === "archive" && selectedCampaign?.id === targetCampaign.id) {
            const nextActive = campaigns.find((campaign) => campaign.id !== targetCampaign.id && !campaign.archivedAt);
            if (nextActive) setSelectedCampaignId(nextActive.id);
          }
        }
        if (action !== "start" && action !== "stop") {
          setErrorGroups((items) => items.filter((group) => group.campaignId !== targetCampaign.id));
        }
        setApiState("websocket commands");
        addEvent("info", `campaign.${action}`, `${targetCampaign.name}: backend confirmed ${action}`, "campaign-service");
      } catch (error) {
        setApiState("local fallback");
        const message = error instanceof Error ? error.message : "command_failed";
        addEvent("error", `campaign.${action}.failed`, `${targetCampaign.name}: backend did not apply ${action} (${message})`, "campaign-service");
      }
    });
  }

  async function handleErrorGroupAction(group: ErrorGroup, action: "retry" | "switch_channel" | "cancel_group", toChannel?: string) {
    if (!selectedCampaign) return;
    const targetChannel = toChannel ?? channels.find((channel) => channel.enabled && channel.code !== group.channelCode)?.code ?? "";
    const key = errorGroupActionKey(group.id, action);
    await runPendingAction(key, async () => {
      try {
        const message = await sendOpsCommand("error_group.action", {
          campaign_id: selectedCampaign.id,
          group_id: group.id,
          action,
          to_channel: targetChannel,
        });
        if (message.campaign) {
          const campaign = normalizeCampaign(message.campaign as Record<string, unknown>);
          setCampaigns((items) => items.map((item) => item.id === campaign.id ? campaign : item));
        }
        setErrorGroups((items) => items.filter((item) => item.id !== group.id));
        setApiState("websocket commands");
        addEvent("info", `error_group.${action}`, `${group.channelCode}/${group.errorCode}: backend confirmed group action`, "campaign-service");
      } catch (error) {
        setApiState("local fallback");
        const message = error instanceof Error ? error.message : "command_failed";
        addEvent("error", `error_group.${action}.failed`, `${group.channelCode}/${group.errorCode}: backend did not apply action (${message})`, "campaign-service");
      }
    });
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
            <button className="primary" onClick={() => setScreen("create")}><Icon name="plus" /> Новая кампания</button>
          </div>
        </header>
        {apiState === "local fallback" && !backendAvailable && (
          <div className="fallbackBanner" role="alert">
            Backend недоступен. Локальные демо-данные не считаются реальной доставкой; новые кампании и повторные действия не применяются без подтверждения backend.
          </div>
        )}

        {screen === "dashboard" && selectedCampaign && (
          <Dashboard
            campaign={selectedCampaign}
            channels={channels}
            error={activeError}
            errorGroups={errorGroups.filter((group) => group.campaignId === selectedCampaign.id)}
            onAction={handleCampaignAction}
            onGroupAction={handleErrorGroupAction}
            pendingActionKeys={pendingActionKeys}
          />
        )}
        {screen === "campaigns" && <CampaignList campaigns={campaigns} selectedId={selectedCampaign?.id} onSelect={(id) => setSelectedCampaignId(id)} onAction={(campaignId, action) => handleCampaignAction(action, campaignId)} pendingActionKeys={pendingActionKeys} />}
        {screen === "create" && <CreateCampaign templates={templates} channels={channels} onCreate={createCampaign} pending={createPending} />}
        {screen === "templates" && <Templates templates={templates} variableOptions={templateVariables} onSave={updateTemplate} />}
        {screen === "channels" && <Channels channels={channels} role={userSession.role} onUpdate={updateChannel} />}
        {screen === "deliveries" && <Deliveries deliveries={deliveries} campaigns={campaigns} />}
        {screen === "stats" && <Stats snapshot={statsSnapshot} statsState={statsState} />}
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

function campaignActionKey(campaignId: string, action: CampaignCommand) {
  return `campaign:${campaignId}:${action}`;
}

function errorGroupActionKey(groupId: string, action: "retry" | "switch_channel" | "cancel_group") {
  return `error-group:${groupId}:${action}`;
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
  pendingActionKeys,
}: {
  campaign: Campaign;
  channels: Channel[];
  error: ActionableError;
  errorGroups: ErrorGroup[];
  onAction: (action: CampaignCommand) => void;
  onGroupAction: (group: ErrorGroup, action: "retry" | "switch_channel" | "cancel_group", toChannel?: string) => void;
  pendingActionKeys: string[];
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
          <CampaignPlayerControls campaign={campaign} onAction={onAction} pendingActionKeys={pendingActionKeys} />
          {campaign.failed > 0 && (
            <div className="recoveryActions" aria-label="Campaign recovery actions">
              <button disabled={pendingActionKeys.includes(campaignActionKey(campaign.id, "retry"))} onClick={() => onAction("retry")}><Icon name="retry" /> Повторить ошибки</button>
              <span className="inlineNotice">Смена канала для всей кампании отключена: используйте группы ошибок.</span>
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
          {campaign.selectedChannels.map((channel, index) => {
            const channelTotal = Math.round(campaign.totalMessages / Math.max(1, campaign.selectedChannels.length));
            const channelSent = Math.round((campaign.success / Math.max(1, campaign.selectedChannels.length)) * (1 - index * 0.04));
            const fillPct = channelTotal > 0 ? Math.min(100, Math.round((channelSent / channelTotal) * 100)) : 0;
            return (
              <div key={channel} className="splitRow">
                <span>{channel}</span>
                <div><i style={{ width: `${fillPct}%` }} /></div>
                <strong>{channelSent.toLocaleString()} <span className="channelTotalFraction">/ {channelTotal.toLocaleString()}</span></strong>
              </div>
            );
          })}
        </div>
      </section>

      <ErrorGroupsPanel
        campaign={campaign}
        channels={channels}
        error={error}
        groups={errorGroups}
        onAction={onAction}
        onGroupAction={onGroupAction}
        pendingActionKeys={pendingActionKeys}
      />
    </div>
  );
}

function CampaignPlayerControls({ campaign, onAction, pendingActionKeys }: { campaign: Campaign; onAction: (action: CampaignCommand) => void; pendingActionKeys: string[] }) {
  const isRunning = campaign.status === "running" || campaign.status === "retrying";
  const isComplete = campaign.status === "cancelled" || campaign.status === "finished";
  const canStart = campaign.status === "created" || campaign.status === "stopped";
  const startLabel = campaign.status === "stopped" ? "Продолжить" : "Запустить";
  const startPending = pendingActionKeys.includes(campaignActionKey(campaign.id, "start"));
  const stopPending = pendingActionKeys.includes(campaignActionKey(campaign.id, "stop"));
  const cancelPending = pendingActionKeys.includes(campaignActionKey(campaign.id, "cancel_campaign"));

  return (
    <div className="transportControls" aria-label="Campaign player controls">
      <button className="primary" disabled={!canStart || startPending} onClick={() => onAction("start")}>
        <Icon name="play" /> {startLabel}
      </button>
      <button disabled={!isRunning || stopPending} onClick={() => onAction("stop")}>
        <Icon name="stop" /> Остановить
      </button>
      <button className="danger" disabled={isComplete || cancelPending} onClick={() => onAction("cancel_campaign")}>
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
  pendingActionKeys,
}: {
  campaign: Campaign;
  channels: Channel[];
  error: ActionableError;
  groups: ErrorGroup[];
  onAction: (action: CampaignCommand) => void;
  onGroupAction: (group: ErrorGroup, action: "retry" | "switch_channel" | "cancel_group", toChannel?: string) => void;
  pendingActionKeys: string[];
}) {
  return (
    <section className="panel errorGroupsPanel" aria-label="Группы ошибок">
      <div className="panelHeader">
        <h2>Группа ошибок</h2>
      </div>
      {groups.length > 0 ? (
        <>
          <div className="errorGroups">
            {groups.map((group) => <ErrorGroupCard key={group.id} group={group} channels={channels} onAction={onGroupAction} pendingActionKeys={pendingActionKeys} />)}
          </div>
        </>
      ) : (
        <div className="emptyState">
          <strong>Нет активных групп ошибок</strong>
          <span>Основная отправка продолжается. Новые сбои появятся здесь отдельными группами для точечного решения.</span>
          {campaign.failed > 0 && (
            <div className="buttonRow">
              <button disabled={pendingActionKeys.includes(campaignActionKey(campaign.id, "retry"))} onClick={() => onAction("retry")}><Icon name="retry" /> {error.actions[0].label}</button>
              <span className="inlineNotice">Смена канала для всей кампании отключена: требуется точная маршрутизация по конкретным ошибочным доставкам.</span>
              <button className="danger" disabled={pendingActionKeys.includes(campaignActionKey(campaign.id, "cancel_campaign"))} onClick={() => onAction("cancel_campaign")}><Icon name="stop" /> {error.actions[2].label}</button>
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
  pendingActionKeys,
}: {
  group: ErrorGroup;
  channels: Channel[];
  onAction: (group: ErrorGroup, action: "retry" | "switch_channel" | "cancel_group", toChannel?: string) => void;
  pendingActionKeys: string[];
}) {
  const alternativeChannels = channels.filter((channel) => channel.enabled && channel.code !== group.channelCode);
  const [selectedChannel, setSelectedChannel] = useState(preferredFallbackChannel(alternativeChannels));
  const alternativeChannelKey = alternativeChannels.map((channel) => channel.code).join("|");
  const actions = group.recommendedActions.length > 0 ? group.recommendedActions : [
    { code: "retry", label: "Повторить группу" },
    { code: "switch_channel", label: "Вставить через другой канал" },
    { code: "cancel_group", label: "Закрыть группу" },
  ];
  const hasAction = (code: "retry" | "switch_channel" | "cancel_group") => actions.some((action) => action.code === code);
  const retryPending = pendingActionKeys.includes(errorGroupActionKey(group.id, "retry"));
  const switchPending = pendingActionKeys.includes(errorGroupActionKey(group.id, "switch_channel"));
  const cancelPending = pendingActionKeys.includes(errorGroupActionKey(group.id, "cancel_group"));

  useEffect(() => {
    if (selectedChannel && alternativeChannels.some((channel) => channel.code === selectedChannel)) return;
    setSelectedChannel(preferredFallbackChannel(alternativeChannels));
  }, [alternativeChannelKey, selectedChannel]);

  function handleChannelSelect(nextChannel: string) {
    setSelectedChannel(nextChannel);
    if (!nextChannel || nextChannel === selectedChannel || switchPending) return;
    onAction(group, "switch_channel", nextChannel);
  }

  return (
    <article className="errorGroupCard">
      <div className="groupHeader">
        <div>
          <strong>{formatChannelName(group.channelCode)}</strong>
          <p>{group.errorMessage || errorCodeLabel(group.errorCode)}</p>
          {group.errorCode && <div className="groupMeta"><code>{group.errorCode}</code></div>}
        </div>
        <b>{group.failedCount.toLocaleString()}</b>
      </div>
      <div className="groupImpact">{group.impact}</div>
      <div className="groupActions">
        {hasAction("retry") && (
          <button disabled={retryPending} onClick={() => onAction(group, "retry")}>
            Повторить
          </button>
        )}
        {hasAction("switch_channel") && (
          <label className="switchChannelAction">
            <span className="srOnly">Альтернативный канал</span>
            <select aria-label="Альтернативный канал" value={selectedChannel} disabled={!selectedChannel || switchPending} onChange={(event) => handleChannelSelect(event.target.value)}>
              {alternativeChannels.map((channel) => <option key={channel.code} value={channel.code}>{formatChannelName(channel.code, channel.name)}</option>)}
            </select>
          </label>
        )}
        {hasAction("cancel_group") && (
          <button className="danger" disabled={cancelPending} onClick={() => onAction(group, "cancel_group")}>
            Закрыть
          </button>
        )}
      </div>
    </article>
  );
}

function preferredFallbackChannel(channels: Channel[]) {
  return channels.find((channel) => channel.code === "custom_app")?.code ?? channels[0]?.code ?? "";
}

function formatChannelName(code: string, fallback?: string) {
  if (code === "custom_app") return "CustomApp";
  if (code === "max") return "Max";
  return fallback ?? code;
}

function errorCodeLabel(code: string) {
  return code ? `Код ошибки: ${code}` : "Ошибка доставки без сообщения адаптера";
}

function CampaignList({ campaigns, selectedId, onSelect, onAction, pendingActionKeys }: { campaigns: Campaign[]; selectedId?: string; onSelect: (id: string) => void; onAction: (campaignId: string, action: "start" | "retry" | "cancel_campaign" | "archive") => void; pendingActionKeys: string[] }) {
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
                  {!campaign.archivedAt && campaign.status === "created" && <button disabled={pendingActionKeys.includes(campaignActionKey(campaign.id, "start"))} onClick={() => onAction(campaign.id, "start")}><Icon name="play" /> Старт</button>}
                  {!campaign.archivedAt && campaign.failed > 0 && <button disabled={pendingActionKeys.includes(campaignActionKey(campaign.id, "retry"))} onClick={() => onAction(campaign.id, "retry")}><Icon name="retry" /> Повторить</button>}
                  {!campaign.archivedAt && (campaign.status === "running" || campaign.status === "retrying") && <button className="danger" disabled={pendingActionKeys.includes(campaignActionKey(campaign.id, "cancel_campaign"))} onClick={() => onAction(campaign.id, "cancel_campaign")}><Icon name="stop" /> Отменить</button>}
                  {!campaign.archivedAt && campaign.status !== "created" && <button disabled={pendingActionKeys.includes(campaignActionKey(campaign.id, "archive"))} onClick={() => onAction(campaign.id, "archive")}><Icon name="archive" /> В архив</button>}
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

const FIRST_NAMES = ["Алексей","Мария","Дмитрий","Анна","Иван","Елена","Сергей","Ольга","Андрей","Наталья","Михаил","Татьяна","Александр","Ирина","Николай","Юлия","Павел","Екатерина","Роман","Светлана"];
const LAST_NAMES = ["Петров","Иванова","Сидоров","Козлова","Новиков","Морозова","Волков","Соколова","Лебедев","Попова","Захаров","Федорова","Васильев","Смирнова","Орлов","Кузнецова","Беляев","Николаева","Макаров","Семенова"];
const CITIES = ["Moscow","Saint Petersburg","Kazan","Novosibirsk","Yekaterinburg","Samara","Omsk","Chelyabinsk","Rostov","Ufa"];
const TAG_SETS: string[][] = [["vip"],["retail"],["vip","retail"],["b2b"],["vip","b2b"],[],["retail","promo"],[],["vip"],[],];

const MOCK_USERS: { id: string; name: string; email: string; city: string; tags: string[] }[] = Array.from({ length: 50000 }, (_, i) => {
  const num = String(i + 1).padStart(5, "0");
  const fn = FIRST_NAMES[i % FIRST_NAMES.length];
  const ln = LAST_NAMES[(i * 3) % LAST_NAMES.length];
  return {
    id: `user-${num}`,
    name: `${fn} ${ln}`,
    email: `${fn.toLowerCase()}${num}@example.com`,
    city: CITIES[i % CITIES.length],
    tags: TAG_SETS[i % TAG_SETS.length],
  };
});

const PICKER_PAGE_SIZE = 50;

function ChannelDropdown({ userId, channels, availableChannels, disabled, onToggle }: {
  userId: string;
  channels: Set<string>;
  availableChannels: string[];
  disabled: boolean;
  onToggle: (userId: string, ch: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const btnRef = useRef<HTMLButtonElement>(null);
  const [pos, setPos] = useState({ top: 0, left: 0 });

  function handleOpen(e: React.MouseEvent) {
    e.stopPropagation();
    if (disabled) return;
    if (btnRef.current) {
      const rect = btnRef.current.getBoundingClientRect();
      setPos({ top: rect.bottom + 4, left: rect.left });
    }
    setOpen((v) => !v);
  }

  useEffect(() => {
    if (!open) return;
    function handler(e: MouseEvent) {
      if (!(e.target as Element).closest(".chDropMenu") && !(e.target as Element).closest(".chDropBtn")) setOpen(false);
    }
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  const label = disabled ? (channels.size > 0 ? "общие" : "—") : `${channels.size} / ${availableChannels.length}`;
  return (
    <>
      <button ref={btnRef} className={`chDropBtn${disabled ? " dim" : ""}${open ? " open" : ""}`} onClick={handleOpen} title={disabled ? "" : [...channels].join(", ")}>
        {label} {!disabled && "▾"}
      </button>
      {open && (
        <div className="chDropMenu" style={{ position: "fixed", top: pos.top, left: pos.left, zIndex: 2000 }}>
          {availableChannels.map((ch) => {
            const active = channels.has(ch);
            const canUncheck = channels.size > 1;
            return (
              <label key={ch} className={`chDropItem${active ? " active" : ""}`}>
                <input
                  type="checkbox"
                  checked={active}
                  disabled={active && !canUncheck}
                  onChange={() => { onToggle(userId, ch); }}
                />
                {ch}
              </label>
            );
          })}
        </div>
      )}
    </>
  );
}

function UserPickerModal({ availableChannels, initialSelection, onClose, onConfirm }: {
  availableChannels: string[];
  initialSelection: SpecificUser[];
  onClose: () => void;
  onConfirm: (users: SpecificUser[]) => void;
}) {
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [pageInput, setPageInput] = useState("1");
  const [selection, setSelection] = useState<Map<string, Set<string>>>(() => {
    const map = new Map<string, Set<string>>();
    for (const u of initialSelection) map.set(u.userId, new Set(u.channels));
    return map;
  });

  const filtered = search.trim()
    ? MOCK_USERS.filter((u) => {
        const q = search.trim().toLowerCase();
        return u.id.includes(q) || u.name.toLowerCase().includes(q) || u.email.includes(q) || u.city.toLowerCase().includes(q) || u.tags.some((t) => t.includes(q));
      })
    : MOCK_USERS;

  const totalPages = Math.max(1, Math.ceil(filtered.length / PICKER_PAGE_SIZE));
  const safePage = Math.min(page, totalPages);
  const pageUsers = filtered.slice((safePage - 1) * PICKER_PAGE_SIZE, safePage * PICKER_PAGE_SIZE);

  useEffect(() => { setPageInput(String(safePage)); }, [safePage]);

  function handleSearch(value: string) { setSearch(value); setPage(1); }

  function commitPageInput() {
    const n = parseInt(pageInput, 10);
    if (!isNaN(n) && n >= 1 && n <= totalPages) setPage(n);
    else setPageInput(String(safePage));
  }

  function toggleUser(userId: string) {
    setSelection((prev) => {
      const next = new Map(prev);
      if (next.has(userId)) next.delete(userId);
      else next.set(userId, new Set(availableChannels));
      return next;
    });
  }

  function toggleUserChannel(userId: string, ch: string) {
    setSelection((prev) => {
      if (!prev.has(userId)) return prev;
      const next = new Map(prev);
      const chs = new Set(next.get(userId)!);
      if (chs.has(ch)) { if (chs.size > 1) chs.delete(ch); }
      else chs.add(ch);
      next.set(userId, chs);
      return next;
    });
  }

  function selectAllFiltered() {
    setSelection((prev) => {
      const next = new Map(prev);
      for (const u of filtered) if (!next.has(u.id)) next.set(u.id, new Set(availableChannels));
      return next;
    });
  }

  function deselectAllFiltered() {
    setSelection((prev) => {
      const next = new Map(prev);
      for (const u of filtered) next.delete(u.id);
      return next;
    });
  }

  const pageAllSelected = pageUsers.length > 0 && pageUsers.every((u) => selection.has(u.id));
  const filteredSelectedCount = filtered.filter((u) => selection.has(u.id)).length;
  const allFilteredSelected = filteredSelectedCount === filtered.length && filtered.length > 0;

  function confirm() {
    onConfirm([...selection.entries()].map(([userId, chs]) => ({ userId, channels: [...chs] })));
  }

  return (
    <div className="modalOverlay" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="modalBox userPickerModal">
        <div className="modalHeader">
          <h2>Выбор пользователей</h2>
          <span className="muted">{selection.size.toLocaleString()} выбрано из {MOCK_USERS.length.toLocaleString()}</span>
          <button className="iconButton" onClick={onClose}><Icon name="cancel" /></button>
        </div>
        <div className="modalSearch">
          <input value={search} onChange={(e) => handleSearch(e.target.value)} placeholder="Поиск по ID, имени, email, городу, тегу…" autoFocus />
          <span className="muted">{filtered.length.toLocaleString()} найдено</span>
        </div>
        <div className="pickerBulkBar">
          <button className="softButton" onClick={allFilteredSelected ? deselectAllFiltered : selectAllFiltered}>
            {allFilteredSelected ? `Снять все ${filtered.length.toLocaleString()}` : `Выбрать все ${filtered.length.toLocaleString()}`}
          </button>
          {selection.size > 0 && <button onClick={() => setSelection(new Map())}>Сбросить выбор</button>}
          <span className="pickerChannelHint">Каналы по умолчанию: {availableChannels.join(", ")}</span>
        </div>
        <div className="userPickerTable">
          <div className="userPickerHead">
            <input type="checkbox" checked={pageAllSelected} onChange={() => {
              if (pageAllSelected) setSelection((prev) => { const next = new Map(prev); pageUsers.forEach((u) => next.delete(u.id)); return next; });
              else setSelection((prev) => { const next = new Map(prev); pageUsers.forEach((u) => { if (!next.has(u.id)) next.set(u.id, new Set(availableChannels)); }); return next; });
            }} title="Выбрать/снять страницу" />
            <span>ID / Имя</span>
            <span>Email · Город</span>
            <span>Теги</span>
            <span>Каналы</span>
          </div>
          <div className="userPickerBody">
            {pageUsers.map((user) => {
              const isSelected = selection.has(user.id);
              const userChannels = selection.get(user.id) ?? new Set<string>();
              return (
                <div key={user.id} className={`userPickerRow${isSelected ? " picked" : ""}`} onClick={() => toggleUser(user.id)}>
                  <input type="checkbox" checked={isSelected} onChange={() => toggleUser(user.id)} onClick={(e) => e.stopPropagation()} />
                  <span><strong>{user.id}</strong><small>{user.name}</small></span>
                  <span><span>{user.email}</span><small className="muted">{user.city}</small></span>
                  <span className="userTags">{user.tags.length > 0 ? user.tags.map((t) => <small key={t}>{t}</small>) : <span className="muted">—</span>}</span>
                  <span onClick={(e) => e.stopPropagation()}>
                    <ChannelDropdown
                      userId={user.id}
                      channels={userChannels}
                      availableChannels={availableChannels}
                      disabled
                      onToggle={toggleUserChannel}
                    />
                  </span>
                </div>
              );
            })}
            {pageUsers.length === 0 && <div className="emptyState"><strong>Никто не найден</strong></div>}
          </div>
        </div>
        <div className="pickerPager">
          <div className="buttonRow tight">
            <button disabled={safePage <= 1} onClick={() => setPage(1)}>«</button>
            <button disabled={safePage <= 1} onClick={() => setPage((p) => p - 1)}>‹</button>
            <span className="pickerPageLabel">
              Стр.&nbsp;
              <input
                className="pickerPageInput"
                type="number"
                min={1}
                max={totalPages}
                value={pageInput}
                onChange={(e) => setPageInput(e.target.value)}
                onBlur={commitPageInput}
                onKeyDown={(e) => { if (e.key === "Enter") { commitPageInput(); (e.target as HTMLInputElement).blur(); } }}
              />
              &nbsp;/ {totalPages.toLocaleString()}
            </span>
            <button disabled={safePage >= totalPages} onClick={() => setPage((p) => p + 1)}>›</button>
            <button disabled={safePage >= totalPages} onClick={() => setPage(totalPages)}>»</button>
          </div>
          <span className="muted">{((safePage - 1) * PICKER_PAGE_SIZE + 1).toLocaleString()}–{Math.min(safePage * PICKER_PAGE_SIZE, filtered.length).toLocaleString()} из {filtered.length.toLocaleString()}</span>
        </div>
        <div className="modalFooter">
          <button onClick={onClose}>Отмена</button>
          <button className="primary" disabled={selection.size === 0} onClick={confirm}><Icon name="play" /> Применить ({selection.size.toLocaleString()})</button>
        </div>
      </div>
    </div>
  );
}

function CreateCampaign({ templates, channels, onCreate, pending }: { templates: Template[]; channels: Channel[]; onCreate: (wizard: WizardState) => void | Promise<void>; pending: boolean }) {
  const enabledChannels = channels.filter((channel) => channel.enabled);
  const [wizard, setWizard] = useState<WizardState>({
    name: "Новая кампания",
    templateId: templates[0]?.id ?? "",
    filter: defaultFilter,
    selectedChannels: enabledChannels.slice(0, 3).map((channel) => channel.code),
  });
  const [showPicker, setShowPicker] = useState(false);

  const isSpecific = (wizard.specificUsers?.length ?? 0) > 0;
  const preview = isSpecific ? (wizard.specificUsers?.length ?? 0) : audiencePreview(wizard.filter);
  const totalMessages = isSpecific
    ? preview * wizard.selectedChannels.length
    : preview * wizard.selectedChannels.length;
  const canStart = wizard.name.trim().length > 2 && Boolean(wizard.templateId) && wizard.selectedChannels.length > 0 && !pending;

  function toggleChannel(code: string) {
    setWizard((current) => {
      const selectedChannels = current.selectedChannels.includes(code)
        ? current.selectedChannels.filter((item) => item !== code)
        : [...current.selectedChannels, code];
      return {
        ...current,
        selectedChannels,
        specificUsers: current.specificUsers?.map((user) => ({ ...user, channels: selectedChannels })),
      };
    });
  }

  function applySpecificUsers(users: SpecificUser[]) {
    setWizard((current) => ({
      ...current,
      specificUsers: users.length > 0
        ? users.map((user) => ({ ...user, channels: current.selectedChannels }))
        : undefined,
    }));
    setShowPicker(false);
  }

  return (
    <>
      {showPicker && (
        <UserPickerModal
          availableChannels={enabledChannels.map((c) => c.code).filter((c) => wizard.selectedChannels.includes(c))}
          initialSelection={wizard.specificUsers ?? []}
          onClose={() => setShowPicker(false)}
          onConfirm={applySpecificUsers}
        />
      )}
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
          {isSpecific ? (
            <div className="specificUsersChip">
              <span><Icon name="users" /> {wizard.specificUsers!.length.toLocaleString()} пользователей выбрано точечно</span>
              <small className="muted">{totalMessages.toLocaleString()} сообщений · backend dispatch получит выбранные ID</small>
              <div className="buttonRow tight">
                <button onClick={() => setShowPicker(true)}>Изменить</button>
                <button className="danger" onClick={() => setWizard((c) => ({ ...c, specificUsers: undefined }))}>Сбросить</button>
              </div>
            </div>
          ) : (
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
          )}
          <div className="formActions">
            <button onClick={() => setShowPicker(true)}><Icon name="users" /> {isSpecific ? "Изменить выбор" : "Выбрать точечно"}</button>
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
          <div className="formActions"><button className="primary startButton" disabled={!canStart} onClick={() => onCreate(wizard)}><Icon name="play" /> {pending ? "Запуск..." : "Запустить кампанию"}</button></div>
        </section>
      </div>
    </>
  );
}

const TEMPLATE_GENERATOR_URL = "http://localhost:8091";

type AIStyle = "professional" | "creative" | "luxury" | "minimal" | "ecommerce";

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function safeNewsletterHref(value: string): string {
  const trimmed = value.trim();
  return /^https?:\/\//i.test(trimmed) ? escapeHtml(trimmed) : "#";
}

export function newsletterMarkdownToHtml(markdown: string): string {
  const lines = markdown.split("\n");
  const out: string[] = [];
  let inList = false;

  for (const raw of lines) {
    if (/^(\*\*)?subject/i.test(raw.trim())) continue;

    let line = escapeHtml(raw)
      .replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>")
      .replace(/\*(.+?)\*/g, "<em>$1</em>")
      .replace(/\[(.+?)\]\((.+?)\)/g, (_match, text: string, href: string) => `<a href="${safeNewsletterHref(href)}">${text}</a>`);

    const hMatch = line.match(/^#{1,3}\s+(.+)/);
    if (hMatch) {
      if (inList) { out.push("</ul>"); inList = false; }
      out.push(`<h3>${hMatch[1]}</h3>`);
      continue;
    }

    if (line.trim() === "") {
      if (inList) { out.push("</ul>"); inList = false; }
      continue;
    }

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

function AIGenerator({ onApply }: { onApply: (text: string) => void }) {
  const [step, setStep] = useState<1 | 2>(1);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [taskDesc, setTaskDesc] = useState("");
  const [style, setStyle] = useState<AIStyle>("professional");
  const [generatedText, setGeneratedText] = useState("");
  const [editMode, setEditMode] = useState(false);

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
            <div className="aiTextPreview" dangerouslySetInnerHTML={{ __html: newsletterMarkdownToHtml(generatedText) }} />
          )}
          <div className="aiActions">
            <button onClick={() => { setStep(1); setGeneratedText(""); }}>← Назад</button>
            <button onClick={() => handleGenerateText()} disabled={loading}>{loading ? "Генерирую..." : "Перегенерировать"}</button>
            <button className="primary" onClick={() => onApply(generatedText)}>Использовать текст →</button>
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
      {activeTab === "ai" && <AIGenerator onApply={(text) => { setEditing({ ...createBlankTemplate(), body: text }); setActiveTab("library"); }} />}
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
          <div className="messageBubble">{preview || "..."}</div>
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

function formatRate(value: number | null) {
  if (value === null || Number.isNaN(value)) return "Нет данных";
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

const PAGE_SIZE = 100;

function Deliveries({ deliveries, campaigns }: { deliveries: Delivery[]; campaigns: Campaign[] }) {
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const [channelFilter, setChannelFilter] = useState("all");
  const [campaignFilter, setCampaignFilter] = useState("all");
  const [page, setPage] = useState(1);
  const [pageInput, setPageInput] = useState("1");

  const allChannels = [...new Set(deliveries.map((d) => d.channelCode))].sort();

  const filtered = deliveries.filter((delivery) => {
    if (campaignFilter !== "all" && delivery.campaignId !== campaignFilter) return false;
    if (statusFilter !== "all" && delivery.status !== statusFilter) return false;
    if (channelFilter !== "all" && delivery.channelCode !== channelFilter) return false;
    if (search.trim()) {
      const q = search.trim().toLowerCase();
      return delivery.userId.toLowerCase().includes(q) || delivery.channelCode.toLowerCase().includes(q) || (delivery.errorMessage ?? "").toLowerCase().includes(q);
    }
    return true;
  });

  const totalPages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const safePage = Math.min(page, totalPages);
  const visible = filtered.slice((safePage - 1) * PAGE_SIZE, safePage * PAGE_SIZE);

  useEffect(() => { setPageInput(String(safePage)); }, [safePage]);

  function reset() { setSearch(""); setStatusFilter("all"); setChannelFilter("all"); setCampaignFilter("all"); setPage(1); }
  function commitPage() {
    const n = parseInt(pageInput, 10);
    if (!isNaN(n) && n >= 1 && n <= totalPages) setPage(n);
    else setPageInput(String(safePage));
  }
  const hasFilter = search || statusFilter !== "all" || channelFilter !== "all" || campaignFilter !== "all";

  return (
    <section className="panel wide">
      <div className="panelHeader">
        <h2>Результаты доставки</h2>
        <span>{filtered.length.toLocaleString()} / {deliveries.length.toLocaleString()} записей</span>
      </div>
      <div className="deliveriesControls">
        <input value={search} onChange={(e) => { setSearch(e.target.value); setPage(1); }} placeholder="Поиск по userId, каналу, ошибке…" />
        <select value={campaignFilter} onChange={(e) => { setCampaignFilter(e.target.value); setPage(1); }}>
          <option value="all">Все кампании</option>
          {campaigns.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
        </select>
        <select value={statusFilter} onChange={(e) => { setStatusFilter(e.target.value); setPage(1); }}>
          <option value="all">Все статусы</option>
          <option value="sent">sent</option>
          <option value="failed">failed</option>
          <option value="queued">queued</option>
          <option value="cancelled">cancelled</option>
        </select>
        <select value={channelFilter} onChange={(e) => { setChannelFilter(e.target.value); setPage(1); }}>
          <option value="all">Все каналы</option>
          {allChannels.map((ch) => <option key={ch} value={ch}>{ch}</option>)}
        </select>
        {hasFilter && <button onClick={reset}>Сбросить</button>}
      </div>
      <div className="tableWrap">
        <table>
          <thead><tr><th>Пользователь</th><th>Канал</th><th>Статус</th><th>Попытка</th><th>Ошибка</th><th>Завершено</th></tr></thead>
          <tbody>
            {visible.map((delivery) => (
              <tr key={delivery.id}>
                <td>{delivery.userId}</td>
                <td>{delivery.channelCode}</td>
                <td><span className={`delivery delivery-${delivery.status}`}>{delivery.status}</span></td>
                <td>{delivery.attempt}</td>
                <td>{delivery.errorMessage ?? "—"}</td>
                <td>{formatDate(delivery.finishedAt)}</td>
              </tr>
            ))}
            {visible.length === 0 && <tr><td colSpan={6}><div className="emptyState"><strong>Нет результатов</strong><span>Попробуйте изменить фильтры или поисковый запрос.</span></div></td></tr>}
          </tbody>
        </table>
      </div>
      {totalPages > 1 && (
        <div className="deliveriesPager">
          <span>{filtered.length.toLocaleString()} записей</span>
          <div className="buttonRow tight">
            <button disabled={safePage <= 1} onClick={() => setPage(1)}>«</button>
            <button disabled={safePage <= 1} onClick={() => setPage((p) => p - 1)}>‹</button>
            <span className="pickerPageLabel">
              Стр.&nbsp;
              <input
                className="pickerPageInput"
                type="number"
                min={1}
                max={totalPages}
                value={pageInput}
                onChange={(e) => setPageInput(e.target.value)}
                onBlur={commitPage}
                onKeyDown={(e) => { if (e.key === "Enter") { commitPage(); (e.target as HTMLInputElement).blur(); } }}
              />
              &nbsp;/ {totalPages}
            </span>
            <button disabled={safePage >= totalPages} onClick={() => setPage((p) => p + 1)}>›</button>
            <button disabled={safePage >= totalPages} onClick={() => setPage(totalPages)}>»</button>
          </div>
        </div>
      )}
    </section>
  );
}

function Stats({ snapshot, statsState }: { snapshot: StatsSnapshot | null; statsState: string }) {
  if (!snapshot) {
    const waitingForSnapshot = statsState === "stats connecting" || statsState === "stats live" || statsState === "stats reconnecting";
    return (
      <section className="panel wide statsUnavailable">
        <div className="emptyState">
          <strong>{waitingForSnapshot ? "Stats-service подключен" : "Stats-service недоступен"}</strong>
          <span>{waitingForSnapshot ? "Ждем первый snapshot статистики." : "Реальная статистика строится только из отдельного сервиса статистики. Демо-расчеты из локальных seed-данных отключены."}</span>
          <span className="apiState">{statsState}</span>
        </div>
      </section>
    );
  }
  const { totals } = snapshot;
  return (
    <div className="pageGrid">
      <section className="panel wide statsOverviewPanel">
        <div className="panelHeader">
          <h2>Реальная статистика</h2>
          <span>{statsState} · {snapshot.source}</span>
        </div>
        <div className="stats">
          <Metric label="всего сообщений" value={totals.messages.toLocaleString()} />
          <Metric label="обработано" value={totals.processed.toLocaleString()} />
          <Metric label="успешно" value={totals.success.toLocaleString()} tone="success" />
          <Metric label="ошибки" value={totals.failed.toLocaleString()} tone="danger" />
          <Metric label="успешность" value={formatRate(totals.successRate)} tone={totals.successRate !== null && totals.successRate >= 0.9 ? "success" : undefined} />
          <Metric label="в очереди DB" value={totals.pending.toLocaleString()} />
          <Metric label="очередь RabbitMQ" value={totals.queueDepth >= 0 ? totals.queueDepth.toLocaleString() : "Нет данных"} />
          <Metric label="активные" value={String(totals.active)} />
          <Metric label="p95 enqueue" value={totals.p95DispatchMs > 0 ? `${totals.p95DispatchMs} ms` : "pending"} />
        </div>
      </section>
      <RealtimeDeliveryCharts snapshot={snapshot} />
      <section className="panel wide">
        <div className="panelHeader"><h2>Качество каналов</h2><span>{snapshot.channels.length} каналов</span></div>
        <div className="barChart">
          {snapshot.channels.map((stat) => {
            const hasStats = stat.successRate !== null && stat.total > 0;
            const rate = hasStats ? Math.min(1, Math.max(0, stat.successRate ?? 0)) : 0;
            const fillWidth = hasStats && rate > 0 ? `${Math.max(2, Math.round(rate * 100))}%` : "0%";
            return (
              <div key={stat.code} className={`qualityRow${hasStats ? "" : " empty"}`} data-testid={`channel-quality-${stat.code}`}>
                <span className="qualityName">{stat.code}</span>
                <span className="qualityTrack" aria-hidden="true"><i className="qualityFill" style={{ width: fillWidth }} /></span>
                <strong>{hasStats ? `${formatRate(rate)} · ${stat.total.toLocaleString()}` : "Нет данных"}</strong>
              </div>
            );
          })}
        </div>
      </section>
      <ServiceMailingMetrics snapshot={snapshot} />
    </div>
  );
}

function RealtimeDeliveryCharts({ snapshot }: { snapshot: StatsSnapshot }) {
  const points = snapshot.realtime;
  const last = points[points.length - 1] ?? { sent: 0, failed: 0, bucket: "now" };
  return (
    <section className="panel wide realtimePanel">
      <div className="panelHeader">
        <h2>Отправки / сбои real-time</h2>
        <span>{points.length} точек · {formatDate(snapshot.generatedAt)}</span>
      </div>
      <div className="realtimeGrid">
        <SparkBars
          label="Отправлено"
          value={last.sent.toLocaleString()}
          testId="realtime-sent-last"
          tone="success"
          values={points.map((point) => point.sent)}
        />
        <SparkBars
          label="Сбои"
          value={last.failed.toLocaleString()}
          testId="realtime-failed-last"
          tone="danger"
          values={points.map((point) => point.failed)}
        />
        <SparkBars
          label="Очередь"
          value={snapshot.totals.queueDepth >= 0 ? snapshot.totals.queueDepth.toLocaleString() : "Нет данных"}
          tone="queue"
          values={points.map(() => Math.max(0, snapshot.totals.queueDepth))}
        />
        <SparkBars
          label="Успешность"
          value={formatRate(snapshot.totals.successRate)}
          tone="rate"
          values={points.map((point) => {
            const resolved = point.sent + point.failed;
            return resolved > 0 ? Math.round((point.sent / resolved) * 100) : 0;
          })}
        />
      </div>
    </section>
  );
}

function SparkBars({ label, value, values, tone, testId }: { label: string; value: string; values: number[]; tone: string; testId?: string }) {
  const max = Math.max(1, ...values);
  return (
    <article className={`sparkCard ${tone}`}>
      <div className="sparkTop">
        <span>{label}</span>
        <strong data-testid={testId}>{value}</strong>
      </div>
      <div className="sparkBars" aria-label={label}>
        {values.map((value, index) => (
          <i key={`${label}-${index}`} style={{ height: `${Math.max(4, Math.round((value / max) * 100))}%` }} />
        ))}
      </div>
    </article>
  );
}

function ServiceMailingMetrics({ snapshot }: { snapshot: StatsSnapshot }) {
  const { totals } = snapshot;
  const degradedChannels = snapshot.channels.filter((stat) => stat.successRate !== null && stat.successRate < 0.9).length;
  const queuedRatio = totals.messages > 0 ? totals.pending / totals.messages : null;
  const avgLatency = average(snapshot.channels.map((channel) => channel.averageLatencyMs).filter((value): value is number => value !== null));
  return (
    <section className="panel wide serviceMetricsPanel">
      <div className="panelHeader"><h2>Метрики рассылок и сервисов</h2><span>операционный срез</span></div>
      <div className="serviceMetricGrid">
        <Metric label="каналов ниже 90%" value={String(degradedChannels)} tone={degradedChannels > 0 ? "danger" : "success"} />
        <Metric label="доля очереди" value={formatRate(queuedRatio)} />
        <Metric label="failure rate" value={formatRate(totals.failedRate)} tone={totals.failedRate !== null && totals.failedRate > 0.05 ? "danger" : undefined} />
        <Metric label="avg delivery latency" value={avgLatency === null ? "Нет данных" : `${Math.round(avgLatency)} ms`} />
      </div>
    </section>
  );
}

function average(values: number[]) {
  if (values.length === 0) return null;
  return values.reduce((sum, value) => sum + value, 0) / values.length;
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
  const [workerStats, setWorkerStats] = useState<WorkerStats | null>(null);
  const [workerAction, setWorkerAction] = useState(false);
  const [workerMinBound, setWorkerMinBound] = useState(1);
  const [workerMaxBound, setWorkerMaxBound] = useState(1);
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

  useEffect(() => {
    function pollWorkerStats() {
      void fetchWorkerStats().then(setWorkerStats);
    }
    pollWorkerStats();
    const timer = window.setInterval(pollWorkerStats, 3000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    if (!workerStats) return;
    setWorkerMinBound(workerStats.minReplicas);
    setWorkerMaxBound(workerStats.maxReplicas);
  }, [workerStats?.minReplicas, workerStats?.maxReplicas]);

  async function saveWorkerBounds() {
    if (!workerStats || workerAction) return;
    const minReplicas = Math.max(1, Math.floor(workerMinBound));
    const maxReplicas = Math.max(1, Math.floor(workerMaxBound));
    if (minReplicas > maxReplicas) return;
    setWorkerAction(true);
    try {
      const updated = await updateWorkerBounds(minReplicas, maxReplicas);
      if (updated) setWorkerStats(updated);
    } finally {
      setWorkerAction(false);
    }
  }

  const workerCapacity = workerStats ? Math.max(1, workerStats.maxReplicas) : 1;
  const workerFill = workerStats ? Math.round((workerStats.replicas / workerCapacity) * 100) : 0;
  const canSaveWorkerBounds = Boolean(workerStats && workerStats.controlEnabled && !workerAction && workerMinBound >= 1 && workerMaxBound >= workerMinBound);
  const workerControlLabel = workerStats?.controlEnabled ? `${workerStats.autoscaler} · ${workerStats.controlMode}` : "только просмотр · CLI";
  const workerBoundsDirty = Boolean(workerStats && (workerMinBound !== workerStats.minReplicas || workerMaxBound !== workerStats.maxReplicas));
  const workerScaleDownPending = Boolean(workerStats && workerStats.replicas > workerStats.maxReplicas);

  return (
    <div className="pageGrid">
      <section className="panel wide">
        <div className="panelHeader"><h2>Пул воркеров отправки</h2></div>
        {workerStats ? (
          <div className="workerStatsGrid">
            <div className="workerStatCard workerStatMain">
              <span className="workerStatLabel">Контейнеров sender-worker</span>
              <strong className="workerStatValue">{workerStats.replicas}</strong>
              <span className="workerStatSub">{workerStats.replicas} готово · HPA цель {workerStats.desiredReplicas}</span>
              <div className="workerBar">
                <div className="workerBarFill" style={{ width: `${workerFill}%` }} />
              </div>
            </div>
            <div className="workerStatCard">
              <span className="workerStatLabel">Сообщений в очереди</span>
              <strong className="workerStatValue">{workerStats.queueDepth >= 0 ? workerStats.queueDepth.toLocaleString() : "—"}</strong>
              <span className="workerStatSub">message.send.request</span>
            </div>
            <div className="workerControls">
              <label>
                <span>Мин</span>
                <input aria-label="Минимум контейнеров" type="number" min={1} value={workerMinBound} onChange={(event) => setWorkerMinBound(Number(event.target.value))} disabled={!workerStats.controlEnabled || workerAction} />
              </label>
              <label>
                <span>Макс</span>
                <input aria-label="Максимум контейнеров" type="number" min={1} value={workerMaxBound} onChange={(event) => setWorkerMaxBound(Number(event.target.value))} disabled={!workerStats.controlEnabled || workerAction} />
              </label>
              <button onClick={() => void saveWorkerBounds()} disabled={!canSaveWorkerBounds}>Сохранить</button>
              <small>
                HPA сейчас: {workerStats.minReplicas}-{workerStats.maxReplicas} · {workerControlLabel}
                {workerBoundsDirty ? " · не сохранено" : ""}
                {workerScaleDownPending ? " · уменьшение в процессе" : ""}
              </small>
            </div>
          </div>
        ) : (
          <p style={{ padding: "1rem", color: "var(--muted)" }}>sender-worker недоступен</p>
        )}
      </section>
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
    actions: [
      { code: "retry", label: "Повторить" },
      { code: "switch_channel", label: "Сменить канал" },
      { code: "cancel_campaign", label: "Отменить" },
    ],
  };
}

function emptyActionableError(): ActionableError {
  return {
    title: "Нет активных ошибок",
    description: "",
    impact: "",
    actions: [
      { code: "retry", label: "Повторить" },
      { code: "switch_channel", label: "Сменить канал" },
      { code: "cancel_campaign", label: "Отменить" },
    ],
  };
}

function replaceCampaignErrorGroups(existing: ErrorGroup[], campaignId: string, groups: ErrorGroup[]) {
  return [
    ...groups.filter(isRealErrorGroup),
    ...existing.filter((group) => group.campaignId !== campaignId && isRealErrorGroup(group)),
  ];
}

function isRealErrorGroup(group: ErrorGroup) {
  return !(group.id === "max-stub-failed" && group.errorCode === "stub_failed" && group.errorMessage === "max stub failed");
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

const screenPaths: Record<Screen, string> = {
  dashboard: "/",
  campaigns: "/campaigns",
  create: "/create",
  templates: "/templates",
  channels: "/channels",
  deliveries: "/deliveries",
  stats: "/stats",
  managers: "/managers",
  health: "/health",
  logs: "/logs",
};

function pathForScreen(screen: Screen) {
  return screenPaths[screen];
}

function screenFromLocation(): Screen {
  if (typeof window === "undefined") return "dashboard";
  const normalizedPath = window.location.pathname.replace(/\/+$/, "") || "/";
  const match = (Object.entries(screenPaths) as [Screen, string][]).find(([, path]) => path === normalizedPath);
  return match?.[0] ?? "dashboard";
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
