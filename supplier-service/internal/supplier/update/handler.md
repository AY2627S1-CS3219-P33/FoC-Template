# Administrator Supplier Update Workflow

This document specifies the update workflow and error-handling cases enforced by the administrator update handler in [`handler.go`](./handler.go), adhering to the supplier service contract, commit specification B3, requirements F2.3, F2.3.1, F2.3.2, F2.3.4, F2.5, F2.5.1, and F2.5.3.

---

## Handled Cases

| Case | Trigger Condition | HTTP Status | Error Code (`apperror.Code`) | Response / Action |
| :--- | :--- | :---: | :---: | :--- |
| **1. Unauthenticated Request** | Request context lacks an authenticated `auth.Principal` | `401 Unauthorized` | `UNAUTHENTICATED` | `"authentication is required"` |
| **2. Insufficient Permissions** | Authenticated caller lacks `auth.ManageSuppliers` (e.g., non-admin user) | `403 Forbidden` | `FORBIDDEN` | `"insufficient permissions for this operation"` |
| **3. Malformed `supplierId`** | Path parameter `{supplierId}` is not a valid UUID | `400 Bad Request` | `INVALID_ARGUMENT` | `"supplierId must be a UUID"` |
| **4. Empty Request Body** | Request body is absent or empty (`io.EOF`) | `400 Bad Request` | `INVALID_ARGUMENT` | `"request body is required"` |
| **5. Malformed JSON Syntax** | Payload contains invalid JSON syntax or unmarshal type mismatch | `400 Bad Request` | `INVALID_ARGUMENT` | `"request body must be valid JSON: ..."` |
| **6. Unknown Fields Present** | Request body includes unexpected attributes not defined in `SupplierPatch` | `400 Bad Request` | `INVALID_ARGUMENT` | Rejects unknown fields (`DisallowUnknownFields`) |
| **7. Client-Supplied `supplierId`** | Client attempts to supply `supplierId` in payload (F2.3.2) | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `{"field": "supplierId", "message": "must not be provided"}` |
| **8. Client-Supplied `versionId`** | Client attempts to supply `versionId` in payload (F2.3.2) | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `{"field": "versionId", "message": "must not be provided"}` |
| **9. Empty Patch Object** | Request body is `{}` containing no fields to update | `400 Bad Request` | `INVALID_ARGUMENT` | `"patch must contain at least one field"` |
| **10. Invalid Field Values** | Field strings exceed length bounds, blank strings for mandatory fields, or coordinates out of bounds `[-90, 90]`/`[-180, 180]` | `400 Bad Request` | `INVALID_ARGUMENT` | Field violations list under `"supplier fields are invalid"` |
| **11. Invalid Operating Hours** | Operating hours do not match `HH:MM` 24-hour format, or patch has `openingTime == closingTime` | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `"must use HH:MM in 24-hour time"` or `closingTime: "must differ from openingTime"` |
| **12. Invalid Image URL** | `imageUrl` provided as string but not absolute HTTP/HTTPS URL or exceeds 2048 runes | `400 Bad Request` | `INVALID_ARGUMENT` | Field violation: `"must be an absolute HTTP or HTTPS URL"` or `"is too long"` |
| **13. Remove Image (`null`)** | Client passes `"imageUrl": null` | *Proceeds* | *None* | Explicitly unlinks image (`ImageURLSet: true, ImageURL: nil`) |
| **14. Supplier Not Found** | Target `supplierId` does not exist or has been soft-deleted | `404 Not Found` | `SUPPLIER_NOT_FOUND` | `"supplier not found"` |
| **15. Normalized Name Conflict** | Renaming matches an existing active supplier's normalized name (F2.3.4) | `409 Conflict` | `SUPPLIER_NAME_CONFLICT` | `"a supplier with this normalized name already exists"` |
| **16. Prospective Invariant Failure** | Applying partial patch to current version violates record invariants (e.g. prospective opening == closing) | `400 Bad Request` | `INVALID_ARGUMENT` | Field-specific violations mapped from repository `ValidationError` |
| **17. Repository / System Failure** | Database or internal infrastructure failure occurs | `500 Internal Server Error` | `INTERNAL` | `"an unexpected internal error occurred"` *(prevents secret/credential leakage)* |
| **18. Successful Update** | All validations pass; new immutable version appended and current version pointer updated (F2.3, F2.5) | `200 OK` | *None* | Returns updated `Supplier` JSON representation with new `versionId` and updated `updatedAt` |

---

## Architectural Details

1. **Defense-in-Depth Authorization**: The handler verifies [`auth.PrincipalFromContext`](../auth/context.go) and asserts the caller possesses [`auth.ManageSuppliers`](../auth/port.go), while remaining directly compatible with the upstream [`auth.Middleware.RequireManage`](../auth/middleware.go) wrapper.
2. **Encapsulated Identifiers (F2.3.2)**: The client cannot modify `supplierId` or `versionId`. Any attempt to pass them returns an explicit validation error.
3. **Immutable Record Versioning (F2.5, F2.5.3)**: Updating never overwrites an existing snapshot. A new immutable version row is appended to `supplier_versions`, and the parent supplier record's pointer is atomically updated in the database.
4. **Deterministic Testing**: Handlers accept [`supplier.Writer`](../repository.go) and can be instantiated with [`NewHandlerWithClock`](./handler.go) to test deterministic timestamps against fakes without database dependencies.
5. **Frozen API Conformance**: Error responses conform strictly to the standard [`apperror.Error`](../../apperror/error.go) schema, and successful update responses strictly match the [`Supplier`](../../openapi/common.yaml) schema.
