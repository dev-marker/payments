BEGIN;

DROP TABLE risk_decisions;
DROP INDEX transfers_paid_idx;

DROP INDEX transfers_due_idx;
CREATE INDEX transfers_due_idx ON transfers (next_attempt_at)
    WHERE state IN ('pending', 'funds_held', 'compensating');

ALTER TABLE transfers DROP CONSTRAINT transfers_state_check;
ALTER TABLE transfers ADD CONSTRAINT transfers_state_check CHECK (state IN
    ('pending', 'funds_held', 'completed', 'compensating', 'failed', 'reversed'));

COMMIT;
