BEGIN;

-- One row per idempotency key: the proof that a request ran, and the response
-- we sent, so a retry gets the same answer instead of running again.
CREATE TABLE idempotency_keys (
    -- who sent the key: clients choose keys, so they're only unique per caller
    scope         text        NOT NULL,
    key           text        NOT NULL,
    -- SHA-256 of the method, the path and the canonical body
    fingerprint   bytea       NOT NULL,
    status        text        NOT NULL DEFAULT 'in_progress'
                              CHECK (status IN ('in_progress', 'completed')),
    -- the response we sent, replayed byte for byte
    response_code integer,
    response_body bytea,
    created_at    timestamptz NOT NULL DEFAULT now(),
    -- when a request last claimed the key: a claim that's too old was abandoned
    locked_at     timestamptz NOT NULL DEFAULT now(),
    completed_at  timestamptz,
    expires_at    timestamptz NOT NULL,
    PRIMARY KEY (scope, key),
    -- a completed key always has its response, and only a completed key does
    CHECK ((status = 'completed') = (response_code IS NOT NULL AND response_body IS NOT NULL))
);

-- The cleanup job deletes expired keys, oldest first.
CREATE INDEX idempotency_keys_expires_at_idx ON idempotency_keys (expires_at);

COMMIT;
