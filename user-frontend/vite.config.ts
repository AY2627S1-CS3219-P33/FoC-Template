import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The user-service owns the API. In development the SPA runs on :5173 and proxies
// API, dev-auth, and health routes to the Go service on :8080, so browser calls
// stay same-origin (no CORS) and the frontend never hard-codes the backend host.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/dev": "http://localhost:8080",
      "/health": "http://localhost:8080",
    },
  },
});
