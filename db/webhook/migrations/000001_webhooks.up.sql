BEGIN;

-- Where a user wants their events delivered, and the secret we sign them with.
CREATE TABLE webhook_endpoints (
    id          uuid        PRIMARY KEY,
    user_id     uuid        NOT NULL,
    url         text        NOT NULL,
    event_types text[]      NOT NULL,
    secret      text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_endpoints_user_id_idx ON webhook_endpoints (user_id);

-- One row per event, per endpoint: the work queue, and the delivery log.
CREATE TABLE webhook_deliveries (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id      uuid        NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
    event_id         uuid        NOT NULL,
    event_type       text        NOT NULL,
    -- the exact bytes we sign and send, every time
    payload          bytea       NOT NULL,
    state            text        NOT NULL DEFAULT 'pending'
                                 CHECK (state IN ('pending', 'succeeded', 'failed')),
    attempts         integer     NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    last_status      integer     NOT NULL DEFAULT 0, -- 0: no response at all
    last_error       text        NOT NULL DEFAULT '',
    last_response_ms integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- an event redelivered by NATS finds its row
    UNIQUE (endpoint_id, event_id)
);
-- The worker's question, many times a second: what's due?
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at)
    WHERE state = 'pending';

COMMIT;
