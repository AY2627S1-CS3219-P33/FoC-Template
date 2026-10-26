# User Frontend

Responsive SPA for Friend on Campus. It is the single browser client for the
platform's services and holds no business logic of its own — each service
remains the source of truth for its own data and rules:

| Service | What the SPA does with it | Client |
| --- | --- | --- |
| **user-service** | Sign in, provision the local account, profile, admin user directory | `src/api/client.ts` |
| **supplier-service** | Browse and administer pickup locations; manage the building and supplier-type lists | `src/api/supplier.ts` |
| **order-service** | Request, browse, accept and complete errands; errand administration | `src/api/order.ts` |
| **credit-service** | Balance, reservations and ledger — **against a proposed contract**, see below | `src/api/credit.ts` |

Every client module mirrors its service's frozen OpenAPI contract. Searching,
filtering, sorting and paging are the services' job; the SPA sends query
parameters and renders what comes back rather than re-sorting pages locally.

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
| — | Admin user directory, search, role changes | `src/pages/admin/AdminUsersPage.tsx` |

Beyond the D1 wireframes, the shell now carries the other services' screens:

| Screen | What it covers | File |
| --- | --- | --- |
| Suppliers | Server-side search, type/building filters and sort; admin create, edit, delete | `src/pages/suppliers/SuppliersPage.tsx` |
| Buildings & types | Admin: add and retire the managed lookup lists | `src/pages/admin/LookupsPage.tsx` |
| Errands → Browse | Open errands, pickup filter, nearest-first ordering, accept | `src/pages/errands/OpenErrandList.tsx` |
| Errands → Requested / Delivering / History | The caller's own errands, with the deadline that matters in each state; history filters to My orders or My deliveries | `src/pages/errands/MyErrandList.tsx` |
| Errand detail | Role-aware lifecycle actions (edit, cancel, confirm, dispute, pick up, deliver, withdraw, report) | `src/pages/errands/ErrandDetail.tsx` |
| Request errand | Supplier picker, building dropdown, items, reward, contact, deadline | `src/pages/errands/ErrandForm.tsx` |
| Credits | Available / reserved / total, ledger and active holds | `src/pages/credit/CreditPage.tsx` |
| Errand admin | Reviews, settings, transition log, flags, failed outbox | `src/pages/admin/OrderAdminPage.tsx` |

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
| F2.x supplier catalogue, backend search/filter/sort | `SuppliersPage.tsx`, `api/supplier.ts` |
| F2.5.2 pickup resolved to the supplier's current version | pickup shown from the errand's stored snapshot |
| F3.1.1–F3.1.3 errand creation, reward, acceptance deadline | `ErrandForm.tsx`, `orderValidation.ts` |
| F3.1.2/F3.1.4 reservation outcome | `ErrandDetail.tsx` polls while `PENDING_RESERVATION` |
| F3.2.1–F3.2.4 open list, supplier filter, distance ordering | `OpenErrandList.tsx` |
| F3.3–F3.10 accept, pick up, deliver, withdraw, confirm, dispute, report | `ErrandDetail.tsx` |
| F3.4/F4.4 edit an open errand; reward edits wait for credit-service | `ErrandForm.tsx` (pending-edit banner) |
| F3.11–F3.13 requested, delivering and finished lists | `MyErrandList.tsx` |
| F6.1 review queue and resolution; F6.2 transition log; F6.3 flags and settings | `src/pages/admin/` |
| NFR3.6.3/NFR3.6.4 one-way contact, notes hidden from the open list | views are rendered as the service returns them |
| NFR7.3 idempotent errand submission | one `Idempotency-Key` per logical submission, reused on retry |
| NFR7.4 failed outbox replay | `FailedOutbox.tsx` |
| F3.3.7/NFR6.1.1 optimistic concurrency | every action sends `expectedVersion`; a stale one reloads the errand |

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

The Vite dev server proxies each service so browser calls stay same-origin and
the frontend never hard-codes a backend host:

| Path | Service | Dev target |
| --- | --- | --- |
| `/api`, `/health` | user-service | `http://localhost:8080` |
| `/supplier-service` | supplier-service | `http://localhost:3002` |
| `/order-service` | order-service | `http://localhost:3003` |
| `/credit-service` | credit-service | `http://localhost:3004` |

The gateway-route prefix is stripped before forwarding, because each Go service
serves its routes without it. Only the user-service is needed to sign in; the
other screens show a "could not reach" banner while their service is down, so
the app stays usable.

```sh
npm install
npm run dev          # http://localhost:5173
```

Each protected screen requests an access token for its own API audience and
permissions, overridable through the environment (see `.env.example`):

| Screen | Audience | Permissions requested |
| --- | --- | --- |
| Suppliers (read) | `VITE_SUPPLIER_AUDIENCE` | `suppliers:read` |
| Suppliers (admin), Buildings & types | `VITE_SUPPLIER_AUDIENCE` | `suppliers:read suppliers:manage` |
| Errands | `VITE_ORDER_AUDIENCE` | `errands:use` |
| Errand admin | `VITE_ORDER_AUDIENCE` | `errands:review` |
| Credits | `VITE_CREDIT_AUDIENCE` | `credits:read` |

### credit-service is not implemented

`credit-service/` contains no service and no agreed HTTP contract — credit
appears in the repository only as the RabbitMQ command/result contract in
`order-service/docs/contracts.md` and as the unimplemented
`service.CreditBalanceReader` interface in user-service.

So that the credit screens could be built now, `credit-service/openapi.yaml`
proposes the minimal read-only surface they need, derived from that message
contract, and `src/api/credit.ts` is its only client. **Both are provisional**:
whoever owns credit-service should amend or replace them, and only those two
files change when the real shapes land. The contract is read-only on purpose —
the credit economy is closed, and credits move only through order-service's
`credit.reserve` / `adjust` / `release` / `transfer` commands, never through a
client request. One question is flagged in that file for the owner: user-service
describes the balance as an exact decimal string, while order-service's reward
is an `int32`; the draft uses integers to match the frozen order contract.

Until the service exists, the Credits screen shows its unreachable banner and
falls back to the balance already on the profile.

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

### Admin user directory

Open `http://localhost:5173/admin` while signed in. The screen requests a
user-service token with the `users:manage:roles` permission, then uses the
existing admin endpoints for role lists, exact email search, promotion, and
demotion. The page waits for a permission-protected API response before showing
any directory or role controls. The existing Auth0 API and role setup must grant
`users:manage:roles` to the Administrator role.

## Scripts

- `npm run dev` — dev server with API proxy
- `npm run build` — production build to `dist/`
- `npm run preview` — serve the production build
- `npm run typecheck` — TypeScript check with no emit

## Structure

```text
src/
  api/                 One module per service, each mirroring its OpenAPI contract
    client.ts          user-service
    supplier.ts        supplier-service: suppliers + building/type lookups
    order.ts           order-service: errands, courier actions, administration
    credit.ts          credit-service (provisional — see above)
    types.ts           user-service response shapes
  auth/                Auth context and embedded Auth0 integration
  components/          AppShell (nav + header) and RoleBadge
  pages/
    auth/              LoginForm, CreateAccountForm, ResetPassword
    suppliers/         List (server-driven search/filter/sort), detail, form
    errands/           Tab shell, open list, my-errand lists, detail, form
      format.ts        Status labels/tones, deadlines, durations, credits
      messages.ts      Order error codes to actionable text
      usePagedList.ts  Cursor paging shared by every errand list
    credit/            Balance, ledger, active holds
    admin/             User directory, lookups, and the five errand-admin screens
  validation.ts        Profile + password validation (user-service)
  supplierValidation.ts Supplier field contract
  orderValidation.ts   Errand field contract, deadlines, datetime-local helpers
```

### A note on the lists

Supplier search, type and building filters and sort are all applied by
supplier-service (the D2 change): `SupplierList` is presentational and the page
sends `q`, `type`, `building` and `sort` as query parameters. Because a cursor
is only valid with the filters and sort that produced it, changing any of them
starts a fresh page sequence rather than appending to the old one.

Buildings and supplier types are managed lookup lists, not free text. The
supplier form and the errand form pick from them, and administrators add and
retire rows under **Buildings & types**. Retired rows stay visible on records
that already reference them — lookup names never change, so history stays
readable — but cannot be assigned to anything new.
