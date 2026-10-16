BEGIN;

-- The house's side of every conversion, one account per currency. It takes in
-- what customers sell, and pays out what they buy: its balances are our
-- position in each currency. Fees are revenue, in an account of their own.
INSERT INTO accounts (kind, name, currency, normal_balance) VALUES
    ('system', 'fx', 'EUR', 'credit'),
    ('system', 'fx', 'GBP', 'credit'),
    ('system', 'fx', 'JPY', 'credit'),
    ('system', 'fx', 'USD', 'credit'),
    ('system', 'fx_fees', 'EUR', 'credit'),
    ('system', 'fx_fees', 'GBP', 'credit'),
    ('system', 'fx_fees', 'JPY', 'credit'),
    ('system', 'fx_fees', 'USD', 'credit');

-- A price we promised: this much of one currency, for that much of another,
-- until it expires. Clients only ever send its ID back, never the terms.
CREATE TABLE fx_quotes (
    id             uuid        PRIMARY KEY,
    owner_id       uuid        NOT NULL,
    sell_currency  text        NOT NULL,
    sell_amount    bigint      NOT NULL CHECK (sell_amount > 0),
    buy_currency   text        NOT NULL CHECK (buy_currency <> sell_currency),
    buy_amount     bigint      NOT NULL CHECK (buy_amount > 0),
    -- in the sell currency, on top of sell_amount
    fee            bigint      NOT NULL CHECK (fee >= 0),
    -- buy per sell, in ten-billionths: fx.Rate
    rate           bigint      NOT NULL CHECK (rate > 0),
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- the conversion that used it: a quote is used once, at most
    transaction_id uuid        UNIQUE REFERENCES transactions (id)
);

COMMIT;
