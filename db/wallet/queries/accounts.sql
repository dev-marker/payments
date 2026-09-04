-- name: CreateAccount :one
INSERT INTO accounts (kind, owner_id, name, currency, normal_balance)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts
WHERE id = $1;

-- name: GetSystemAccount :one
SELECT * FROM accounts
WHERE kind = 'system' AND name = $1 AND currency = $2;

-- name: ListAccountsByOwner :many
SELECT * FROM accounts
WHERE owner_id = @owner_id::uuid
ORDER BY created_at;

-- name: GetAccountBalance :one
SELECT * FROM account_balances
WHERE account_id = $1;

-- name: LockAccount :one
-- Locks an account's row until the end of the transaction. Anyone else who
-- locks it waits until we commit or roll back.
SELECT id FROM accounts
WHERE id = $1
FOR UPDATE;
