-- name: CreateQuote :one
-- It expires by the database's clock, the one LockQuote checks it with.
INSERT INTO fx_quotes (id, owner_id, sell_currency, sell_amount, buy_currency,
                       buy_amount, fee, rate, expires_at)
VALUES (@id, @owner_id, @sell_currency, @sell_amount, @buy_currency,
        @buy_amount, @fee, @rate, now() + make_interval(secs => @ttl_seconds))
RETURNING *;

-- name: LockQuote :one
-- Two conversions with the same quote take turns: the second finds it used.
SELECT *, expires_at <= now() AS expired FROM fx_quotes
WHERE id = $1 AND owner_id = $2
FOR UPDATE;

-- name: UseQuote :one
UPDATE fx_quotes SET transaction_id = $2
WHERE id = $1
RETURNING *;

-- name: GetWalletByCurrency :one
SELECT * FROM accounts
WHERE kind = 'wallet' AND owner_id = $1 AND currency = $2;
