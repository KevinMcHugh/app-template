-- name: CreateUser :one
INSERT INTO users (id, name)
VALUES ($1, $2)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
ORDER BY created_at, id;

-- name: GetUserIdentity :one
SELECT * FROM user_identities
WHERE provider = $1 AND subject = $2 AND deleted_at IS NULL;

-- name: CreateUserIdentity :one
INSERT INTO user_identities (id, user_id, provider, subject, email, display_name, last_login_at)
VALUES ($1, $2, $3, $4, $5, $6, NOW())
RETURNING *;

-- name: TouchUserIdentity :exec
UPDATE user_identities SET
    last_login_at = NOW(),
    email = $2,
    display_name = COALESCE(sqlc.narg(display_name), display_name),
    updated_at = NOW()
WHERE id = $1;

-- name: ListUserIdentities :many
SELECT * FROM user_identities
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at;

-- The key a user's web sessions act as: their first. Signing in with
-- Google mints one the first time; nobody ever sees it.
-- name: GetKeyForUser :one
SELECT * FROM api_keys
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at, id
LIMIT 1;

-- ---- Google sign-in ---------------------------------------------------------

-- name: CreateSSOLoginIntent :exec
INSERT INTO sso_login_intents (
    state, provider, return_to,
    client_id, redirect_uri, code_challenge, code_challenge_method, downstream_state, scope,
    invite_code, link_user_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ConsumeSSOLoginIntent :one
DELETE FROM sso_login_intents
WHERE state = $1 AND expires_at > NOW()
RETURNING *;

-- name: PurgeExpiredSSOLoginIntents :exec
DELETE FROM sso_login_intents WHERE expires_at < NOW();
