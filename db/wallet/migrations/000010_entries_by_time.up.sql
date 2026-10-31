-- A wallet's entries in time order: statements, and balances before a date.
-- The amounts are in the index too, so sums never visit the table at all.
-- CONCURRENTLY builds it while the wallet keeps writing, and can't run in a
-- transaction: this file must hold this one statement, and nothing else.
CREATE INDEX CONCURRENTLY entries_account_id_created_at_idx
    ON entries (account_id, created_at, id) INCLUDE (direction, amount);
