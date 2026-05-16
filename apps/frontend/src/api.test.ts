import { describe, expect, it } from "vitest";
import { api, campaignWebSocketURL, operationsWebSocketURL, serviceBase, serviceHealthTargets } from "./api";

describe("api routing", () => {
  it("uses frontend proxy paths for every service in dev and production", () => {
    expect(serviceBase("ops-gateway")).toBe("/api/ops-gateway");
    expect(api.ops).toBe("/api/ops-gateway");
    expect(api.campaigns).toBe("/api/campaign-service");
    expect(api.stats).toBe("/api/stats-service");
    expect(serviceHealthTargets.find((target) => target.id === "ops-gateway")?.url).toBe("/api/ops-gateway/health/ready");
  });

  it("builds websocket URLs on the current frontend origin", () => {
    expect(operationsWebSocketURL()).toBe("ws://localhost:3000/api/ops-gateway/ws/ops");
    expect(campaignWebSocketURL("cmp-1")).toBe("ws://localhost:3000/api/ops-gateway/ws/campaigns/cmp-1");
  });
});
