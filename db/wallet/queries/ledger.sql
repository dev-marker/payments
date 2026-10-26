-- name: CreateTransaction :one
INSERT INTO transactions (description)
VALUES ($1)
RETURNING *;

-- name: CreateEntry :one
INSERT INTO entries (transaction_id, account_id, direction, amount, currency)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListEntriesByAccount :many
-- Newest first, one page at a time: pass the last id you saw as before_id.
SELECT * FROM entries
WHERE account_id = @account_id
  AND id < @before_id
ORDER BY id DESC
LIMIT @page_size;

-- name: BalanceBefore :one
-- An account's balance from its entries before a moment: a statement's
-- opening balance.
SELECT coalesce(sum(CASE WHEN e.direction = a.normal_balance THEN e.amount ELSE -e.amount END), 0)::bigint
FROM entries e
JOIN accounts a ON a.id = e.account_id
WHERE e.account_id = @account_id AND e.created_at < @before;

-- name: StatementPage :many
-- A page of an account's entries posted in [since, until), oldest first, after
-- the entry after_id: keyset pagination, as fast for the last page as the first.
SELECT e.id, e.created_at, e.transaction_id, t.description, e.direction, e.amount
FROM entries e
JOIN transactions t ON t.id = e.transaction_id
WHERE e.account_id = @account_id
  AND e.created_at >= @since AND e.created_at < @until
  AND e.id > @after_id
ORDER BY e.id
LIMIT @page_size;

-- name: TrialBalance :many
-- Every ledger account's debits and credits before a moment, with customer
-- wallets as one account, and a total per currency, where the two must match.
SELECT currency, coalesce(account, 'total')::text AS account,
       sum(debit)::bigint AS debits, sum(credit)::bigint AS credits
FROM (
    SELECT a.currency, CASE a.kind WHEN 'wallet' THEN 'customer wallets' ELSE a.name END AS account,
           CASE e.direction WHEN 'debit' THEN e.amount ELSE 0 END AS debit,
           CASE e.direction WHEN 'credit' THEN e.amount ELSE 0 END AS credit
    FROM entries e
    JOIN accounts a ON a.id = e.account_id
    WHERE e.created_at < @before
) AS lines
GROUP BY currency, ROLLUP (account)
ORDER BY currency, account IS NULL, account;
