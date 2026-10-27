-- Sensitive actions: who did what, to what, when, and from where. Rows are
-- only ever added: transfer_app may INSERT, and nothing else, not even SELECT.
CREATE TABLE audit_log (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at         timestamptz NOT NULL DEFAULT now(),
    actor      text        NOT NULL, -- who: a user's ID, or a database role
    action     text        NOT NULL, -- what, like transfer.approve
    target     text        NOT NULL, -- to what: a transfer's ID, say
    details    jsonb       NOT NULL DEFAULT '{}',
    client     text        NOT NULL DEFAULT '', -- from where: the service that called us
    request_id text        NOT NULL DEFAULT '',
    trace_id   text        NOT NULL DEFAULT ''
);
REVOKE ALL ON audit_log FROM transfer_app;
GRANT INSERT ON audit_log TO transfer_app;

-- And a transfer's history, and every risk decision, are only ever added to.
REVOKE UPDATE, DELETE ON transfer_steps, risk_decisions FROM transfer_app;
