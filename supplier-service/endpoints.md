# Supplier Service Endpoints & Architecture

This document summarizes the HTTP endpoints and component architecture implemented across Person B's commits according to [commit-plan.md](file:///e:/University/Y4S1/CS3219/FoC-Template/commit-plan.md#L211-L290), [contracts.md](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/docs/contracts.md), and [openapi.yaml](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/openapi.yaml).

---

## Table of Endpoints

| Commit | Scope / Feature | HTTP Method & Path | Primary Purpose |
| :--- | :--- | :--- | :--- |
| **B1** | `feat(supplier-auth)` | *N/A (HTTP Middleware)* | Token verification, permission policies, and context propagation |
| **B2** | `feat(supplier-create)` | `POST /suppliers` | Administrator creates a brand-new supplier record |
| **B3** | `feat(supplier-update)` | `PATCH /suppliers/{supplierId}` | Administrator partially updates supplier details, appending a new immutable version |
| **B4** | `feat(supplier-order-client)` | *N/A (Outbound HTTP Client)* | Order service client to inspect/acquire/commit deletion fence |
| **B5** | `feat(supplier-delete)` | `DELETE /suppliers/{supplierId}` | Administrator requests idempotent guarded soft-deletion |
| **B6** | `feat(supplier-delete)` | *N/A (Background Worker)* | Reconciles pending/interrupted deletion operations |

---


