import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The user-service owns the API. In development the SPA runs on :5173 and proxies
// API and health routes to the Go service on :8080, so browser calls
// stay same-origin (no CORS) and the frontend never hard-codes the backend host.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/health": "http://localhost:8080",
      // Supplier service (documented gateway route /supplier-service). The Go
      // service serves routes without that prefix, so strip it when forwarding.
      "/supplier-service": {
        target: "http://localhost:3002",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/supplier-service/, ""),
      },
    },
  },
});
