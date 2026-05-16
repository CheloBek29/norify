import { defineConfig } from "vite";

export default defineConfig({
  server: {
    proxy: {
      "/api/stats-service": {
        target: "http://localhost:8092",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api\/stats-service/, ""),
      },
    },
  },
  test: {
    environment: "jsdom",
  },
});
