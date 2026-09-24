# HTTP handlers — planned responsibilities

The package serves the embedded Auth0 test page, exposes public health/browser configuration, and protects provisioning and test API routes with Auth0 JWT validation. Provisioning accepts identity only from validated token context and Auth0 `/userinfo`, never browser-submitted profile fields.

| Requirements | Future HTTP responsibility |
| --- | --- |
| F1.1, F1.1.1–F1.1.5 | Redirect registration to Auth0 Universal Login and provision only a matching, verified `u.nus.edu` identity. |
| F1.2, F1.2.1–F1.2.4 | Accept self-deletion with explicit confirmation and use the authenticated identity as the target; report blocked deletion outcomes. |
| F1.3, F1.3.1–F1.3.2 | Use the Auth0 SPA SDK for login, logout, and session continuity; never accept credentials locally. |
| F1.4, F1.4.1–F1.4.3 | Return permitted profile fields and credit balance supplied by the service. Accept only display name and mobile number edits; reject attempts to edit system-managed fields. |
| F1.6.3, F1.6.6 | Expose administrator-management requests behind super-administrator access checks. |
| F1.7, F1.7.1 | Use Auth0's password-reset flow for the registered identity. |
| F1.9, F1.9.1–F1.9.5 | Accept super-administrator invitations and activation requests; derive the creator identity from authentication. |
| F1.10, F1.10.1–F1.10.7 | Accept deactivation, re-authentication/confirmation, and reactivation requests; derive the acting identity from authentication. |

Services remain authoritative for local uniqueness, ownership, account state, and role restrictions. Auth0 remains authoritative for credentials, password rules, and sessions.

No errand creation, courier assignment, supplier management, or dispute endpoints belong in this package (F1.5, F1.6.1–F1.6.2).
