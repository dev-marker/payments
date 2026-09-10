-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (user_id, token_hash, family_id, parent_id, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetRefreshTokenForUpdate :one
-- Locks the token's row: two refreshes with the same token take turns.
SELECT * FROM refresh_tokens
WHERE token_hash = $1
FOR UPDATE;

-- name: RotateRefreshToken :exec
UPDATE refresh_tokens SET rotated_at = now()
WHERE id = $1;

-- name: RevokeFamily :execrows
UPDATE refresh_tokens SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeFamilyOf :execrows
-- Revokes the family of the token with this hash: a logout.
UPDATE refresh_tokens SET revoked_at = now()
WHERE revoked_at IS NULL
  AND family_id = (SELECT t.family_id FROM refresh_tokens t WHERE t.token_hash = $1);
