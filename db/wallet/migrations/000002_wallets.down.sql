BEGIN;

CREATE INDEX entries_account_id_idx ON entries (account_id);
DROP INDEX entries_account_id_id_idx;

-- Dropping the column drops its check constraint and indexes with it. The
-- settlement accounts stay: their entries still refer to them.
ALTER TABLE accounts DROP COLUMN kind;
DROP TYPE account_kind;

COMMIT;
