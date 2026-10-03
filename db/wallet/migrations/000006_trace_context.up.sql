-- Where each event's trace was when it was written, so that the relay can
-- carry it on. A constant default: Postgres adds the column without a rewrite.
ALTER TABLE outbox ADD COLUMN trace_context jsonb NOT NULL DEFAULT '{}';
