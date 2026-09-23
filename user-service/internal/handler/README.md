# HTTP handlers — planned responsibilities

Documentation only; no routes or request/response models are implemented.

| Requirements | Future HTTP responsibility |
| --- | --- |
| F1.1, F1.1.1–F1.1.5 | Accept username, NUS email, password, and password confirmation; pass registration and email verification requests to the service. Return validation outcomes without exposing credentials. |
| F1.2, F1.2.1–F1.2.4 | Accept self-deletion with explicit confirmation and use the authenticated identity as the target; report blocked deletion outcomes. |
| F1.3, F1.3.1–F1.3.2 | Accept login credentials and Remember me preference; transport sessions and logout requests using the eventual session mechanism. |
| F1.4, F1.4.1–F1.4.3 | Return permitted profile fields and credit balance supplied by the service. Accept only display name and mobile number edits; reject attempts to edit system-managed fields. |
| F1.6.3, F1.6.6 | Expose administrator-management requests behind super-administrator access checks. |
| F1.7, F1.7.1 | Accept reset requests and confirmation links; do not allow a caller to choose an alternative delivery address. |
| F1.9, F1.9.1–F1.9.5 | Accept super-administrator invitations and activation/password-setting requests; derive the creator identity from authentication. |
| F1.10, F1.10.1–F1.10.7 | Accept deactivation, re-authentication/confirmation, and reactivation requests; derive the acting identity from authentication. |

Services remain authoritative for uniqueness, password rules, ownership, account state, and role restrictions. Forms, password-confirmation fields, Remember me controls, and deletion confirmation screens belong to the frontend; HTTP validation cannot replace business enforcement.

No errand creation, courier assignment, supplier management, or dispute endpoints belong in this package (F1.5, F1.6.1–F1.6.2).
