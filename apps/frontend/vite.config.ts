import { defineConfig } from "vite";
import { existsSync, readFileSync } from "node:fs";

type ServiceProxy = {
  name: string;
  port: number;
  ws?: boolean;
};

const services: ServiceProxy[] = [
  { name: "auth-service", port: 8081 },
  { name: "user-service", port: 8082 },
  { name: "template-service", port: 8083 },
  { name: "channel-service", port: 8084 },
  { name: "campaign-service", port: 8085 },
  { name: "dispatcher-service", port: 8086 },
  { name: "sender-worker", port: 8087 },
  { name: "notification-error-service", port: 8088 },
  { name: "ops-gateway", port: 8090, ws: true },
  { name: "stats-service", port: 8092 },
];

function k8sFrontendProxyTarget() {
  const configured = process.env.NORIFY_FRONTEND_PROXY_TARGET ?? process.env.VITE_API_PROXY_TARGET;
  if (configured) return configured;

  const portFile = "../../.cache/k8s-frontend.port-forward.port";
  if (!existsSync(portFile)) return "";

  const port = readFileSync(portFile, "utf8").trim();
  return port ? `http://localhost:${port}` : "";
}

function serviceProxy() {
  const frontendTarget = k8sFrontendProxyTarget();
  return Object.fromEntries(services.map(({ name, port, ws }) => {
    const path = `/api/${name}`;
    if (frontendTarget) {
      return [path, {
        target: frontendTarget,
        changeOrigin: true,
        ws: Boolean(ws),
      }];
    }
    return [path, {
      target: `http://localhost:${port}`,
      changeOrigin: true,
      ws: Boolean(ws),
      rewrite: (requestPath: string) => requestPath.replace(new RegExp(`^${path}`), ""),
    }];
  }));
}

export default defineConfig({
  server: {
    proxy: serviceProxy(),
  },
  test: {
    environment: "jsdom",
  },
});
