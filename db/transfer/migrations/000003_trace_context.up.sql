BEGIN;

-- The trace each transfer, and each event, was written in: the saga's steps
-- and the relay carry it on.
ALTER TABLE transfers ADD COLUMN trace_context jsonb NOT NULL DEFAULT '{}';
ALTER TABLE outbox ADD COLUMN trace_context jsonb NOT NULL DEFAULT '{}';

COMMIT;
