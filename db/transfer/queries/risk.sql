-- name: InsertDecision :exec
INSERT INTO risk_decisions (transfer_id, decision, reasons, decided_by, note)
VALUES (@transfer_id, @decision, coalesce(@reasons::text[], '{}'), @decided_by, @note);

-- name: PaidBefore :one
-- Has this user ever completed a transfer to this wallet?
SELECT EXISTS (
    SELECT 1 FROM transfers
    WHERE user_id = @user_id AND to_wallet_id = @to_wallet_id AND state = 'completed'
);
