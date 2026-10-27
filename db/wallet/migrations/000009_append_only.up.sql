-- The ledger is append-only. The triggers say so, and now the role does too:
-- even with the triggers switched off, the wallet can't rewrite history.
REVOKE UPDATE, DELETE ON transactions, entries FROM wallet_app;
