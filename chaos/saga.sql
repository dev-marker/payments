-- The saga of the transfer that needed the most tries: every step, how it
-- went, and when. Each retry waits about twice as long as the one before.
SELECT step, outcome, left(detail, 52) AS detail, created_at::time(0) AS at
FROM transfer_steps
WHERE transfer_id = (
    SELECT transfer_id FROM transfer_steps WHERE outcome = 'failed'
    GROUP BY transfer_id ORDER BY count(*) DESC, max(id) DESC LIMIT 1
)
ORDER BY id;
