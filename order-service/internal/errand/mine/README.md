# My errands (A7)

`mine` serves the caller's own errand lists (commit A7, `feat(order-mine):
requester, courier and history lists`):

| Endpoint | Store method | Holds | Order | Item view |
| --- | --- | --- | --- | --- |
| `GET /me/errands/requested` | `ListRequested` | active errands the caller requested (F3.11) | newest `createdAt` first | `RequesterErrandView` |
| `GET /me/errands/delivering` | `ListDelivering` | active errands assigned to the caller (F3.12) | newest `updatedAt` first | `CourierErrandView` |
| `GET /me/errands/history` | `ListHistory` | terminal errands the caller requested or delivered (F3.13), or one of the two with `role` | newest `updatedAt` first | by the caller's role in each errand |

"Active" is every non-terminal state, including `PENDING_RESERVATION` and
`REVIEW_REQUIRED`. The filtering and ordering are the store's (A1); the
handler only pages, renders and checks nothing else. It depends on `Store`
and `SupplierGate` only, and is tested against [`testfakes`](../../testfakes/).

## Requests

Each route takes a user token with `errands:use` and two optional query
parameters. The user is always the token's subject; no route takes a user ID
(NFR3.3.1).

| Parameter | Type | Default | Rules |
| --- | --- | --- | --- |
| `limit` | integer | `25` | 1 to 100 |
| `cursor` | string | first page | The previous page's `nextCursor` from the same list, unchanged; at most 512 characters |

`GET /me/errands/history` also takes an optional `role` (requester
enhancements, R1). The other lists ignore it.

| `role` | Holds | Item view |
| --- | --- | --- |
| left out | errands the caller requested or delivered (as before) | by the caller's role in each errand |
| `REQUESTER` | errands the caller requested | `RequesterErrandView` |
| `COURIER` | errands the caller delivered | `CourierErrandView` |

It is case-sensitive; any other value, including an empty one, is
`400 INVALID_ARGUMENT`.

```http
GET /me/errands/history?limit=2
Authorization: Bearer <user token with errands:use>
```

```json
{
  "items": [
    { "errandId": "…", "viewerRole": "COURIER", "status": "COMPLETED", "…": "…" },
    { "errandId": "…", "viewerRole": "REQUESTER", "status": "CANCELLED", "contactName": "Alex Tan", "…": "…" }
  ],
  "nextCursor": "eyJsIjoiaGlzdG9yeSIs…"
}
```

`items` is `[]` when the list is empty, and `nextCursor` is left out on the
last page.

The cursor is base64url JSON of the list's name and the last item's sort key
(`createdAt` or `updatedAt`, and `errandId`). The history list's name
includes the role (`history`, `history:REQUESTER`, `history:COURIER`). A
cursor from another list or role, or one that does not decode, is
`400 INVALID_ARGUMENT`. It carries no user
ID: given to another user, it only pages through that user's own errands.

## Field visibility

The views are the shared ones in `errandhttp`, so the rules of
`docs/contracts.md` § Field visibility (NFR3.6.4) hold without anything
list-specific:

- **Requester items** carry the requester's own contact person and pending
  edit, and nothing about the courier: no ID, name or mobile.
- **Courier items** carry the requester's notes (F3.12) but never the
  requester's identity, and the contact person only while the errand is
  `ASSIGNED`, `PICKED_UP`, `DELIVERED` or `REVIEW_REQUIRED`. Those are the
  only active states with a courier, so every `delivering` item shows it,
  and no `history` item does.
- In `history`, an errand the caller requested is shown in the requester
  view, any other in the courier view. A requester cannot accept their own
  errand, so no errand is both. With `role`, every item has that role's
  view.

A courier who withdrew is no longer the errand's courier, so it is in neither
their `delivering` list nor their `history`, with or without
`role=COURIER`.

## Pickup details

Each item's `pickup` (and a pending edit's) comes from
`SupplierGate.GetVersion` on the errand's stored supplier version. Versions
are immutable and kept for deleted suppliers, so history stays readable
after a supplier is deleted (F2.4.3). A page reads each distinct version
once (`errandhttp.LoadPickups` now takes several errands).

## Errors

1. token and scope (`401`, `403`; a service token is `403`);
2. `role` (history only), `limit` and `cursor` (`400 INVALID_ARGUMENT`);
3. the store read (`500 INTERNAL` on failure);
4. supplier-service (`503 DEPENDENCY_UNAVAILABLE`). The whole page fails
   rather than returning items without their pickup location. A version
   supplier-service does not know is `500`, as in A3–A6.

Only the operation and error are logged, never the caller or request data.

## Open concerns

- **Not wired into the service.** `mine.NewHandler` is registered in the
  integration commits (I1–I3), like A3–A6.
- **No `rejectionReason`.** A `REJECTED` errand in the requester's history
  has no `rejectionReason`, for the reason given in `errandhttp.RequesterErrand`:
  the `Store` port does not expose the `REJECTED` transition's reason.
- **Pickup reads are sequential.** One `GetVersion` per distinct version on
  the page. B's `supplierclient.CachedGate` keeps versions in Redis with no
  expiry, so these are usually cache hits. With Redis off (`REDIS_URL`
  empty), a page of 100 errands from different suppliers makes up to 100
  sequential calls; if that shows up, the reads could run concurrently.
- **Race detector not run** (no cgo on the development machine). The handler
  holds no shared mutable state.
