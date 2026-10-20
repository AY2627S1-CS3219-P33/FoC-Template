# Browsing errands (A3)

`browse` serves the read-only errand routes (commit A3, `feat(order-browse):
list open errands and errand details`):

- `GET /errands/open`: the open-errand list couriers browse (F3.2).
- `GET /errands/{errandId}`: one errand, in the view the caller may see
  (NFR3.6.3, NFR3.6.4).

The handler depends only on ports (`Store`, `SupplierGate`, `OpenErrandCache`,
`Clock`) and is tested against [`testfakes`](../../testfakes/), with both the
fake cache and B's `cache.NoopOpenErrandCache`.

## Requests

Both routes take no body. Each needs a user token with the `errands:use`
scope:

```http
Authorization: Bearer <user token>
```

### `GET /errands/open`

All parameters are optional. With none, the response is the first 25 open
errands, soonest acceptance deadline first.

| Parameter | In | Type | Default | Rules |
| --- | --- | --- | --- | --- |
| `supplierId` | query | UUID | all suppliers | Only errands from this supplier |
| `lat` | query | number | none | -90 to 90; must be sent with `lng` |
| `lng` | query | number | none | -180 to 180; must be sent with `lat` |
| `limit` | query | integer | `25` | 1 to 100 |
| `cursor` | query | string | first page | The previous page's `nextCursor`, unchanged; at most 512 characters |

Sending `lat` and `lng` sorts the list by distance and adds `distanceMeters`
to each item.

```http
GET /errands/open?lat=1.2966&lng=103.7764&limit=2
```

```json
{
  "items": [
    {
      "errandId": "6f1c2a9e-0b7d-4c3e-9a51-2d8e4f7b1c90",
      "version": 1,
      "supplierId": "3b9d4e21-7a6f-4c18-8e02-5f1a9c3d7b64",
      "supplierVersionId": "a7e2c5d8-1f4b-4a93-b6c0-9d3e8f2a5c17",
      "pickup": {
        "name": "Fine Food",
        "building": "Town Plaza",
        "floor": "1",
        "locationDescription": "Next to the lift lobby",
        "latitude": 1.3045,
        "longitude": 103.7731
      },
      "deliveryBuilding": { "id": "c4f8e1a2-5b3d-4e7f-9a6c-1d2b3e4f5a6b", "name": "COM1" },
      "deliveryDetail": "Level 2, seminar room 2",
      "items": [{ "description": "Chicken rice", "quantity": 1 }],
      "reward": 300,
      "createdAt": "2026-10-20T04:07:12Z",
      "acceptanceDeadline": "2026-10-20T04:37:12Z",
      "groupKey": "3b9d4e21-7a6f-4c18-8e02-5f1a9c3d7b64:2026-10-20T04:00Z",
      "distanceMeters": 937.4
    }
  ],
  "nextCursor": "eyJmIjoiOWQ..."
}
```

`nextCursor` is left out on the last page. Values above are illustrative.

### `GET /errands/{errandId}`

| Parameter | In | Type | Rules |
| --- | --- | --- | --- |
| `errandId` | path | UUID | Required; any letter case |

```http
GET /errands/6f1c2a9e-0b7d-4c3e-9a51-2d8e4f7b1c90
```

The body is one of three views, chosen by who is asking (see the cases
below). Each carries `viewerRole` so the client knows which one it got.

## Requests and responses

Checks run in this order: token, scope, then query parameters (`supplierId`,
`lat`/`lng`, `limit`, `cursor`). The first failure is returned. Every error has
the body `{"code": "...", "message": "..."}`.

### Both routes

| Request | Response |
| --- | --- |
| No token, or an invalid one | `401 UNAUTHENTICATED` |
| User token without `errands:use`, or a service token | `403 FORBIDDEN` |
| Any unexpected failure (store error, etc.) | `500 INTERNAL`; logged with the operation, the error and the errand ID only |

### `GET /errands/open`

| Request | Response |
| --- | --- |
| No parameters | `200`, first 25 open errands by acceptance deadline, then `errandId` |
| `supplierId=<uuid>` | `200`, only that supplier's open errands; an unknown supplier gives `items: []` |
| `lat=1.2966&lng=103.7764` | `200`, by distance, then deadline, then `errandId`; each item has `distanceMeters` |
| `limit=10` | `200`, at most 10 items |
| `cursor=<nextCursor>` with the same filters | `200`, the items after the previous page |
| `cursor=<nextCursor>` with a different `limit` | `200`, the next page at the new size |
| No open errands match | `200`, `{"items": []}` with no `nextCursor` |
| An open errand has an unknown supplier version | `200` without that errand; a warning is logged with its ID |
| `supplierId=abc` | `400 INVALID_ARGUMENT`: "supplierId must be a UUID" |
| `lat` without `lng`, or `lng` without `lat` | `400 INVALID_ARGUMENT`: "lat and lng must be sent together" |
| `lat=91`, `lat=abc` or `lat=NaN` | `400 INVALID_ARGUMENT`: "lat must be a number from -90 to 90" |
| `lng=181`, `lng=abc` or `lng=Inf` | `400 INVALID_ARGUMENT`: "lng must be a number from -180 to 180" |
| `limit=0`, `limit=101` or `limit=ten` | `400 INVALID_ARGUMENT`: "limit must be between 1 and 100" |
| `cursor=` (empty), longer than 512 characters, not base64url JSON, or missing its position | `400 INVALID_ARGUMENT`: "cursor is invalid or was issued for other filters" |
| A valid cursor with a different `supplierId`, `lat` or `lng` | `400 INVALID_ARGUMENT`: same message |
| Supplier-service unreachable when the open set is loaded | `503 DEPENDENCY_UNAVAILABLE` |

### `GET /errands/{errandId}`

| Request | Response |
| --- | --- |
| Caller is the requester, any status | `200`, `RequesterErrandView` (`viewerRole: REQUESTER`), with the contact person and any pending edit |
| Caller is the assigned courier, `ASSIGNED`, `PICKED_UP`, `DELIVERED` or `REVIEW_REQUIRED` | `200`, `CourierErrandView` (`viewerRole: COURIER`) with `contactName` and `contactMobile` |
| Caller is the assigned courier, any other status | `200`, `CourierErrandView` without the contact person |
| Anyone else, errand `OPEN` | `200`, `PublicErrandView` (`viewerRole: PUBLIC`), the same fields as an open-list item |
| Anyone else, errand not `OPEN` | `404 ERRAND_NOT_FOUND`, so the existence of an assigned errand is not disclosed |
| No errand with that ID | `404 ERRAND_NOT_FOUND` |
| `errandId` is not a UUID | `400 INVALID_ARGUMENT`: "errandId must be a UUID" |
| Supplier-service unreachable while loading pickup locations | `503 DEPENDENCY_UNAVAILABLE` |
| The errand's supplier version is unknown | `500 INTERNAL` (see [below](#an-unknown-supplier-version-hides-the-errand-on-the-list)) |

A courier who withdrew is no longer `courier_id`, so they fall into "anyone
else". This route always reads from the store, never from the cache.

## How the open list is built

1. **Load the open set** (`openset.go`): every `OPEN` errand whose acceptance
   deadline is after now, as the redacted `errand.OpenErrandView`.
   - It is read from `OpenErrandCache` first.
   - On a miss it is loaded from `Store.ListOpen`, page by page (500 per
     page), behind a `singleflight` group, so concurrent misses cause one
     load. Each supplier version is read once per load through
     `SupplierGate.GetVersion`.
   - The result is cached for `OpenSetTTL` (2 s).
2. **Per request** (`list.go`), on that shared set:
   1. drop errands whose deadline has passed since the set was cached;
   2. keep only the requested `supplierId`;
   3. compute the distance from `lat`/`lng` when given;
   4. sort and cut one page after the cursor.

Courier coordinates are used only in step 2. They are never cached, stored or
logged (F3.2.4), and the cursor holds a hash of them, not the values.

### Cursors

The cursor is base64url JSON holding the last item's position (distance,
deadline, ID) and an 8-byte SHA-256 digest of the filters (`supplierId`, `lat`,
`lng`). That is why a cursor reused with other filters is refused, while
`limit`, which is not a filter, may change between pages.

Paging is by position, not offset: an errand that is accepted between two
requests does not shift the next page.

### Item fields

- `groupKey`: `"{supplierId}:{yyyy-MM-ddTHH:mmZ}"`, the creation time
  truncated to 15 minutes in UTC. Errands with the same key can be picked up
  together (F3.2.1).
- `distanceMeters`: haversine distance with a mean Earth radius of
  6,371,008.8 m. It is present only when coordinates were sent.
- `version`: the value to send back as `expectedVersion` when accepting.
- No requester identity, notes or contact person, by construction:
  `OpenErrandView` has no such fields.

## Design decisions

### The shared load ignores the caller's cancellation

The `singleflight` load runs with `context.WithoutCancel` and its own 10 s
timeout. If it used the first caller's context, that caller disconnecting
would fail every request waiting on the same load. Each caller still stops
waiting when its own request is cancelled.

### An unknown supplier version hides the errand on the list

Versions are kept for deleted suppliers, so `SUPPLIER_UNAVAILABLE` from
`GetVersion` for a stored errand means bad data. On the list, that errand is
left out and a warning is logged with its ID, rather than failing the list for
every courier. On the detail route, it is `500 INTERNAL`:
`errandhttp.LoadPickups` drops the code, because a `422` would blame the
client. This also applies to A2's replay response.

### Deadline check at read time

The cached set is filtered again by `acceptanceDeadline > now` on every
request. An errand whose deadline passes while the set is cached is therefore
hidden at once, not up to 2 s later, and before the expiry job runs.

## Open concerns

- **`rejectionReason` is never returned** on the requester view, as in A2
  ([create/README.md](../create/README.md#open-concerns)). This route is where
  a requester would normally learn why an errand was `REJECTED`, so the gap
  matters more here. Closing it needs a jointly reviewed `Store` port change.
- **The whole open set is loaded on each miss.** This fits the NFR1.3 volume
  and the cache contract. If the number of open errands grows into the
  thousands, the load and the JSON value in Redis grow with it.
- **The `singleflight` test waits 200 ms** for the other requests to join the
  load before releasing it. It passed 15 runs in a row, but on a very slow CI
  machine a request might not join in time and the test would see two loads.
- **Not wired into the service.** `browse.NewHandler` is registered in the
  integration commits (I1–I3), with B's Redis or no-op cache selected by
  `REDIS_URL`.
- **Race detector not run** (no cgo on the development machine). The shared
  open set is never modified after loading; run
  `go test -race ./internal/errand/browse/` on Linux or in CI.
