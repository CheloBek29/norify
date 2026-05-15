import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

class MockWebSocket {
  static instances: MockWebSocket[] = [];
  onopen: null | (() => void) = null;
  onmessage: null | ((event: { data: string }) => void) = null;
  onclose: null | (() => void) = null;
  readyState = 1;
  sent: string[] = [];
  url: string;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
    window.setTimeout(() => this.onopen?.(), 0);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    this.readyState = 3;
    this.onclose?.();
  }
}

function login() {
  fireEvent.click(screen.getByRole("button", { name: "Продолжить" }));
}

describe("App", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  beforeEach(() => {
    MockWebSocket.instances = [];
    const storage = new Map<string, string>();
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => storage.set(key, value),
        clear: () => storage.clear(),
      },
    });
    vi.restoreAllMocks();
    vi.stubGlobal("fetch", vi.fn(() => Promise.reject(new Error("backend_offline"))));
    vi.stubGlobal("WebSocket", MockWebSocket);
  });

  it("renders login screen", () => {
    render(<App />);
    expect(screen.getAllByText("Norify").length).toBeGreaterThan(0);
    expect(screen.getByText("Добро пожаловать в")).toBeTruthy();
    expect(screen.getByText("Вход в личный кабинет")).toBeTruthy();
    expect(screen.getByLabelText("Email")).toBeTruthy();
    expect(screen.getByLabelText("Пароль")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Голубая тема" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Розовая тема" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Зеленая тема" })).toBeTruthy();
    expect(screen.getByLabelText("Пользовательский цвет")).toBeTruthy();
  });

  it("persists the selected visual theme", () => {
    const { unmount } = render(<App />);
    const shell = screen.getByTestId("theme-root");

    expect(shell.getAttribute("data-theme")).toBe("sky");
    fireEvent.click(screen.getByRole("button", { name: "Розовая тема" }));
    expect(shell.getAttribute("data-theme")).toBe("pink");
    expect(window.localStorage.getItem("norify-theme")).toBe(JSON.stringify("pink"));

    unmount();
    render(<App />);
    expect(screen.getByTestId("theme-root").getAttribute("data-theme")).toBe("pink");
    fireEvent.change(screen.getByLabelText("Пользовательский цвет"), { target: { value: "#ff6a00" } });
    expect(screen.getByTestId("theme-root").getAttribute("data-theme")).toBe("custom");
    expect(window.localStorage.getItem("norify-custom-color")).toBe(JSON.stringify("#ff6a00"));
  });

  it("renders localized navigation after login", async () => {
    render(<App />);
    login();

    expect(await screen.findByRole("button", { name: "Панель" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Кампании" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Шаблоны" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Каналы" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Здоровье" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Новая кампания/i })).toBeTruthy();
  });

  it("shows realtime error groups and resolves one without stopping the campaign", async () => {
    render(<App />);
    login();

    expect(await screen.findByText("Группы ошибок")).toBeTruthy();
    expect(screen.getByText("Telegram adapter timeout")).toBeTruthy();
    expect(screen.getByLabelText(/Альтернативный канал/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: /Вставить/i })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /Вставить/i }));

    await waitFor(() => expect(screen.queryByText("Telegram adapter timeout")).toBeNull());
    expect(screen.getByText("Нет активных групп ошибок")).toBeTruthy();
    expect(screen.getByText(/Основная отправка продолжается/i)).toBeTruthy();
  });

  it("requires a different channel when switching a failed error group", async () => {
    window.localStorage.setItem("norify-error-groups", JSON.stringify([{
      id: "sms-failed",
      campaignId: "cmp-spring",
      channelCode: "sms",
      errorCode: "STUB_DELIVERY_FAILED",
      errorMessage: "sms stub failed",
      failedCount: 4,
      maxAttempt: 1,
      firstSeenAt: "2026-05-13T17:44:00Z",
      lastSeenAt: "2026-05-13T17:46:00Z",
      impact: "Затронуто 4 сообщений. Основная очередь продолжает обработку.",
      recommendedActions: [
        { code: "retry", label: "Повторить группу" },
        { code: "switch_channel", label: "Вставить через другой канал" },
        { code: "cancel_group", label: "Закрыть группу" },
      ],
    }]));

    render(<App />);
    login();

    const select = await screen.findByLabelText(/Альтернативный канал/i) as HTMLSelectElement;
    const options = within(select).getAllByRole("option").map((option) => (option as HTMLOptionElement).value);

    expect(screen.getByText("sms stub failed")).toBeTruthy();
    expect(options).not.toContain("sms");
    expect(options).toContain("email");
  });

  it("runs campaign actions against the clicked campaign row", async () => {
    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Кампании" }));

    const row = screen.getByRole("button", { name: "Админское уведомление" }).closest("tr");
    expect(row).toBeTruthy();
    expect(within(row as HTMLTableRowElement).getByText("created")).toBeTruthy();

    fireEvent.click(within(row as HTMLTableRowElement).getByRole("button", { name: /Старт/i }));

    await waitFor(() => expect(within(row as HTMLTableRowElement).getByText("running")).toBeTruthy());
  });

  it("archives campaigns without deleting them from local state", async () => {
    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Кампании" }));

    expect(await screen.findByRole("button", { name: "Весенняя реактивация" })).toBeTruthy();
    const row = screen.getByRole("button", { name: "Весенняя реактивация" }).closest("tr");
    expect(row).toBeTruthy();

    fireEvent.click(within(row as HTMLTableRowElement).getByRole("button", { name: /В архив/i }));

    await waitFor(() => expect(screen.queryByRole("button", { name: "Весенняя реактивация" })).toBeNull());
    expect(screen.getByText(/1 актив/i)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Показать архив" }));

    expect(await screen.findByRole("button", { name: "Весенняя реактивация" })).toBeTruthy();
    expect(screen.getByText("архив")).toBeTruthy();
  });

  it("sends campaign actions over websocket and stays on the current screen", async () => {
    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Кампании" }));

    const row = screen.getByRole("button", { name: "Админское уведомление" }).closest("tr");
    expect(row).toBeTruthy();
    fireEvent.click(within(row as HTMLTableRowElement).getByRole("button", { name: /Старт/i }));

    await waitFor(() => {
      const opsSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/ops"));
      const messages = opsSocket?.sent.map((item) => JSON.parse(item));
      expect(messages?.some((message) => message.type === "campaign.action" && message.payload.action === "start")).toBe(true);
    });
    expect(screen.getByRole("heading", { name: "Кампании" })).toBeTruthy();
    await waitFor(() => expect(within(row as HTMLTableRowElement).getByText("running")).toBeTruthy());
  });

  it("starts a created campaign immediately and opens the dashboard", async () => {
    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Создать" }));

    fireEvent.change(screen.getByLabelText("Название"), { target: { value: "Моментальный запуск" } });
    fireEvent.click(screen.getByRole("button", { name: /Запустить кампанию/i }));

    expect(await screen.findByRole("heading", { name: "Панель управления" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Моментальный запуск" })).toBeTruthy();
    expect(screen.getByText("running")).toBeTruthy();
    expect(screen.getByText("0 / 115,920")).toBeTruthy();
    await waitFor(() => {
      const opsSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/ops"));
      const messages = opsSocket?.sent.map((item) => JSON.parse(item));
      expect(messages?.some((message) => message.type === "campaign.create" && message.payload.name === "Моментальный запуск")).toBe(true);
    });
  });

  it("replaces the optimistic campaign with the started backend campaign", async () => {
    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Создать" }));

    fireEvent.change(screen.getByLabelText("Название"), { target: { value: "Backend запуск" } });
    fireEvent.click(screen.getByRole("button", { name: /Запустить кампанию/i }));

    const opsSocket = await waitFor(() => {
      const socket = MockWebSocket.instances.find((item) => item.url.includes("/ws/ops") && item.sent.length > 0);
      expect(socket).toBeTruthy();
      return socket as MockWebSocket;
    });
    const request = JSON.parse(opsSocket.sent[0]);
    await act(async () => {
      opsSocket.onmessage?.({ data: JSON.stringify({
        type: "campaign.upsert",
        request_id: request.id,
        campaign: {
          id: "cmp-backend-started",
          name: "Backend запуск",
          template_id: "tpl-reactivation",
          template_name: "Реактивация клиента",
          status: "running",
          filters: {},
          selected_channels: ["email", "sms", "telegram"],
          total_recipients: 50000,
          total_messages: 150000,
          sent_count: 4,
          success_count: 4,
          failed_count: 0,
          cancelled_count: 0,
          p95_dispatch_ms: 25,
          created_at: "2026-05-15T12:00:00Z",
          started_at: "2026-05-15T12:00:01Z",
        },
      }) });
    });

    expect(await screen.findByRole("heading", { name: "Панель управления" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Backend запуск" })).toBeTruthy();
    expect(screen.getByText("4 / 150,000")).toBeTruthy();
    expect(screen.queryAllByRole("heading", { name: "Backend запуск" })).toHaveLength(1);
  });

  it("starts two new campaigns without queueing template save between create commands", async () => {
    render(<App />);
    login();

    fireEvent.click(await screen.findByRole("button", { name: "Создать" }));
    fireEvent.change(screen.getByLabelText("Название"), { target: { value: "Первый запуск" } });
    fireEvent.click(screen.getByRole("button", { name: /Запустить кампанию/i }));

    const opsSocket = await waitFor(() => {
      const socket = MockWebSocket.instances.find((item) => item.url.includes("/ws/ops") && item.sent.length > 0);
      expect(socket).toBeTruthy();
      return socket as MockWebSocket;
    });
    const firstRequest = JSON.parse(opsSocket.sent[0]);
    await act(async () => {
      opsSocket.onmessage?.({ data: JSON.stringify({
        type: "campaign.upsert",
        request_id: firstRequest.id,
        campaign: {
          id: "cmp-first-backend",
          name: "Первый запуск",
          template_id: "tpl-reactivation",
          template_name: "Реактивация клиента",
          status: "running",
          filters: {},
          selected_channels: ["email", "sms", "telegram"],
          total_recipients: 38640,
          total_messages: 115920,
          sent_count: 0,
          success_count: 0,
          failed_count: 0,
          cancelled_count: 0,
          created_at: "2026-05-15T12:00:00Z",
        },
      }) });
    });

    fireEvent.click(screen.getByRole("button", { name: /Новая кампания/i }));
    fireEvent.change(screen.getByLabelText("Название"), { target: { value: "Второй запуск" } });
    fireEvent.click(screen.getByRole("button", { name: /Запустить кампанию/i }));

    await waitFor(() => {
      const sentTypes = opsSocket.sent.map((item) => JSON.parse(item).type);
      expect(sentTypes.slice(0, 2)).toEqual(["campaign.create", "campaign.create"]);
    });
  });

  it("renders player-style campaign controls and resumes after stop", async () => {
    render(<App />);
    login();

    const controls = await screen.findByLabelText("Campaign player controls");
    expect(within(controls).getByRole("button", { name: /Запустить/i })).toBeTruthy();
    expect(within(controls).getByRole("button", { name: /Остановить/i })).toBeTruthy();
    expect(within(controls).getByRole("button", { name: /Отменить/i })).toBeTruthy();
    expect((within(controls).getByRole("button", { name: /Запустить/i }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Telegram adapter timeout")).toBeTruthy();

    fireEvent.click(within(controls).getByRole("button", { name: /Остановить/i }));

    await waitFor(() => expect(screen.getByText("stopped")).toBeTruthy());
    expect(within(controls).getByRole("button", { name: /Продолжить/i })).toBeTruthy();
    expect(screen.getByText("Telegram adapter timeout")).toBeTruthy();
    expect(screen.getByText("5,120 / 150,000")).toBeTruthy();

    const statusSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/campaigns/cmp-spring"));
    expect(statusSocket).toBeTruthy();
    await act(async () => {
      statusSocket?.onmessage?.({ data: JSON.stringify({
        type: "campaign.progress",
        campaign_id: "cmp-spring",
        status: "running",
        total_messages: 0,
        processed: 0,
        success: 0,
        failed: 0,
        cancelled: 0,
      }) });
    });
    expect(screen.getByText("stopped")).toBeTruthy();
    expect(screen.getByText("5,120 / 150,000")).toBeTruthy();

    await act(async () => {
      statusSocket?.onmessage?.({ data: JSON.stringify({
        type: "campaign.progress",
        campaign_id: "cmp-spring",
        status: "running",
        total_messages: 150000,
        processed: 5120,
        success: 4781,
        failed: 339,
        cancelled: 0,
        p95_dispatch_ms: 3752,
      }) });
    });
    expect(screen.getByText("3752 ms")).toBeTruthy();

    await act(async () => {
      statusSocket?.onmessage?.({ data: JSON.stringify({
        type: "campaign.progress",
        campaign_id: "cmp-spring",
        status: "running",
        total_messages: 150000,
        processed: 90000,
        success: 89000,
        failed: 1000,
        cancelled: 0,
      }) });
    });

    expect(screen.getByText("stopped")).toBeTruthy();
    expect(screen.queryByText("90,000 / 150,000")).toBeNull();
    expect(screen.getByText("5,120 / 150,000")).toBeTruthy();

    fireEvent.click(within(controls).getByRole("button", { name: /Продолжить/i }));

    await waitFor(() => expect(screen.getByText("running")).toBeTruthy());
    expect(screen.getByText("5,120 / 150,000")).toBeTruthy();
    await waitFor(() => {
      const opsSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/ops"));
      const messages = opsSocket?.sent.map((item) => JSON.parse(item));
      expect(messages?.some((message) => message.type === "campaign.action" && message.payload.action === "stop")).toBe(true);
      expect(messages?.some((message) => message.type === "campaign.action" && message.payload.action === "start")).toBe(true);
    });
  });

  it("does not reset running campaign counters from an empty realtime snapshot", async () => {
    render(<App />);
    login();

    expect(await screen.findByText("5,120 / 150,000")).toBeTruthy();
    const statusSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/campaigns/cmp-spring"));
    expect(statusSocket).toBeTruthy();

    await act(async () => {
      statusSocket?.onmessage?.({ data: JSON.stringify({
        type: "campaign.progress",
        campaign_id: "cmp-spring",
        status: "running",
        total_messages: 0,
        processed: 0,
        success: 0,
        failed: 0,
        cancelled: 0,
      }) });
    });

    expect(screen.getByText("running")).toBeTruthy();
    expect(screen.getByText("5,120 / 150,000")).toBeTruthy();
  });

  it("recovers displayed total messages from recipients and channels when stored progress total is zero", async () => {
    window.localStorage.setItem("norify-campaigns", JSON.stringify([{
      id: "cmp-corrupt-progress",
      name: "Новая кампания 3",
      templateId: "tpl-reactivation",
      templateName: "Реактивация клиента",
      status: "running",
      filters: {},
      selectedChannels: ["email", "sms", "telegram", "whatsapp", "vk", "max"],
      totalRecipients: 38640,
      totalMessages: 0,
      processed: 117,
      success: 106,
      failed: 11,
      cancelled: 0,
      p95DispatchMs: 0,
      createdAt: "2026-05-15T12:00:00Z",
    }]));

    render(<App />);
    login();

    expect(await screen.findByText("117 / 231,840")).toBeTruthy();
    expect(screen.getByText("231,723")).toBeTruthy();
  });

  it("shows real microservice health checks instead of static statuses", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("8082/health/ready")) return Promise.reject(new Error("users_down"));
      if (url.includes("/health/ready")) return Promise.resolve(new Response(JSON.stringify({ status: "ready" }), { status: 200 }));
      return Promise.reject(new Error("backend_offline"));
    }));

    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Здоровье" }));

    await waitFor(() => {
      const opsSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/ops"));
      const messages = opsSocket?.sent.map((item) => JSON.parse(item));
      expect(messages?.some((message) => message.type === "health.check")).toBe(true);
    });
    const opsSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/ops"));
    opsSocket?.onmessage?.({ data: JSON.stringify({
      type: "health.snapshot",
      services: [
        { id: "auth-service", name: "auth-service", url: "http://localhost:8081/health/ready", status: "ready", latency_ms: 12, checked_at: new Date().toISOString(), detail: "ready" },
        { id: "user-service", name: "user-service", url: "http://localhost:8082/health/ready", status: "down", latency_ms: 1800, checked_at: new Date().toISOString(), detail: "users_down" },
      ],
    }) });
    expect((await screen.findAllByText("auth-service")).length).toBeGreaterThan(0);
    await waitFor(() => expect(screen.getAllByText("user-service").length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getAllByText("down").length).toBeGreaterThan(0));
    expect(screen.getAllByText("ready").length).toBeGreaterThan(0);
  });

  it("does not show a fake zero p95 before dispatch metrics arrive", async () => {
    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Кампании" }));

    const row = screen.getByRole("button", { name: "Админское уведомление" }).closest("tr");

    expect(row).toBeTruthy();
    expect(within(row as HTMLTableRowElement).queryByText("0 ms")).toBeNull();
    expect(within(row as HTMLTableRowElement).getByText("pending")).toBeTruthy();
  });

  it("labels dispatch p95 as queue enqueue latency and shows sub-millisecond values clearly", async () => {
    window.localStorage.setItem("norify-campaigns", JSON.stringify([{
      id: "cmp-fast-enqueue",
      name: "Быстрая постановка",
      templateId: "tpl-service",
      templateName: "Сервисное уведомление",
      status: "running",
      filters: {},
      selectedChannels: ["email"],
      totalRecipients: 10,
      totalMessages: 10,
      processed: 10,
      success: 10,
      failed: 0,
      cancelled: 0,
      p95DispatchMs: 1,
      createdAt: "2026-05-15T12:00:00Z",
      startedAt: "2026-05-15T12:00:00Z",
    }]));

    render(<App />);
    login();

    expect(await screen.findByText("p95 enqueue")).toBeTruthy();
    expect(screen.getByText("<1 ms")).toBeTruthy();
  });

  it("renders channel cards from delivery statistics instead of configured probability", async () => {
    window.localStorage.setItem("norify-channels", JSON.stringify([{
      code: "email",
      name: "Email",
      enabled: true,
      successProbability: 0.5,
      minDelaySeconds: 2,
      maxDelaySeconds: 60,
      maxParallelism: 180,
      retryLimit: 5,
      deliveryTotal: 10,
      deliverySent: 7,
      deliveryFailed: 3,
      deliveryQueued: 0,
      deliverySuccessRate: 0.7,
      averageAttempt: 1.7,
    }]));

    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Каналы" }));

    await screen.findByText("Email");
    const emailCard = screen.getByText("70%").closest("article");
    expect(emailCard).toBeTruthy();
    expect(within(emailCard as HTMLElement).getByText("Успех доставки")).toBeTruthy();
    expect(within(emailCard as HTMLElement).getByText("70%")).toBeTruthy();
    expect(within(emailCard as HTMLElement).getByText("Всего")).toBeTruthy();
    expect(within(emailCard as HTMLElement).getByText("10")).toBeTruthy();
    expect(within(emailCard as HTMLElement).getByText("1.7")).toBeTruthy();
    expect(within(emailCard as HTMLElement).getByText("Порог успеха")).toBeTruthy();
    expect(within(emailCard as HTMLElement).getAllByText("50%").length).toBeGreaterThan(0);
  });

  it("keeps the channel registry readable when delivery statistics are missing", async () => {
    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Каналы" }));

    expect(await screen.findByText("Email")).toBeTruthy();
    expect(screen.queryAllByText("no data")).toHaveLength(0);
    expect(screen.getAllByText("Нет данных").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Порог успеха").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Всего").length).toBeGreaterThan(0);
  });

  it("renders templates as a composer with preview and variable validation", async () => {
    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Шаблоны" }));

    expect(await screen.findByRole("heading", { name: "Редактор шаблона" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Библиотека шаблонов" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Живой предпросмотр" })).toBeTruthy();
    expect(screen.getByText(/Здравствуйте, Анна/i)).toBeTruthy();
    expect(screen.getAllByText("first_name").length).toBeGreaterThan(0);

    fireEvent.change(screen.getByLabelText("Текст сообщения"), { target: { value: "Ваш код {{promo_code}}" } });

    expect((await screen.findAllByText("promo_code")).length).toBeGreaterThan(0);
    expect(screen.getByText(/Не объявлены: promo_code/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: /Сохранить версию/i }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("inserts template variables from PostgreSQL user columns", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/auth/login")) return Promise.reject(new Error("backend_offline"));
      if (url.endsWith("/campaigns") || url.endsWith("/templates") || url.endsWith("/channels")) {
        return Promise.resolve(new Response(JSON.stringify([]), { status: 200 }));
      }
      if (url.endsWith("/templates/variables")) {
        return Promise.resolve(new Response(JSON.stringify([
          { name: "email", type: "text", source: "users" },
          { name: "phone", type: "text", source: "users" },
        ]), { status: 200 }));
      }
      return Promise.reject(new Error("backend_offline"));
    }));

    render(<App />);
    login();
    fireEvent.click(await screen.findByRole("button", { name: "Шаблоны" }));

    expect(await screen.findByText("Колонки PostgreSQL")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /email/i }));

    expect((screen.getByLabelText("Текст сообщения") as HTMLTextAreaElement).value).toContain("{{email}}");
    expect((screen.getByLabelText("Объявленные переменные") as HTMLInputElement).value).toContain("email");
  });

  it("applies the polished form surface to every editable workflow", async () => {
    render(<App />);
    login();

    fireEvent.click(await screen.findByRole("button", { name: "Создать" }));
    const campaignPanel = screen.getByRole("heading", { name: "Кампания" }).closest("section");
    const audiencePanel = screen.getByRole("heading", { name: "Аудитория" }).closest("section");
    expect(campaignPanel?.classList.contains("formPanel")).toBe(true);
    expect(audiencePanel?.classList.contains("formPanel")).toBe(true);
    expect(campaignPanel?.querySelector(".formStack")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Шаблоны" }));
    const templateLibrary = screen.getByRole("heading", { name: "Библиотека шаблонов" }).closest("section");
    const templateEditor = screen.getByRole("heading", { name: "Редактор шаблона" }).closest("section");
    expect(templateLibrary?.classList.contains("formPanel")).toBe(true);
    expect(templateEditor?.classList.contains("formPanel")).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Менеджеры" }));
    const managerPanel = screen.getByRole("heading", { name: "Добавить менеджера" }).closest("section");
    expect(managerPanel?.classList.contains("formPanel")).toBe(true);
    expect(managerPanel?.querySelector(".formActions")).toBeTruthy();
  });
});
