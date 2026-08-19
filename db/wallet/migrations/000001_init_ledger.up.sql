BEGIN;

-- Debit or credit: the two sides of every entry.
CREATE TYPE entry_direction AS ENUM ('debit', 'credit');

-- An account is anything that holds money: a user's wallet, or a
-- system account, like the bank account that deposits come from.
CREATE TABLE accounts (
    id             uuid            PRIMARY KEY DEFAULT gen_random_uuid(),
    -- the user from the auth service; NULL for system accounts
    owner_id       uuid,
    name           text            NOT NULL CHECK (name <> ''),
    currency       text            NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    normal_balance entry_direction NOT NULL,
    created_at     timestamptz     NOT NULL DEFAULT now(),
    UNIQUE (id, currency)
);

CREATE INDEX accounts_owner_id_idx ON accounts (owner_id);

-- The journal: one row per movement of money.
CREATE TABLE transactions (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    description text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- One line of a transaction: an amount debited or credited to an account.
CREATE TABLE entries (
    id             bigint          GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id uuid            NOT NULL REFERENCES transactions (id),
    account_id     uuid            NOT NULL,
    direction      entry_direction NOT NULL,
    amount         bigint          NOT NULL CHECK (amount > 0),
    currency       text            NOT NULL,
    created_at     timestamptz     NOT NULL DEFAULT now(),
    -- an entry's currency must match its account's currency
    FOREIGN KEY (account_id, currency) REFERENCES accounts (id, currency)
);

-- Postgres does not index foreign keys for us.
CREATE INDEX entries_transaction_id_idx ON entries (transaction_id);
CREATE INDEX entries_account_id_idx ON entries (account_id);

-- A transaction must have entries, and its debits must equal its credits
-- in every currency. Checked at COMMIT, once all the entries exist.
CREATE FUNCTION check_transaction_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    tx_id uuid;
BEGIN
    IF TG_TABLE_NAME = 'transactions' THEN
        tx_id := NEW.id;
    ELSE
        tx_id := NEW.transaction_id;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM entries WHERE transaction_id = tx_id) THEN
        RAISE EXCEPTION 'transaction % has no entries', tx_id
            USING ERRCODE = 'check_violation';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM entries
        WHERE transaction_id = tx_id
        GROUP BY currency
        HAVING sum(CASE direction WHEN 'debit' THEN amount ELSE -amount END) <> 0
    ) THEN
        RAISE EXCEPTION 'transaction % is not balanced', tx_id
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER transactions_balanced
    AFTER INSERT ON transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_transaction_balanced();

CREATE CONSTRAINT TRIGGER entries_balanced
    AFTER INSERT ON entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_transaction_balanced();

-- The ledger is append-only: mistakes are fixed with a reversing transaction.
CREATE FUNCTION forbid_ledger_changes() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only, post a reversing transaction instead', TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER transactions_append_only
    BEFORE UPDATE OR DELETE ON transactions
    FOR EACH ROW EXECUTE FUNCTION forbid_ledger_changes();

CREATE TRIGGER entries_append_only
    BEFORE UPDATE OR DELETE ON entries
    FOR EACH ROW EXECUTE FUNCTION forbid_ledger_changes();

-- Balances are derived from entries, never stored.
CREATE VIEW account_balances AS
SELECT
    a.id AS account_id,
    a.name,
    a.currency,
    coalesce(sum(
        CASE WHEN e.direction = a.normal_balance THEN e.amount ELSE -e.amount END
    ), 0)::bigint AS balance
FROM accounts a
LEFT JOIN entries e ON e.account_id = a.id
GROUP BY a.id;

COMMIT;
