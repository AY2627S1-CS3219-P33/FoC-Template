# Administrator Supplier Creation Workflow

This document specifies the creation workflow and error-handling cases enforced by the administrator creation handler in [`handler.go`](./handler.go), adhering to the supplier service contract, commit specification B2, requirements F2.2, F2.2.1–F2.2.3, F2.2.5, NFR2.2, and NFR2.2.1.

---

## Handled Cases

| Case | Trigger Condition | HTTP Status | Error Code (`apperror.Code`) | Response / Action |
| :--- | :--- | :---: | :---: | :--- |
| **1. Unauthenticated Request** | Request context lacks an authenticated `auth.Principal` | `401 Unauthorized` | `UNAUTHENTICATED` | `"authentication is required"` |
| **2. Insufficient Permissions** | Authenticated caller lacks `auth.ManageSuppliers` (e.g. non-admin user) | `403 Forbidden` | `FORBIDDEN` | `"insufficient permissions for this operation"` |
| **3. Empty Request Body** | Request body is absent or empty (`io.EOF`) | `400 Bad Request` | `INVALID_ARGUMENT` | `"request body is required"` |
| **4. Malformed JSON Syntax** | Payload contains invalid JSON syntax or unmarshal type mismatch | `400 Bad Request` | `INVALID_ARGUMENT` | `"request body must be valid JSON: ..."` |
| **5. Unknown Fields Present** | Request body includes unexpected attributes not defined in `SupplierWrite` | `400 Bad Request` | `INVALID_ARGUMENT` | Rejects unknown fields (`DisallowUnknownFields`) |
| **6. Client-Supplied `supplierId`** | Client attempts to supply `supplierId` in payload (F2.2.5) | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `{"field": "supplierId", "message": "must not be provided"}` |
| **7. Client-Supplied `versionId`** | Client attempts to supply `versionId` in payload (F2.2.5) | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `{"field": "versionId", "message": "must not be provided"}` |
| **8. Missing Mandatory Fields** | Any of `name`, `type`, `building`, `floor`, `locationDescription`, `latitude`, `longitude`, `openingTime`, `closingTime` omitted or blank (F2.2.1, F2.2.2) | `400 Bad Request` | `INVALID_ARGUMENT` | Field-specific violations: `"supplier fields are invalid"` with populated `fields` list |
| **9. Coordinates Out of Bounds** | `latitude` not in `[-90, 90]` or `longitude` not in `[-180, 180]` | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `latitude: "must be between -90 and 90"` / `longitude: "must be between -180 and 180"` |
| **10. Invalid Operating Hours** | Times do not match `HH:MM` 24-hour format, or `openingTime == closingTime` | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `"must use HH:MM in 24-hour time"` or `closingTime: "must differ from openingTime"` |
| **11. Invalid Image URL** | `imageUrl` provided but not absolute `http://` or `https://`, or exceeds 2048 runes | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `"must be an absolute HTTP or HTTPS URL"` or `"is too long"` |
| **12. Field Length Exceeded** | Strings exceed domain bounds (`name` > 120, `type` > 64, `building` > 120, `floor` > 32, `locationDescription` > 500) | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `"<field> is too long"` |
| **13. Normalized Name Conflict** | Repository reports normalized name already exists among active records (F2.2.3) | `409 Conflict` | `SUPPLIER_NAME_CONFLICT` | `"a supplier with this normalized name already exists"` |
| **14. Database Race Conflict** | Concurrent creation collides on database unique constraint `suppliers_live_normalized_name_uidx` | `409 Conflict` | `SUPPLIER_NAME_CONFLICT` | Maps database constraint race to `"a supplier with this normalized name already exists"` |
| **15. Repository / System Failure** | Database or internal infrastructure failure occurs | `500 Internal Server Error` | `INTERNAL` | `"an unexpected internal error occurred"` *(prevents secret/credential leakage)* |
| **16. Successful Creation** | All validations pass and record is persisted (F2.2) | `201 Created` | *None* | Sets header `Location: /suppliers/{supplierId}` and returns full `Supplier` JSON body with generated UUIDs and `available: true` |

---

## Architectural Details

1. **Defense-in-Depth Authorization**: The handler verifies [`auth.PrincipalFromContext`](../auth/context.go) and asserts the caller possesses [`auth.ManageSuppliers`](../auth/port.go), while remaining directly compatible with the upstream [`auth.Middleware.RequireManage`](../auth/middleware.go) wrapper.
2. **Encapsulated Identifiers (F2.2.5)**: The client cannot choose or influence [`supplierId`](../types.go) or [`versionId`](../types.go). Any attempt to pass them returns an explicit validation error.
3. **Deterministic Testing**: Handlers accept [`supplier.Writer`](../repository.go) and can be instantiated with [`NewHandlerWithClock`](./handler.go) to test deterministic timestamps against fakes.
4. **Frozen API Conformance**: Error responses conform strictly to the standard [`apperror.Error`](../../apperror/error.go) schema, and successful creation responses strictly match [`Supplier`](../../openapi/common.yaml) schema.
