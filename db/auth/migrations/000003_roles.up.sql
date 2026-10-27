-- What a user may do besides move their own money: review transfers, so far.
-- The check catches a typo, like reveiwer, before it locks someone out.
ALTER TABLE users ADD COLUMN roles text[] NOT NULL DEFAULT '{}'
    CHECK (roles <@ ARRAY['reviewer']);

-- auth_app reads roles, and puts them in tokens. It can't grant one: it has
-- no UPDATE on users at all. Only an admin can, by hand.
REVOKE UPDATE ON users FROM auth_app;
