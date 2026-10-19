BEGIN;

-- A new state: the money is held while a person reviews the transfer. This
-- is why state was a CHECK, and not an enum: the saga grows.
ALTER TABLE transfers DROP CONSTRAINT transfers_state_check;
ALTER TABLE transfers ADD CONSTRAINT transfers_state_check CHECK (state IN
    ('pending', 'in_review', 'funds_held', 'completed', 'compensating', 'failed', 'reversed'));

-- A transfer in review is due when its review times out.
DROP INDEX transfers_due_idx;
CREATE INDEX transfers_due_idx ON transfers (next_attempt_at)
    WHERE state IN ('pending', 'in_review', 'funds_held', 'compensating');

-- Has this user paid this wallet before?
CREATE INDEX transfers_paid_idx ON transfers (user_id, to_wallet_id)
    WHERE state = 'completed';

-- Every risk decision, the engine's and the reviewers': why a transfer went
-- through, waited, or didn't. Rows are only ever added.
CREATE TABLE risk_decisions (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transfer_id uuid        NOT NULL REFERENCES transfers (id),
    decision    text        NOT NULL CHECK (decision IN
                    ('allow', 'review', 'deny', 'approve', 'reject')),
    reasons     text[]      NOT NULL DEFAULT '{}',
    decided_by  text        NOT NULL, -- the risk engine, or a reviewer
    note        text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX risk_decisions_transfer_id_idx ON risk_decisions (transfer_id, id);

COMMIT;
