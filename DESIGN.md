# DESIGN

## Goals

Reliable at-least-once webhook delivery from an outbox table, with Stripe-shaped
HMAC signatures, retry/backoff, dead-letter queue, and safe multi-worker claiming.

## Outbox

`events(id, type, payload, created_at)` mirrors ledgerd's outbox. Deliveries are
fan-out rows in `deliveries(event_id, endpoint_id, …)` with a unique constraint
so each endpoint sees each event once as a logical delivery (many attempts).

`EnsureDeliveries` inserts pending rows for all enabled endpoints. A background
`FanOutNewEvents` cursor (`outbox_cursors`) also picks up events inserted
outside the admin API.

## Claiming

Workers claim with:

```sql
… FOR UPDATE OF d SKIP LOCKED
```

and set `status='in_flight'`. Concurrent workers never double-claim the same
row. Stale `in_flight` rows (process crash) are returned to `pending` by
`RecoverStaleInFlight` after `2·timeout + 30s`.

Per-endpoint `max_concurrency` caps how many `in_flight` rows exist at once.

## Signatures

`Hookrelay-Signature: t=<unix>,v1=<hex>` over `t + "." + body`. Rotation sends
two `v1` values. `pkg/verify` uses `hmac.Equal`, rejects timestamps outside a
300s window, and accepts any matching secret.

## Failure handling

Any non-2xx, timeout, or connection error increments attempt and schedules
`next_attempt_at` with full-jitter exponential backoff. When `MaxAttempts` or
`MaxAge` is exhausted, the row moves to `dead_letters`. Consecutive endpoint
failures can auto-disable the endpoint; `POST …/enable` clears the counter.

## Chaos / measurement

`cmd/chaosrecv` injects 5xx / hang / slow / reset faults, verifies signatures,
and dedupes by event id. `cmd/faultbench` inserts N events, kills/restarts
`hookrelay` mid-run, waits for drain, and writes `bench/RESULTS.md` with
hardware, commit SHA, and command.

When `DATABASE_URL` is unset, faultbench and store tests start
`embedded-postgres` so local machines without Docker can still measure.

## What broke / trade-offs

- Nested `COUNT(*)` for concurrency inside the claim CTE is simple but not
  free under high load; acceptable for the portfolio scope.
- Fan-out cursor + `EnsureDeliveries` can race; `ON CONFLICT DO NOTHING` keeps
  it safe.
- Windows hosts often lack Docker; embedded Postgres was added so honesty
  numbers can still be produced on the developer machine.
