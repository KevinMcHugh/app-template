-- name: CreateOAuthClient :one
INSERT INTO oauth_clients (id, redirect_uris, client_name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOAuthClient :one
SELECT * FROM oauth_clients
WHERE id = $1 AND deleted_at IS NULL;

-- name: CreateOAuthCode :exec
INSERT INTO oauth_codes (
    code_hash, client_id, api_key_id, redirect_uri,
    code_challenge, code_challenge_method, scope, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ConsumeOAuthCode :one
UPDATE oauth_codes SET consumed_at = NOW()
WHERE code_hash = $1 AND consumed_at IS NULL AND expires_at > NOW()
RETURNING *;

-- name: CreateOAuthToken :exec
INSERT INTO oauth_tokens (
    id, token_hash, client_id, api_key_id, scope, expires_at,
    refresh_token_hash, refresh_expires_at
) VALUES (
    $1, $2, sqlc.narg(client_id), $3, $4, $5,
    sqlc.narg(refresh_token_hash), sqlc.narg(refresh_expires_at)
);

-- A token stops working when its api key is deleted.
-- name: GetOAuthTokenByHash :one
SELECT oauth_tokens.* FROM oauth_tokens
JOIN api_keys ON api_keys.id = oauth_tokens.api_key_id AND api_keys.deleted_at IS NULL
WHERE token_hash = $1
  AND oauth_tokens.revoked_at IS NULL
  AND oauth_tokens.expires_at > NOW();

-- name: GetOAuthTokenByRefreshHash :one
SELECT oauth_tokens.* FROM oauth_tokens
JOIN api_keys ON api_keys.id = oauth_tokens.api_key_id AND api_keys.deleted_at IS NULL
WHERE refresh_token_hash = $1
  AND oauth_tokens.revoked_at IS NULL
  AND oauth_tokens.refresh_expires_at > NOW();

-- name: RevokeOAuthToken :exec
UPDATE oauth_tokens SET revoked_at = NOW()
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeOAuthTokenByHash :exec
UPDATE oauth_tokens SET revoked_at = NOW()
WHERE token_hash = $1 AND revoked_at IS NULL;
