# User Frontend

Responsive SPA for the FoC **User Service**. It consumes the service's HTTP API to
sign a user in, provision their local account, and manage profile information. It
holds no business logic of its own — the user-service remains the source of truth.

Built with Vite + React + TypeScript.

## Screens

Built to the D1 presentation wireframes (User Service screens only):

| Wireframe | Screen | File |
| --- | --- | --- |
| slide 29 / 32 | Log in (tabbed) + error state | `src/pages/auth/LoginForm.tsx` |
| slide 30 | Create account (NUS email, password checklist, terms) | `src/pages/auth/CreateAccountForm.tsx` |
| slide 31 | Reset password + "check your inbox" | `src/pages/auth/ResetPassword.tsx` |
| slides 33–34 | App shell: sidebar / mobile bottom nav, credit header, user footer | `src/components/AppShell.tsx` |
| — | Profile (view + edit) | `src/pages/Profile.tsx` |

## Requirements implemented

| Requirement | Where |
| --- | --- |
| F1.1 create account, F1.1.2 password rules, F1.1.5 NUS email | `CreateAccountForm.tsx`, `validation.ts` |
| F1.3 authenticate + session, F1.3.2 "Remember me" | `AuthContext.tsx`, `LoginForm.tsx` |
| F1.4.1 view username, email, role, credit balance | `Profile.tsx` (Account panel) |
| F1.4.2 update display name + mobile number | `Profile.tsx` (Edit form) |
| F1.4.3 protected fields not editable | role / credit / id shown read-only and locked |
| F1.6 role awareness | `RoleBadge.tsx` |
| NFR3.1.6 non-revealing auth errors | login + reset messages |
| NFR2 responsive UI | sidebar collapses to bottom nav below 52rem |

### Auth model — important

The wireframes show username/password login, sign-up, and password reset. The
actual user-service is **Auth0-only** — Auth0 owns credentials, registration, and
reset; this service never sees a password. These screens therefore reproduce the
wireframe UI as the presentation layer, with the real auth seam behind them:

- **Production:** the login / create-account buttons hand off to Auth0 Universal
  Login (`signInAuth0`).
- **Local dev (`DEV_FAKE_AUTH=1`):** the forms mint a token from the service's
  mock issuer and provision the account, so the whole flow is demoable without an
  Auth0 tenant. The password field is accepted but not verified in this mode.

The password-strength checklist (F1.1.2) and NUS-email rule (F1.1.5) are enforced
client-side to match the wireframe; the wireframe's "7+ characters" label is
implemented as the required 8+ from F1.1.2.

## Running locally

The frontend expects the user-service running on `http://localhost:8080`. The Vite
dev server proxies `/api`, `/dev`, and `/health` to it, so browser calls stay
same-origin.

```sh
npm install
npm run dev          # http://localhost:5173
```

With the service in dev mode (`DEV_FAKE_AUTH=1`), sign in with any `@u.nus.edu`
email — no Auth0 account needed. Otherwise the login button starts the Auth0
Universal Login redirect, which requires a real tenant configured in the service.

## Scripts

- `npm run dev` — dev server with API proxy
- `npm run build` — production build to `dist/`
- `npm run preview` — serve the production build
- `npm run typecheck` — TypeScript check with no emit

## Structure

```text
src/
  api/          HTTP client and response types (mirrors the service contract)
  auth/         Auth context: Auth0 + dev-token sign-in, session persistence
  components/   AppShell (nav + header) and RoleBadge
  pages/        AuthScreen, Profile, Settings, Placeholder
    auth/       LoginForm, CreateAccountForm, ResetPassword
  validation.ts Client-side mirror of profile + password validation
```
