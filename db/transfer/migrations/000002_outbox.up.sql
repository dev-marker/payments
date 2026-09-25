BEGIN;

-- Events waiting to be published. A service writes them in the same
-- transaction as the change they describe, and a relay publishes them.
CREATE TABLE outbox (
    -- the order events were written in, and the order we publish them in
    seq          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- the event's own ID, sent as its Nats-Msg-Id
    event_id     uuid        NOT NULL UNIQUE,
    subject      text        NOT NULL,
    -- json keeps our bytes exactly as we wrote them; jsonb would reorder keys
    payload      json        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

-- The relay only looks for events it hasn't published, oldest first. Once
-- published, a row leaves this index, so it stays tiny.
CREATE INDEX outbox_unpublished_idx ON outbox (seq) WHERE published_at IS NULL;

-- Wake the relay when events are written. NOTIFY is transactional: it's
-- sent at commit, never after a rollback, and once per transaction.
CREATE FUNCTION outbox_notify() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('outbox', '');
    RETURN NULL;
END;
$$;

CREATE TRIGGER outbox_notify
    AFTER INSERT ON outbox
    FOR EACH STATEMENT EXECUTE FUNCTION outbox_notify();

COMMIT;
