# Review of the original services

This is a review of the repository as it stood at commit `199b246`
(Account, Catalog and Order gRPC services behind a gqlgen gateway), before
Backdraft was built on top of it. Every item below is fixed in the current
tree; the ones that change behaviour have regression tests.

The code was a solid starting point: clean package boundaries, a repository
interface per service, and vendored dependencies. The problems cluster in
three places: what happens to data across service boundaries, what errors
mean to callers, and what happens when a dependency is slow.

## Correctness

### 1. Order history shows today's prices, not the prices paid
**Where:** `order/server.go` `GetOrdersForAccount`, `order/up.sql`
**What:** `order_products` stored only `product_id` and `quantity`. Reading
an account's orders re-fetched every product from the catalog and decorated
the lines with the *current* name, description and price.
**Impact:** change a product's price and every historical order changes with
it, while `orders.total_price` (computed at purchase) does not, so line items
stop summing to the total. Reads of order history also depended on the
catalog being up.
**Fix:** line items snapshot name, description and unit price at purchase.
Reading history no longer calls the catalog at all.
**Test:** `order/service_test.go` `TestOrderHistoryKeepsThePricePaid`.

### 2. Unknown product IDs are silently dropped from an order
**Where:** `order/server.go` `PostOrder`
**What:** products not returned by the catalog were skipped. An order for
`[known, unknown]` was created with one line; an order of only unknown IDs
was created with no lines and a total of 0 (and then vanished from history,
because the history query used an inner join on lines).
**Fix:** every requested product must exist, or the order is rejected with
`InvalidArgument` naming the missing IDs. Empty orders and zero quantities
are rejected.
**Test:** `TestUnknownProductsAreRejectedNotDropped`.

### 3. The same product twice in one order fails the whole order
**Where:** `order/up.sql` primary key `(product_id, order_id)`
**What:** `[{id: A, qty: 1}, {id: A, qty: 2}]` violated the primary key; the
error surfaced as an opaque "could not post order".
**Fix:** repeated lines are merged before validation.
**Test:** `TestRepeatedProductsAreMerged`.

### 4. A missing catalog document crashes the catalog service
**Where:** `catalog/repository.go` `ListProductsWithIDs`
**What:** Elasticsearch's multi-get returns `found: false` with a nil
`_source` for missing IDs; `json.Unmarshal(*doc.Source, ...)` dereferenced
it and panicked, taking the process down. Any order containing a deleted
product ID did this.
**Fix:** skip documents that were not found. `GetProductByID` also maps
Elasticsearch's 404 to `NotFound` instead of an internal error.

### 5. Every downstream failure becomes "account not found"
**Where:** `order/server.go` `PostOrder`
**What:** any error from the account service (timeout, unavailable,
cancellation) was replaced by `errors.New("account not found")`, which gRPC
reports as `UNKNOWN`.
**Impact:** callers could not tell a missing account (do not retry) from a
blip (retry), and a transient outage looked like bad input.
**Fix:** `NotFound` from the account service becomes `FailedPrecondition`;
everything else keeps its status. All services now map domain errors to
codes in one place (`toStatus`), and `internal/rpcerr` translates context
errors.
**Test:** `TestAccountErrorsKeepTheirMeaning`.

### 6. Retrying `PostOrder` can create duplicate orders
**Where:** the whole order path
**What:** nothing made order creation idempotent. Any retry after a timeout,
by a client or by an infrastructure layer, could place the same order twice,
because a request that timed out has often succeeded.
**Fix:** `PostOrderRequest.idempotency_key`, unique per account
(`UNIQUE (account_id, idempotency_key)`, `ON CONFLICT DO NOTHING`, then read
back). Replays return the original order and set an `idempotent-replayed`
response header. The gateway passes a client's `Idempotency-Key` header
through, and generates one per mutation otherwise.
**Test:** `TestIdempotencyKeyReplaysTheOriginalOrder`.

### 7. Money is stored as `MONEY` and summed as floats
**Where:** `order/up.sql`, `order/service.go`
**What:** Postgres `MONEY` formatting depends on the server's
`lc_monetary`; the read path round-tripped it through
`::money::numeric::float8`. Totals were accumulated in `float64`
(`0.1 + 0.2 != 0.3`).
**Fix:** `NUMERIC(12,2)` columns, totals computed in integer cents.

### 8. `createOrder` returns an order with no products
**Where:** `graphql/mutation_resolver.go`
**What:** the mutation built the response without `Products`, so selecting
`products` on the result always returned an empty list.
**Fix:** the response is built from the stored order.

### 9. Ignored error on `PrepareContext`
**Where:** `order/repository.go` `PutOrder`
**What:** `stmt, _ := tx.PrepareContext(...)`; a failure would panic on the
nil statement. The `COPY` statement was also closed after use instead of
before commit, which `lib/pq` requires.
**Fix:** errors checked; statement closed before commit.

## Contracts

### 10. Timestamps travel as `time.MarshalBinary` bytes
**Where:** `order/order.proto` `bytes createdAt`
**What:** an encoding only Go can read, and only by convention.
**Fix:** `google.protobuf.Timestamp`.

### 11. `option go_package = "./"`
**Where:** all three protos
**What:** generated code did not declare its import path; regeneration
depended on running `protoc` from a particular directory.
**Fix:** full import paths, versioned proto packages (`order.v1`), and a
working `go:generate` line in each `server.go`.

### 12. No input validation
**Where:** `account/service.go`, `catalog/service.go`
**What:** account names longer than the `VARCHAR(24)` column failed with a
database error; negative and NaN prices were accepted.
**Fix:** validation in the service layer, reported as `InvalidArgument`.

## Resilience

### 13. Timeouts chosen per resolver, not per request
**Where:** every gateway resolver created its own
`context.WithTimeout(ctx, 3*time.Second)`.
**What:** the effective deadline depended on resolver nesting, and nothing
bounded the calls the order service made on the gateway's behalf.
**Now:** per-call policy lives in one place, the Backdraft agent, and the
gateway's inbound deadline propagates downstream. Which leads to the reason
the rest of this repository exists: once timeouts and retries are explicit,
you can see what they add up to. See the README.

### 14. Deprecated APIs
`grpc.Dial` and `grpc.WithInsecure` (replaced by `grpc.NewClient` and
`insecure.NewCredentials()`), gqlgen's `handler.GraphQL` and
`handler.Playground` (replaced by `handler.New` with explicit transports).

### 15. No graceful shutdown
Services exited on signal mid-request. They now drain with
`GracefulStop` / `http.Server.Shutdown` (`internal/serve`).

## Operations

### 16. Docker and Compose
- `order/app.dockerfile` used a different `WORKDIR` path from the others
  (`go-microservice`), and the four app Dockerfiles were near-copies. They
  are now one parameterised `Dockerfile`.
- `graphql` did not `depends_on` the order service.
- Postgres 10.3 (end of life in 2022) is now 16; databases have health
  checks and services wait for them.
- Images run as a non-root user; published ports bind to `127.0.0.1`.
- Credentials are still inline in `docker-compose.yaml`; that is fine for a
  local stack and should not be copied anywhere else.

### 17. Nothing ran without Docker
Every service needed its database. Each repository now has an in-memory
implementation (`DATABASE_URL=memory`, the default), so `go run ./cmd/lab`
starts the whole system in one process.
