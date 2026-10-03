-- name: InsertOutboxEvent :exec
-- Must run in the transaction that made the event happen.
INSERT INTO outbox (event_id, subject, payload, trace_context)
VALUES ($1, $2, $3, $4);
