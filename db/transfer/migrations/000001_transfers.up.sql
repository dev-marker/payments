BEGIN;

-- A transfer is a saga. Its row says where the saga is, at all times.
CREATE TABLE transfers (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid        NOT NULL,
    idempotency_key text        NOT NULL,
    from_wallet_id  uuid        NOT NULL,
    to_wallet_id    uuid        NOT NULL CHECK (to_wallet_id <> from_wallet_id),
    amount          bigint      NOT NULL CHECK (amount > 0),
    currency        text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    -- a CHECK, not an enum: states come and go as the saga grows
    state           text        NOT NULL DEFAULT 'pending' CHECK (state IN
                        ('pending', 'funds_held', 'completed', 'compensating', 'failed', 'reversed')),
    failure_reason  text        NOT NULL DEFAULT '',
    -- tries of the current step, and when the next one is due
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    -- bumped by every write: optimistic concurrency
    version         integer     NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key),
    CHECK ((state IN ('compensating', 'failed', 'reversed')) = (failure_reason <> ''))
);

-- Which unfinished transfers are due?
CREATE INDEX transfers_due_idx ON transfers (next_attempt_at)
    WHERE state IN ('pending', 'funds_held', 'compensating');

-- Every try of every step: the saga's history.
CREATE TABLE transfer_steps (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transfer_id uuid        NOT NULL REFERENCES transfers (id),
    step        text        NOT NULL,
    outcome     text        NOT NULL CHECK (outcome IN ('succeeded', 'rejected', 'failed')),
    detail      text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX transfer_steps_transfer_id_idx ON transfer_steps (transfer_id, id);

COMMIT;
