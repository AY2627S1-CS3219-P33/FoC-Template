# Supplier Service Endpoints & Architecture (Person B)

This document summarizes the HTTP endpoints and component architecture implemented across Person B's commits according to [commit-plan.md](file:///e:/University/Y4S1/CS3219/FoC-Template/commit-plan.md#L211-L290), [contracts.md](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/docs/contracts.md), and [openapi.yaml](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/openapi.yaml).

---

## Person B Component & Endpoint Roadmap

| Commit | Scope / Feature | HTTP Method & Path | Primary Purpose |
| :--- | :--- | :--- | :--- |
| **B1** | `feat(supplier-auth)` | *N/A (HTTP Middleware)* | Token verification, permission policies, and context propagation |
| **B2** | `feat(supplier-create)` | `POST /suppliers` | Administrator creates a brand-new supplier record |
| **B3** | `feat(supplier-update)` | `PATCH /suppliers/{supplierId}` | Administrator partially updates supplier details, appending a new immutable version |
| **B4** | `feat(supplier-order-client)` | *N/A (Outbound HTTP Client)* | Order service client to inspect/acquire/commit deletion fence |
| **B5** | `feat(supplier-delete)` | `DELETE /suppliers/{supplierId}` | Administrator requests idempotent guarded soft-deletion |
| **B6** | `feat(supplier-delete)` | *N/A (Background Worker)* | Reconciles pending/interrupted deletion operations |

---

## Detailed Component Specifications

### B1: Authentication & Authorization Middleware
* **Endpoint**: None (No explicit URL/route).
* **Role**: HTTP request pipeline interceptor.
* **Mechanism**:
  1. Intercepts incoming HTTP requests destined for protected endpoints.
  2. Parses the `Authorization: Bearer <token>` header.
  3. Verifies credentials via [`auth.Port`](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/internal/auth/port.go).
  4. Enforces RBAC permissions ([`RequireRead`](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/internal/auth/middleware.go#L101-L104) or [`RequireManage`](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/internal/auth/middleware.go#L106-L109)).
  5. If rejected, short-circuits immediately with `401 Unauthorized` or `403 Forbidden`.
  6. If authorized, attaches the trusted [`auth.Principal`](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/internal/auth/port.go#L37-L46) to the request context via [`auth.WithPrincipal`](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/internal/auth/context.go#L10-L12) and invokes `next.ServeHTTP(w, r)`.

### B2: Administrator Supplier Creation
* **Endpoint**: `POST /suppliers`
* **Handler**: [`create.Handler`](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/internal/supplier/create/handler.go)
* **Access**: Authenticated administrator with `ManageSuppliers` permission.
* **Payload**: Full supplier attributes (`name`, `type`, `building`, `floor`, `locationDescription`, `latitude`, `longitude`, `openingTime`, `closingTime`, and optional `imageUrl`).
* **Response**: `201 Created` with header `Location: /suppliers/{supplierId}` and JSON body containing generated `supplierId` and initial `versionId`.

### B3: Administrator Supplier Update & Versioning
* **Endpoint**: `PATCH /suppliers/{supplierId}`
* **Access**: Authenticated administrator with `ManageSuppliers` permission.
* **Payload**: Partial update fields (non-empty patch; prohibits `supplierId` and `versionId`).
* **Response**: `200 OK` with JSON body containing the updated supplier projection with new `versionId`. Appends an immutable snapshot in `supplier_versions` without mutating or deleting previous versions.

### B4: Deletion-Fence Order Client
* **Endpoint**: None (Outbound HTTP client integration with Order Service).
* **Role**: Interacts with Order Service endpoints (`Acquire`, `Inspect`, `Commit`, `Release`) over TLS to ensure no active errands exist before deletion.

### B5 & B6: Guarded Supplier Deletion & Reconciliation
* **Endpoint**: `DELETE /suppliers/{supplierId}`
* **Access**: Authenticated administrator with `ManageSuppliers` permission.
* **Response**:
  * `204 No Content` if fence committed and supplier soft-deleted.
  * `202 Accepted` if soft-deleted but fence commit confirmation is pending.
  * `409 Conflict` if active errands prevent deletion.
  * Preserves all historical immutable versions in `supplier_versions`.

---

## Full Supplier Service API Overview (Person A & Person B)

For reference, the complete OpenAPI contract ([openapi.yaml](file:///e:/University/Y4S1/CS3219/FoC-Template/supplier-service/openapi.yaml)) includes:

| Method | Path | Owner | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/readyz` | Shared | Service readiness probe (unauthenticated) |
| `GET` | `/suppliers` | Person A | List & search active suppliers (authenticated read) |
| `POST` | `/suppliers` | **Person B (B2)** | Create new supplier (administrator write) |
| `GET` | `/suppliers/{supplierId}` | Person A | Retrieve current supplier details (authenticated read) |
| `PATCH` | `/suppliers/{supplierId}` | **Person B (B3)** | Update supplier details & append version (administrator write) |
| `DELETE` | `/suppliers/{supplierId}` | **Person B (B5)** | Soft delete supplier under active errand fence (administrator write) |
| `GET` | `/supplier-versions/{versionId}` | Person A | Retrieve immutable historical snapshot (authenticated read) |
