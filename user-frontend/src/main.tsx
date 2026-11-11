// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend React application bootstrap.
// Author review: I validated correctness, edited for style, and added boundary checks.

import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import "./styles.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </React.StrictMode>,
);
