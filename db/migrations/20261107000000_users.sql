-- migrate:up

-- People. A user is a person: they sign in with Google (user_identities,
-- as the inventory app has it) or with an api key the CLI minted for them,
-- and every key belongs to a user, so a request knows whom it acts for
-- (auth.UserID) as well as which key. What a user may see is still the
-- app's to decide: docs/architecture.md says how to give resources owners
-- or members.
--
-- The previous binary knows nothing of users, so every new column is
-- nullable and every new table is one it never reads: a rolled-back deploy
-- still runs.

CREATE TABLE users (
    id          CHAR(20)    PRIMARY KEY,
    name        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

-- A sign-in with an identity provider. A (provider, subject) pair belongs
-- to exactly one user; a user may have several.
CREATE TABLE user_identities (
    id            CHAR(20)    PRIMARY KEY,
    user_id       CHAR(20)    NOT NULL REFERENCES users(id),
    provider      TEXT        NOT NULL,
    subject       TEXT        NOT NULL,
    email         TEXT        NOT NULL,
    display_name  TEXT,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);

CREATE UNIQUE INDEX user_identities_provider_subject
    ON user_identities (provider, subject) WHERE deleted_at IS NULL;

ALTER TABLE api_keys ADD COLUMN user_id CHAR(20) REFERENCES users(id);

-- State carried across a Google sign-in: where to go after, the OAuth
-- authorize request it interrupted (a claude.ai connector), an invite to
-- redeem, or the signed-in user a new identity links to.
CREATE TABLE sso_login_intents (
    state                 TEXT        PRIMARY KEY,
    provider              TEXT        NOT NULL,
    return_to             TEXT,
    client_id             CHAR(20),
    redirect_uri          TEXT,
    code_challenge        TEXT,
    code_challenge_method TEXT,
    downstream_state      TEXT,
    scope                 TEXT,
    invite_code           TEXT,
    link_user_id          CHAR(20)    REFERENCES users(id),
    expires_at            TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX sso_login_intents_expiry ON sso_login_intents (expires_at);

-- Invites now bring in a new user, recorded beside their key.
ALTER TABLE invites ADD COLUMN user_id CHAR(20) REFERENCES users(id);

-- Backfill: keys with the same name are the same person (rotating a key
-- keeps its name), so each name becomes a user.
INSERT INTO users (id, name, created_at)
SELECT DISTINCT ON (name) id, name, created_at FROM api_keys
WHERE deleted_at IS NULL
ORDER BY name, created_at, id;

UPDATE api_keys SET user_id = users.id FROM users
WHERE api_keys.deleted_at IS NULL AND api_keys.name = users.name;

-- migrate:down

ALTER TABLE invites DROP COLUMN user_id;
DROP TABLE sso_login_intents;
ALTER TABLE api_keys DROP COLUMN user_id;
DROP TABLE user_identities;
DROP TABLE users;
