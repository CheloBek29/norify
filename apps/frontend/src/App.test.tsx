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
    expect(screen.getByText("Norify")).toBeTruthy();
    expect(screen.getByText("Вход в кабинет")).toBeTruthy();
  });

  it("shows realtime error groups and resolves one without stopping the campaign", async () => {
    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));

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
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));

    const select = await screen.findByLabelText(/Альтернативный канал/i) as HTMLSelectElement;
    const options = within(select).getAllByRole("option").map((option) => (option as HTMLOptionElement).value);

    expect(screen.getByText("sms stub failed")).toBeTruthy();
    expect(options).not.toContain("sms");
    expect(options).toContain("email");
  });

  it("runs campaign actions against the clicked campaign row", async () => {
    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Campaigns" }));

    const row = screen.getByRole("button", { name: "Админское уведомление" }).closest("tr");
    expect(row).toBeTruthy();
    expect(within(row as HTMLTableRowElement).getByText("created")).toBeTruthy();

    fireEvent.click(within(row as HTMLTableRowElement).getByRole("button", { name: /Start/i }));

    await waitFor(() => expect(within(row as HTMLTableRowElement).getByText("running")).toBeTruthy());
  });

  it("sends campaign actions over websocket and stays on the current screen", async () => {
    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Campaigns" }));

    const row = screen.getByRole("button", { name: "Админское уведомление" }).closest("tr");
    expect(row).toBeTruthy();
    fireEvent.click(within(row as HTMLTableRowElement).getByRole("button", { name: /Start/i }));

    await waitFor(() => {
      const opsSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/ops"));
      const messages = opsSocket?.sent.map((item) => JSON.parse(item));
      expect(messages?.some((message) => message.type === "campaign.action" && message.payload.action === "start")).toBe(true);
    });
    expect(screen.getByRole("heading", { name: "Campaigns" })).toBeTruthy();
    await waitFor(() => expect(within(row as HTMLTableRowElement).getByText("running")).toBeTruthy());
  });

  it("renders player-style campaign controls and resumes after stop", async () => {
    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));

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

    const statusSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/campaigns/cmp-spring"));
    expect(statusSocket).toBeTruthy();
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
    await waitFor(() => {
      const opsSocket = MockWebSocket.instances.find((socket) => socket.url.includes("/ws/ops"));
      const messages = opsSocket?.sent.map((item) => JSON.parse(item));
      expect(messages?.some((message) => message.type === "campaign.action" && message.payload.action === "stop")).toBe(true);
      expect(messages?.some((message) => message.type === "campaign.action" && message.payload.action === "start")).toBe(true);
    });
  });

  it("shows real microservice health checks instead of static statuses", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("8082/health/ready")) return Promise.reject(new Error("users_down"));
      if (url.includes("/health/ready")) return Promise.resolve(new Response(JSON.stringify({ status: "ready" }), { status: 200 }));
      return Promise.reject(new Error("backend_offline"));
    }));

    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Health" }));

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
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Campaigns" }));

    const row = screen.getByRole("button", { name: "Админское уведомление" }).closest("tr");

    expect(row).toBeTruthy();
    expect(within(row as HTMLTableRowElement).queryByText("0 ms")).toBeNull();
    expect(within(row as HTMLTableRowElement).getByText("pending")).toBeTruthy();
  });

  it("renders channel cards from delivery statistics instead of configured probability", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/auth/login")) return Promise.reject(new Error("backend_offline"));
      if (url.endsWith("/campaigns") || url.endsWith("/templates")) {
        return Promise.resolve(new Response(JSON.stringify([]), { status: 200 }));
      }
      if (url.endsWith("/channels")) {
        return Promise.resolve(new Response(JSON.stringify([{
          code: "email",
          name: "Email",
          enabled: true,
          success_probability: 0.5,
          min_delay_seconds: 2,
          max_delay_seconds: 60,
          max_parallelism: 180,
          retry_limit: 5,
          delivery_total: 10,
          delivery_sent: 7,
          delivery_failed: 3,
          delivery_queued: 0,
          delivery_success_rate: 0.7,
          average_attempt: 1.7,
        }]), { status: 200 }));
      }
      return Promise.reject(new Error("backend_offline"));
    }));

    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Channels" }));

    expect(await screen.findByText("Email")).toBeTruthy();
    expect(screen.getByText("70%")).toBeTruthy();
    expect(screen.getByText("10 total")).toBeTruthy();
    expect(screen.getByText("1.7")).toBeTruthy();
    expect(screen.queryByText("50%")).toBeNull();
  });

  it("renders templates as a composer with preview and variable validation", async () => {
    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: /Login/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Templates" }));

    expect(await screen.findByRole("heading", { name: "Template composer" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Template library" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Live preview" })).toBeTruthy();
    expect(screen.getByText(/Здравствуйте, Анна/i)).toBeTruthy();
    expect(screen.getAllByText("first_name").length).toBeGreaterThan(0);

    fireEvent.change(screen.getByLabelText("Message body"), { target: { value: "Ваш код {{promo_code}}" } });

    expect((await screen.findAllByText("promo_code")).length).toBeGreaterThan(0);
    expect(screen.getByText(/Не объявлены: promo_code/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: /Save version/i }) as HTMLButtonElement).disabled).toBe(true);
  });
});
