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
| F1.1 Auth0 account creation; F1.1.2/F1.1.5 browser checks (server policy required) | `CreateAccountForm.tsx`, `validation.ts` |
| F1.3 authenticate + session | `AuthContext.tsx`, `LoginForm.tsx` |
| F1.4.1 view username, email, role, credit balance | `Profile.tsx` (Account panel) |
| F1.4.2 update display name + mobile number | `Profile.tsx` (Edit form) |
| F1.4.3 protected fields not editable | role / credit / id shown read-only and locked |
| F1.6 role awareness | `RoleBadge.tsx` |
| NFR3.1.6 non-revealing auth errors | login + reset messages |
| NFR2 responsive UI | sidebar collapses to bottom nav below 52rem |

### Auth model

Production login and signup use **Auth0.js v10** from the existing forms:

- Login sends the entered email/password through `WebAuth.login()` using the
  configured database connection. Auth0 redirects back to `/`; `parseHash()`
  validates the transaction and extracts the tokens. The API audience still
  identifies the Go user service, not the Management API.
- Signup calls `WebAuth.signup()` (`POST /dbconnections/signup`). Only email,
  password, and profile name/nickname are sent; confirm-password stays in the
  browser. Successful signup shows a verification message and a button returning
  to login with the email filled in. It does not obtain tokens or provision a
  local account.
- After login, the frontend calls `/api/auth/provision` with the access token.
  The backend checks email verification and NUS eligibility for new local users.
- Password reset uses `WebAuth.changePassword()`; the confirmation does not
  disclose whether the account exists or promise a particular link lifetime.
- Access tokens and their expiry are held in memory, never stored in browser
  storage by this integration. Before protected operations, the context reuses a
  valid token or renews through `checkSession()` with a 30-second expiry buffer.
  A reload attempts restoration through Auth0's browser session. Restoration
  depends on cookie availability and may require signing in again.
- Logout clears local state and redirects to Auth0 logout. Issued access tokens
  remain valid until expiry. The hosted Auth0 login remains available as a
  fallback when embedded login cannot complete.
The username/display-name redesign is deferred. The signup label `Username`
currently maps to Auth0's profile nickname/name for compatibility with existing
provisioning. It is **not** an Auth0 login identifier and Auth0 does not enforce
its uniqueness; PostgreSQL may reject a conflict later during provisioning.
Production login therefore asks for email only.

The password checklist and NUS domain check are browser feedback, not enforcement
against direct API callers. Configure compatible Auth0 password policy and
server-side Auth0 registration rules if these must restrict Auth0 account creation.
The Go backend still enforces verified NUS email before new local provisioning.

### Required Auth0 Dashboard configuration

Use the existing **Single Page Application**, not the backend M2M application.
For local Vite development at `http://localhost:5173` configure:

- Allowed Callback URLs: `http://localhost:5173/`
- Allowed Logout URLs: `http://localhost:5173/`
- Allowed Web Origins: `http://localhost:5173`
- Allowed Origins (CORS): `http://localhost:5173`
- Allow Cross-Origin Authentication enabled, and the Implicit grant enabled
  for the `token id_token` response used by Auth0.js embedded login.
- RS256 ID-token signing (Auth0.js v10 rejects HS256 ID tokens).
- The database connection enabled for this SPA; **Disable Sign Ups off** for
  public signup. Email remains the login identifier.
- A working verification email template/provider for signup.

Preserve existing URLs (including `http://localhost:8080`) when adding these.
Use corresponding production origins/URLs when deploying. This change does not
modify tenant settings or use the backend's Management API credentials.

Modern browsers restrict third-party cookies. For reliable production embedded
login, use an Auth0 custom domain under the same parent domain as the app.
This standalone app does not render Auth0 Classic-hosted CAPTCHA widgets. If
Auth0 requires an interactive challenge, the embedded request can fail; the
user can continue on the hosted Auth0 page. Keep abuse protection enabled and
verify challenge behavior in the actual tenant before production use.

References: [Auth0.js](https://auth0.com/docs/libraries/auth0js),
[cross-origin authentication](https://auth0.com/docs/authenticate/login/cross-origin-authentication),
[Bot Detection](https://auth0.com/docs/secure/attack-protection/bot-detection).

## Running locally

The frontend expects the user-service running on `http://localhost:8080`. The Vite
dev server proxies `/api` and `/health` to it, so browser calls stay
same-origin.

```sh
npm install
npm run dev          # http://localhost:5173
```

The forms use real Auth0 credentials and require the tenant settings above. The
public connection name defaults to
`Username-Password-Authentication`; override it through `VITE_AUTH0_CONNECTION`
in the frontend environment if needed. Never put secrets in `VITE_` variables.

### HTTPS local testing with ngrok

Auth0 may require an interactive consent step when the SPA runs on `localhost`.
To test the supplier audience through an HTTPS origin, start the frontend and
then expose it with ngrok:

```sh
npm run dev
ngrok http 5173
```

Add the generated URL, for example `https://abc123.ngrok-free.app`, to the
Auth0 application's:

- Allowed Callback URLs: `https://abc123.ngrok-free.app/`
- Allowed Logout URLs: `https://abc123.ngrok-free.app/`
- Allowed Web Origins: `https://abc123.ngrok-free.app`

Open the generated HTTPS URL and sign in again. The Vite proxy still forwards
`/api` to the user-service and `/supplier-service` to the supplier-service.
The ngrok URL changes between sessions unless a reserved domain is used.

## Scripts

- `npm run dev` — dev server with API proxy
- `npm run build` — production build to `dist/`
- `npm run preview` — serve the production build
- `npm run typecheck` — TypeScript check with no emit

## Structure

```text
src/
  api/          HTTP client and response types (mirrors the service contract)
  auth/         Auth context and embedded Auth0 integration
  components/   AppShell (nav + header) and RoleBadge
  pages/        AuthScreen, Profile, Settings, Placeholder
    auth/       LoginForm, CreateAccountForm, ResetPassword
  validation.ts Client-side mirror of profile + password validation
```
