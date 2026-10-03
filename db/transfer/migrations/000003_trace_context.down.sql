BEGIN;

ALTER TABLE outbox DROP COLUMN trace_context;
ALTER TABLE transfers DROP COLUMN trace_context;

COMMIT;
