-- name: CreateAPIKey :one
INSERT INTO api_keys (id, name, key_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetAPIKeyByHash :one
SELECT * FROM api_keys
WHERE key_hash = $1 AND deleted_at IS NULL;

-- name: GetAPIKey :one
SELECT * FROM api_keys
WHERE id = $1 AND deleted_at IS NULL;

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = NOW()
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListAPIKeys :many
SELECT * FROM api_keys
WHERE deleted_at IS NULL
ORDER BY created_at DESC;

-- name: DeleteAPIKey :exec
UPDATE api_keys SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL;
