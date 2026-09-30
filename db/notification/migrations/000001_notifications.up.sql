BEGIN;

-- One row per event we notified someone about. The primary key is what makes
-- the consumer idempotent: an event delivered twice finds its row.
CREATE TABLE notifications (
    event_id   uuid        PRIMARY KEY,
    user_id    uuid        NOT NULL,
    recipient  text        NOT NULL,
    subject    text        NOT NULL,
    sent_at    timestamptz NOT NULL DEFAULT now()
);

-- Messages we gave up on, and why, until someone replays them.
CREATE TABLE dead_letters (
    stream      text        NOT NULL,
    seq         bigint      NOT NULL,
    subject     text        NOT NULL,
    -- exactly as it arrived: it might not even decode
    payload     bytea       NOT NULL,
    error       text        NOT NULL,
    deliveries  integer     NOT NULL,
    failed_at   timestamptz NOT NULL DEFAULT now(),
    replayed_at timestamptz,
    PRIMARY KEY (stream, seq)
);

COMMIT;
