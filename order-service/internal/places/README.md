<!--
AI Assistance Disclosure:
Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
Scope: Documentation writing and formatting.
Author review: Reviewed, validated, and tested by the authors.
-->

# Saved places (P3)

`places` serves the caller's saved delivery and pickup places (requester
enhancements P3, `feat(order-places): saved places API`). A place only fills
in an errand request; nothing else reads it. The design is in
[`references/requester-enhancements-n2h.md`](../../references/requester-enhancements-n2h.md),
"Saved places".

| Endpoint | Store method | Answer |
| --- | --- | --- |
| `GET /me/places?kind=` | `List` | `200`, `{ "items": [...] }`, newest `createdAt` first |
| `POST /me/places` | `Create` | `201`, the place |
| `PATCH /me/places/{placeId}` | `Update` | `200`, the place |
| `DELETE /me/places/{placeId}` | `Delete` | `204` |

Each route takes a user token with `errands:use`. The user is always the
token's subject; no route takes a user ID (NFR3.3.1). Another user's place
is `404 PLACE_NOT_FOUND`, like a missing one. The handler depends on
`Store` (`requesterrepo` in the service) and `errand.SupplierGate` only,
and is tested against [`testfakes`](../testfakes/).

## Kinds

| Kind | Saved fields | Available while |
| --- | --- | --- |
| `DELIVERY` | `label`, `deliveryBuildingId`, `deliveryDetail` | the building is in supplier-service's list and not retired |
| `PICKUP` | `label`, `supplierId` | the supplier exists and is not deleted, open or not |

Contact details are not saved: the errand form prefills them from the
profile (NFR3.6.4).

## Requests

`GET /me/places` takes an optional `kind`, `DELIVERY` or `PICKUP`. It is
case-sensitive; any other value, including an empty one, is
`400 INVALID_ARGUMENT`. The list is not paged: a user has at most 20 places
of each kind.

```http
POST /me/places
Authorization: Bearer <user token with errands:use>
Content-Type: application/json

{ "kind": "DELIVERY", "label": "Hall", "deliveryBuildingId": "…", "deliveryDetail": "Block B, level 7 lounge" }
```

```json
{
  "placeId": "…",
  "kind": "DELIVERY",
  "label": "Hall",
  "deliveryBuilding": { "id": "…", "name": "Eusoff Hall", "retired": false },
  "deliveryDetail": "Block B, level 7 lounge",
  "available": true,
  "createdAt": "2026-11-02T09:00:00Z"
}
```

A `PICKUP` item has `supplierId` and, while available, `supplier`:
`{ name, building, floor, openingTime, closingTime }` from the supplier's
current version.

`PATCH` takes `label`, `deliveryDetail`, or both. The kind, supplier and
building cannot change: delete the place and save another.

## Rules

Bodies are read with `errandhttp.Validator`, so every invalid field is
reported in one `VALIDATION_FAILED` (NFR2.2.1):

- `kind` is required and exactly `DELIVERY` or `PICKUP`.
- `label` is required, not blank, and at most 40 characters.
- A `DELIVERY` body needs `deliveryBuildingId` (a UUID) and `deliveryDetail`
  (`errandhttp.DeliveryDetailText`, the errand rule); a `PICKUP` body needs
  `supplierId`. Any other member, including the other kind's, is
  `is not allowed`.
- A `PATCH` body with neither field (`{}`, or only nulls) is
  `400 INVALID_ARGUMENT`, as for an errand edit; `deliveryDetail` on a
  `PICKUP` place is `VALIDATION_FAILED`.

Only a valid body reaches supplier-service. On `POST`:

- the building must be listed and not retired (`errandhttp.DeliveryBuilding`,
  one `ListBuildings` call);
- the supplier must exist and not be deleted (`GetCurrent`). Opening hours
  are not checked, so a closed supplier can be saved.

Either failure is `VALIDATION_FAILED` on that field. Then `Store.Create`
enforces the per-kind limit and uniqueness (labels per kind ignoring case,
each supplier once).

## Stale places

Supplier and building validity live in supplier-service, so a saved place
can go stale after it is saved. It is never deleted or changed
automatically. Every read checks it again:

- `GET` makes one `ListBuildings` call when any `DELIVERY` place is listed,
  and one `GetCurrent` per distinct `PICKUP` supplier, at most 5 at a time
  (`errgroup`, at most 20 places per kind).
- A retired building gives `retired: true`, `available: false`. A building
  no longer listed at all (supplier-service keeps retired ones, so this
  should not happen) gives an empty `name`, `available: false`.
- `SUPPLIER_UNAVAILABLE` for a supplier gives `available: false` and no
  `supplier`.

The user removes a stale place. It can still be renamed.

## Errors

1. token and scope (`401`, `403`; a service token is `403`);
2. a malformed `placeId` (`404 PLACE_NOT_FOUND`, before the body is read);
3. `kind` query (`400 INVALID_ARGUMENT`), or a body that is not one JSON
   object of at most 4 KiB (`400 INVALID_ARGUMENT`);
4. body fields (`400 VALIDATION_FAILED`);
5. supplier-service (`POST` only): building or supplier
   (`400 VALIDATION_FAILED`), or unreachable (`503 DEPENDENCY_UNAVAILABLE`);
6. the store: `404 PLACE_NOT_FOUND`, `409 PLACE_LIMIT_REACHED`, duplicate
   label or supplier (`400 VALIDATION_FAILED`), or `500 INTERNAL`;
7. supplier-service again when rendering a `GET` or `PATCH` answer
   (`503 DEPENDENCY_UNAVAILABLE`). The whole list fails rather than guessing
   which places are available.

Only the operation and error are logged, never the caller, labels or
delivery details.

## Open concerns

- **Supplier reads are not cached.** `GetCurrent` is deliberately uncached
  (P2), so every list with pickup places calls supplier-service up to 20
  times. If that shows up, a short-lived cache of availability would do;
  it must not hide a deletion for long.
- **A `PATCH` can succeed and still answer `503`.** The change is stored
  before the answer is resolved against supplier-service. Repeating the
  request is safe, since it sets the same values.
- **`Clock` is unused.** `Dependencies.Clock` is required by the plan but
  the store sets the timestamps; it is kept so the handler can stamp
  anything time-based later without changing its wiring.
- **`docs/contracts.md`** does not list `/me/places` in its route table or
  `PLACE_NOT_FOUND` and `PLACE_LIMIT_REACHED` in its error table yet;
  `openapi/common.yaml` does.
