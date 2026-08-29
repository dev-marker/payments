BEGIN;

-- A wallet belongs to a customer. A system account belongs to us: money
-- enters and leaves the ledger through system accounts.
CREATE TYPE account_kind AS ENUM ('wallet', 'system');

-- Existing accounts without an owner become system accounts.
ALTER TABLE accounts ADD COLUMN kind account_kind NOT NULL DEFAULT 'wallet';
UPDATE accounts SET kind = 'system' WHERE owner_id IS NULL;
ALTER TABLE accounts ALTER COLUMN kind DROP DEFAULT;

-- Wallets always have an owner, and system accounts never do.
ALTER TABLE accounts ADD CONSTRAINT accounts_kind_owner_check
    CHECK ((kind = 'wallet') = (owner_id IS NOT NULL));

-- One wallet per owner and currency, and one system account per name and
-- currency, so code can look a system account up by its name.
CREATE UNIQUE INDEX accounts_wallet_owner_currency_idx
    ON accounts (owner_id, currency) WHERE kind = 'wallet';
CREATE UNIQUE INDEX accounts_system_name_currency_idx
    ON accounts (name, currency) WHERE kind = 'system';

-- Deposits are funded from a settlement account: the money we hold at our
-- bank. One per supported currency; a new currency is a new migration.
INSERT INTO accounts (kind, name, currency, normal_balance) VALUES
    ('system', 'settlement', 'EUR', 'debit'),
    ('system', 'settlement', 'GBP', 'debit'),
    ('system', 'settlement', 'JPY', 'debit'),
    ('system', 'settlement', 'USD', 'debit')
ON CONFLICT (name, currency) WHERE kind = 'system' DO NOTHING;

-- Listing a wallet's entries reads them in id order. This index serves that
-- directly, and replaces the index on account_id alone.
CREATE INDEX entries_account_id_id_idx ON entries (account_id, id);
DROP INDEX entries_account_id_idx;

COMMIT;
