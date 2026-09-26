-- hookrelay schema
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type        TEXT NOT NULL,
    payload     JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS endpoints (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    url                     TEXT NOT NULL,
    secret_current          TEXT NOT NULL,
    secret_previous         TEXT,
    enabled                 BOOLEAN NOT NULL DEFAULT TRUE,
    consecutive_failures    INT NOT NULL DEFAULT 0,
    max_concurrency         INT NOT NULL DEFAULT 8,
    disable_after_failures  INT NOT NULL DEFAULT 20,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS deliveries (
    id              BIGSERIAL PRIMARY KEY,
    event_id        UUID NOT NULL REFERENCES events(id),
    endpoint_id     UUID NOT NULL REFERENCES endpoints(id),
    attempt         INT NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending', -- pending | in_flight | succeeded | dead
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT,
    claimed_at      TIMESTAMPTZ,
    claimed_by      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (event_id, endpoint_id)
);

CREATE INDEX IF NOT EXISTS deliveries_due_idx
    ON deliveries (next_attempt_at)
    WHERE status IN ('pending', 'in_flight');

CREATE TABLE IF NOT EXISTS dead_letters (
    id              BIGSERIAL PRIMARY KEY,
    delivery_id     BIGINT NOT NULL UNIQUE REFERENCES deliveries(id),
    event_id        UUID NOT NULL REFERENCES events(id),
    endpoint_id     UUID NOT NULL REFERENCES endpoints(id),
    attempts        INT NOT NULL,
    last_error      TEXT,
    payload         JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    replayed_at     TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS outbox_cursors (
    endpoint_id UUID PRIMARY KEY REFERENCES endpoints(id),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT 'epoch'
);
