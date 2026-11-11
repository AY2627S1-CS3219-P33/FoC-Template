// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Vite development and build configuration for user-frontend.
// Author review: I validated correctness, edited for style, and added boundary checks.

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
      // Order service (documented gateway route /order-service), same
      // prefix-stripping arrangement as the supplier service.
      "/order-service": {
        target: "http://localhost:3003",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/order-service/, ""),
      },
      // Admin service (documented gateway route /admin-service): the Metrics
      // tab and the readiness section of Overview. Same prefix-stripping.
      "/admin-service": {
        target: "http://localhost:3005",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/admin-service/, ""),
      },
      // Credit service. No implementation exists yet, so this forwards nowhere
      // until one is running; the credit screens then show a reachability
      // error rather than failing silently.
      "/credit-service": {
        target: "http://localhost:3004",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/credit-service/, ""),
      },
      // Notification service (documented gateway route /notification-service),
      // same prefix-stripping arrangement. Forwards nowhere until the service
      // is running on :3007, when the notification screens light up.
      "/notification-service": {
        target: "http://localhost:3007",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/notification-service/, ""),
      },
      // Chat service: REST plus the WebSocket at /chat-service/ws. The browser's
      // Origin (localhost:5173) is passed through unchanged, and chat-service
      // allows it by default (WS_ALLOWED_ORIGINS).
      "/chat-service": {
        target: "http://localhost:3006",
        changeOrigin: true,
        ws: true,
        rewrite: (path) => path.replace(/^\/chat-service/, ""),
      },
    },
    allowedHosts: ["russell-unjumbled-unputatively.ngrok-free.dev"],
  },
});
