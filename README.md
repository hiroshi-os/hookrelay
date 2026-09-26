# hookrelay

[![CI](https://github.com/hiroshi-os/hookrelay/actions/workflows/ci.yml/badge.svg)](https://github.com/hiroshi-os/hookrelay/actions/workflows/ci.yml)

Signed, retried webhook delivery with a dead-letter queue. Workers claim due
deliveries with `SELECT … FOR UPDATE SKIP LOCKED`, sign bodies with a
Stripe-shaped HMAC header, and back off with full jitter until success or DLQ.

Designed to consume an `events` outbox compatible with [ledgerd](https://github.com/hiroshi-os/ledgerd);
this repo ships its own `events` table so it runs standalone.

## 60-second path

```bash
# Needs Docker for Postgres (or set DATABASE_URL to an existing Postgres 16+)
docker compose up --build -d
# wait for health
curl -sf http://127.0.0.1:8080/health

# Register an endpoint (chaosrecv is on :9090 in compose)
curl -s -X POST http://127.0.0.1:8080/v1/endpoints \
  -H 'authorization: Bearer dev-admin-token' \
  -H 'content-type: application/json' \
  -d '{"url":"http://chaosrecv:9090/","secret":"test-secret","max_concurrency":8}'

# Enqueue an event
curl -s -X POST http://127.0.0.1:8080/v1/events \
  -H 'authorization: Bearer dev-admin-token' \
  -H 'content-type: application/json' \
  -d '{"type":"invoice.paid","payload":{"amount":100}}'
```

No Docker? `go test ./...` and `make fault-small` start an **embedded Postgres**
automatically when `DATABASE_URL` is unset.

```bash
go test -race ./...
make build
make fault-small   # 500 events, 1 restart → bench/RESULTS_SMALL.md
make fault         # 10_000 events, 5 restarts → bench/RESULTS.md
```

## Signature

Header: `Hookrelay-Signature: t=<unix>,v1=<hex>`

Signed payload is `t + "." + raw_body`. HMAC-SHA256 with the endpoint secret.
During rotation, two `v1=` values are sent (current + previous); receivers accept
any match. Default replay window: 300s.

Verify with [`pkg/verify`](pkg/verify):

```go
err := verify.Verify(r.Header.Get("Hookrelay-Signature"), body,
    []string{currentSecret, previousSecret}, verify.Options{})
```

OpenSSL one-liner (replace `SECRET`, `TS`, and the body file):

```bash
printf '%s.%s' "$TS" "$(cat body.json)" | openssl dgst -sha256 -hmac "$SECRET"
```

## Retry schedule

Exponential backoff with **full jitter**: delay ~ Uniform`[0, min(cap, base·2ⁿ)]`
where `n` is the 0-based failed-attempt index. Defaults: `base=1s`, `cap=5m`,
`max_attempts=12`, `max_age=24h`. Exhausted deliveries move to `dead_letters`.
After `disable_after_failures` consecutive failures (default 20), the endpoint
auto-disables until `POST /v1/endpoints/{id}/enable`.

## Admin API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Liveness |
| `POST` | `/v1/endpoints` | `{url, secret, max_concurrency?, disable_after_failures?}` |
| `POST` | `/v1/endpoints/{id}/enable` | Re-enable + reset consecutive failures |
| `GET` | `/v1/dead_letters` | List DLQ rows |
| `POST` | `/v1/dead_letters/{id}/replay` | Reset delivery to pending |
| `POST` | `/v1/events` | Insert outbox event + fan-out deliveries |
| `GET` | `/v1/stats` | Delivery counts / DLQ size |

Auth: `Authorization: Bearer <ADMIN_TOKEN>` (default `dev-admin-token`).

## Measured numbers

From a run recorded in [`bench/RESULTS.md`](bench/RESULTS.md) (fill after
`make fault` on this machine — see that file for hardware, commit SHA, and
exact command). Until that run completes, prefer the CI artifact from the
`fault` job (`bench/CI_RESULTS.md`, 500 events / 1 restart).

| Metric | Value |
| --- | --- |
| Delivered % | see RESULTS.md |
| Lost events | see RESULTS.md (must be 0) |
| Duplicate deliveries | see RESULTS.md |
| DLQ count | see RESULTS.md |
| E2E delay p50 / p99 | see RESULTS.md |

## Honesty / limitations

- Delivery is **at-least-once**, not exactly-once. Receivers must dedupe by
  stable event `id`.
- **Ordering is not guaranteed** across endpoints or after retries.
- Single-node Postgres; no multi-region / no HA story in this repo.
- Auto-disable is per-endpoint consecutive failures, not a circuit breaker with
  half-open probing.
- `chaosrecv` failure rates are probabilistic; exact mixes vary by seed.

## Layout

| Path | Role |
| --- | --- |
| `cmd/hookrelay` | Admin API + delivery workers |
| `cmd/chaosrecv` | Faulty receiver with `/stats` |
| `cmd/faultbench` | 10k-event fault harness + process restarts |
| `internal/store` | pgx store, SKIP LOCKED claims |
| `internal/worker` | HTTP delivery + backoff |
| `internal/sign` | HMAC header generation |
| `pkg/verify` | Receiver-side verification |
| `migrations/` | SQL (also embedded via `internal/migrate`) |

See [DESIGN.md](DESIGN.md) for decisions.

## Resume bullet (DRAFT)

> DRAFT: Built hookrelay — signed webhook relay with SKIP LOCKED workers,
> exponential backoff + DLQ, and a chaos harness showing zero lost events under
> process kills and 30% receiver failure.
