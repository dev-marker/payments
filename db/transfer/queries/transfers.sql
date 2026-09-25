-- name: CreateTransfer :one
-- Returns no row when the user already used this idempotency key.
INSERT INTO transfers (user_id, idempotency_key, from_wallet_id, to_wallet_id, amount, currency)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, idempotency_key) DO NOTHING
RETURNING *;

-- name: GetTransfer :one
SELECT * FROM transfers
WHERE id = $1;

-- name: GetTransferByKey :one
SELECT * FROM transfers
WHERE user_id = $1 AND idempotency_key = $2;

-- name: ClaimTransfers :many
-- Leases due transfers to one worker, until lease_until.
UPDATE transfers
SET next_attempt_at = @lease_until, version = version + 1
WHERE id IN (
    SELECT id FROM transfers
    WHERE state IN ('pending', 'funds_held', 'compensating')
      AND next_attempt_at <= now()
    ORDER BY next_attempt_at
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: AdvanceTransfer :one
-- Only if the transfer is still exactly as we read it.
UPDATE transfers
SET state = @next_state, failure_reason = @failure_reason, attempts = 0,
    next_attempt_at = now(), version = version + 1, updated_at = now()
WHERE id = @id AND state = @state AND version = @version
RETURNING *;

-- name: RetryTransfer :execrows
UPDATE transfers
SET attempts = attempts + 1, next_attempt_at = @retry_at, version = version + 1, updated_at = now()
WHERE id = @id AND version = @version;

-- name: InsertStep :exec
INSERT INTO transfer_steps (transfer_id, step, outcome, detail)
VALUES ($1, $2, $3, $4);
