BEGIN;

-- One row per refresh token we've issued. A login starts a family, and every
-- refresh adds a token to it and retires the one it replaced.
CREATE TABLE refresh_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of the token. A leaked table holds no working tokens.
    token_hash bytea       NOT NULL UNIQUE,
    -- the login this token descends from, and the token it replaced
    family_id  uuid        NOT NULL,
    parent_id  uuid        REFERENCES refresh_tokens (id) ON DELETE CASCADE,
    -- when it was exchanged for a new token: using it again is reuse
    rotated_at timestamptz,
    revoked_at timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Reuse and logout revoke a whole family at once.
CREATE INDEX refresh_tokens_family_id_idx ON refresh_tokens (family_id);
-- A user's sessions, and the foreign key from users.
CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);

COMMIT;
