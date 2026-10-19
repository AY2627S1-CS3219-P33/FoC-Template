# Browsing errands (A3)

`browse` serves the read-only errand routes (commit A3, `feat(order-browse):
list open errands and errand details`):

- `GET /errands/open`: the open-errand list couriers browse (F3.2).
- `GET /errands/{errandId}`: one errand, in the view the caller may see
  (NFR3.6.3, NFR3.6.4).

Both routes require a user token with `errands:use`. The handler depends only
on ports (`Store`, `SupplierGate`, `OpenErrandCache`, `Clock`) and is tested
against [`testfakes`](../../testfakes/), with both the fake cache and B's
`cache.NoopOpenErrandCache`.

## `GET /errands/open`

### How the list is built

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

### Ordering and cursors

| Query | Order |
| --- | --- |
| no coordinates | acceptance deadline, then `errandId` |
| `lat` + `lng` | distance to the pickup location, then acceptance deadline, then `errandId` |

The cursor is base64url JSON holding the last item's position (distance,
deadline, ID) and an 8-byte SHA-256 digest of the filters (`supplierId`, `lat`,
`lng`). A cursor reused with different filters is `400 INVALID_ARGUMENT`;
`limit` is not a filter and may change between pages.

Paging is by position, not offset: an errand that is accepted between two
requests does not shift the next page.

### Item fields

- `groupKey`: `"{supplierId}:{yyyy-MM-ddTHH:mmZ}"`, the creation time
  truncated to 15 minutes in UTC (F3.2.1).
- `distanceMeters`: haversine distance with a mean Earth radius of
  6,371,008.8 m. It is present only when coordinates were sent.
- `version`: the value to send back as `expectedVersion` when accepting.
- No requester identity, notes or contact person, by construction:
  `OpenErrandView` has no such fields.

## `GET /errands/{errandId}`

Always read from the store, never from the cache.

| Caller | Status | Response |
| --- | --- | --- |
| the requester | any | `RequesterErrandView` (`viewerRole: REQUESTER`) |
| the assigned courier | any | `CourierErrandView` (`viewerRole: COURIER`); `contactName` and `contactMobile` only while `ASSIGNED`, `PICKED_UP`, `DELIVERED` or `REVIEW_REQUIRED` |
| anyone else | `OPEN` | `PublicErrandView` (`viewerRole: PUBLIC`) |
| anyone else | not `OPEN` | `404 ERRAND_NOT_FOUND`, so the existence of an assigned errand is not disclosed |

A courier who withdrew is no longer `courier_id`, so they fall into "anyone
else".

## Errors

| Status | Code | When |
| --- | --- | --- |
| 400 | `INVALID_ARGUMENT` | Bad `supplierId`, `lat`/`lng` (missing pair, out of range, not a number), `limit` outside 1–100, or a malformed, oversized or mismatched cursor; non-UUID `errandId` |
| 401 | `UNAUTHENTICATED` | No valid token |
| 403 | `FORBIDDEN` | No `errands:use`, or a service token |
| 404 | `ERRAND_NOT_FOUND` | No such errand, or the caller may not see it |
| 503 | `DEPENDENCY_UNAVAILABLE` | Supplier-service is unreachable while loading pickup locations |
| 500 | `INTERNAL` | Anything else; logged with the operation and errand ID only |

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
