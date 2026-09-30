-- name: InsertNotification :execrows
-- Inserts nothing, and returns 0, when the event was handled before.
INSERT INTO notifications (event_id, user_id, recipient, subject)
VALUES ($1, $2, $3, $4)
ON CONFLICT (event_id) DO NOTHING;

-- name: InsertDeadLetter :exec
INSERT INTO dead_letters (stream, seq, subject, payload, error, deliveries)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (stream, seq) DO NOTHING;

-- name: ListDeadLetters :many
SELECT * FROM dead_letters
WHERE replayed_at IS NULL
ORDER BY failed_at;

-- name: MarkReplayed :exec
UPDATE dead_letters SET replayed_at = now()
WHERE stream = $1 AND seq = $2;
