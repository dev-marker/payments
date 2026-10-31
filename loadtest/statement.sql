-- The opening balance of the shop's statement for October, and its first page,
-- exactly as the wallet asks for them, for the wallet in :wallet.
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF)
SELECT coalesce(sum(CASE WHEN e.direction = a.normal_balance THEN e.amount ELSE -e.amount END), 0)
FROM entries e JOIN accounts a ON a.id = e.account_id
WHERE e.account_id = :'wallet' AND e.created_at < '2026-10-01';

EXPLAIN (ANALYZE, BUFFERS, COSTS OFF)
SELECT e.id, e.created_at, e.transaction_id, t.description, e.direction, e.amount
FROM entries e JOIN transactions t ON t.id = e.transaction_id
WHERE e.account_id = :'wallet'
  AND (e.created_at, e.id) > ('2026-10-01', 0) AND e.created_at < '2026-11-01'
ORDER BY e.created_at, e.id
LIMIT 500;
