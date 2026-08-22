-- name: CreateAccount :one
INSERT INTO accounts (owner_id, name, currency, normal_balance)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts
WHERE id = $1;

-- name: ListAccountsByOwner :many
SELECT * FROM accounts
WHERE owner_id = @owner_id::uuid
ORDER BY created_at;

-- name: GetAccountBalance :one
SELECT * FROM account_balances
WHERE account_id = $1;
