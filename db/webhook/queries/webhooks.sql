-- name: CreateEndpoint :one
INSERT INTO webhook_endpoints (id, user_id, url, event_types, secret)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListEndpoints :many
-- Never the secrets: they're shown once, when an endpoint is created.
SELECT id, url, event_types, created_at FROM webhook_endpoints
WHERE user_id = $1
ORDER BY created_at;

-- name: DeleteEndpoint :execrows
DELETE FROM webhook_endpoints WHERE id = $1 AND user_id = $2;

-- name: FanOut :execrows
-- A delivery for each of the user's endpoints that wants this event.
INSERT INTO webhook_deliveries (endpoint_id, event_id, event_type, payload)
SELECT id, @event_id::uuid, @event_type::text, @payload::bytea FROM webhook_endpoints
WHERE user_id = @user_id AND @event_type = ANY (event_types)
ON CONFLICT (endpoint_id, event_id) DO NOTHING;

-- name: ClaimDue :many
-- Takes up to $1 due deliveries, and leases them for a minute: a worker that
-- dies holding them only delays them. SKIP LOCKED lets workers share the work.
UPDATE webhook_deliveries d SET next_attempt_at = now() + interval '1 minute'
FROM webhook_endpoints e
WHERE e.id = d.endpoint_id AND d.id IN (
    SELECT id FROM webhook_deliveries
    WHERE state = 'pending' AND next_attempt_at <= now()
    ORDER BY next_attempt_at
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING d.id, d.event_id, d.payload, d.attempts, e.url, e.secret;

-- name: RecordAttempt :exec
-- The next attempt is due by the database clock, the one ClaimDue goes by.
UPDATE webhook_deliveries
SET attempts = attempts + 1, state = @state,
    next_attempt_at = now() + make_interval(secs => @retry_in_seconds),
    last_status = @last_status, last_error = @last_error, last_response_ms = @last_response_ms
WHERE id = @id;

-- name: ListDeliveries :many
SELECT d.id, d.event_id, d.event_type, d.state, d.attempts, d.last_status,
       d.last_error, d.last_response_ms, d.next_attempt_at, d.created_at
FROM webhook_deliveries d JOIN webhook_endpoints e ON e.id = d.endpoint_id
WHERE e.user_id = $1 AND d.endpoint_id = $2
ORDER BY d.created_at DESC
LIMIT 50;

-- name: Replay :execrows
-- Sends a delivery again, as new: due now, with the whole schedule ahead.
UPDATE webhook_deliveries d SET state = 'pending', attempts = 0, next_attempt_at = now()
FROM webhook_endpoints e
WHERE e.id = d.endpoint_id AND d.id = $1 AND e.user_id = $2;
