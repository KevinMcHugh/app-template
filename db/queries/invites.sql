-- name: CreateInvite :one
INSERT INTO invites (id, code_hash, note, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- Unused, unexpired, undeleted invites.
-- name: ListActiveInvites :many
SELECT * FROM invites
WHERE used_at IS NULL AND deleted_at IS NULL
  AND (expires_at IS NULL OR expires_at > NOW())
ORDER BY created_at DESC;

-- Marks an invite used in one UPDATE, so two redemptions racing the same
-- code cannot both succeed.
-- name: ClaimInviteByHash :one
UPDATE invites SET used_at = NOW(), updated_at = NOW()
WHERE code_hash = $1
  AND used_at IS NULL AND deleted_at IS NULL
  AND (expires_at IS NULL OR expires_at > NOW())
RETURNING *;

-- name: SetInviteKey :exec
UPDATE invites SET api_key_id = $2, updated_at = NOW()
WHERE id = $1;

-- name: DeleteInvite :execrows
UPDATE invites SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND used_at IS NULL AND deleted_at IS NULL;
