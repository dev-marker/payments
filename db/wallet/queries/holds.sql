-- name: CreateHold :one
INSERT INTO holds (id, wallet_id, amount, currency, hold_tx_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetHold :one
SELECT * FROM holds
WHERE id = $1;

-- name: LockHold :one
SELECT * FROM holds
WHERE id = $1
FOR UPDATE;

-- name: SettleHold :one
UPDATE holds
SET status = $2, settled_to = $3, settle_tx_id = $4, settled_at = now()
WHERE id = $1 AND status = 'held'
RETURNING *;
