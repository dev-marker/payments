-- Two years of history, like a small production database: a deposit every
-- two minutes, a million entries. One deposit in five goes to a busy shop,
-- the wallet in :wallet, the rest to a thousand other customers. For
-- development only, as the superuser.
BEGIN;
-- Our balance triggers would check a million rows one at a time, at COMMIT.
-- These balance by construction, and the reconciliation checks every one of
-- them anyway: so for this transaction only, no triggers.
SET LOCAL session_replication_role = replica;

INSERT INTO accounts (kind, owner_id, name, currency, normal_balance)
SELECT 'wallet', gen_random_uuid(), 'USD wallet', 'USD', 'credit' FROM generate_series(1, 1000);

CREATE TEMP TABLE history ON COMMIT DROP AS
SELECT gen_random_uuid() AS tx, now() - n * interval '2 minutes' AS at, 100 + n % 9900 AS amount,
       CASE WHEN n % 5 = 0 THEN :'wallet'::uuid ELSE ids[1 + n % cardinality(ids)] END AS wallet
FROM generate_series(525600, 1, -1) AS n,
     (SELECT array_agg(id) AS ids FROM accounts WHERE kind = 'wallet' AND id <> :'wallet') AS customers;

INSERT INTO transactions (id, description, created_at) SELECT tx, 'deposit', at FROM history;
INSERT INTO entries (transaction_id, account_id, direction, amount, currency, created_at)
SELECT tx, e.account, e.direction, amount, 'USD', at
FROM history,
     (SELECT id FROM accounts WHERE kind = 'system' AND name = 'settlement' AND currency = 'USD') AS s,
     LATERAL (VALUES (s.id, 'debit'::entry_direction), (wallet, 'credit')) AS e (account, direction)
ORDER BY at;
COMMIT;

-- Fresh statistics for the planner, and a visibility map for index-only scans.
VACUUM ANALYZE accounts, transactions, entries;
