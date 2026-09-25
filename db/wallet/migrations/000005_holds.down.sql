BEGIN;

-- The holds accounts stay: their entries still refer to them.
DROP TABLE holds;
DROP TYPE hold_status;
ALTER TABLE accounts DROP COLUMN frozen;

COMMIT;
