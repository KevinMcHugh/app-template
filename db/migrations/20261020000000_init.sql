-- migrate:up

-- Conventions (docs/architecture.md): xid ids as CHAR(20), created_at /
-- updated_at / deleted_at on every live table, soft delete through
-- deleted_at, and every query filtering deleted_at IS NULL. There are no
-- tenants: api keys identify who did what. docs/architecture.md says how to
-- add them if an app needs them.

CREATE TABLE api_keys (
    id           CHAR(20)    PRIMARY KEY,
    name         TEXT        NOT NULL,
    key_hash     TEXT        NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ
);

CREATE UNIQUE INDEX api_keys_hash_active ON api_keys (key_hash) WHERE deleted_at IS NULL;

-- OAuth 2.1 for claude.ai custom connectors: dynamically registered public
-- clients, PKCE codes, and access/refresh token pairs. A token acts as the
-- api key that approved it.
CREATE TABLE oauth_clients (
    id            CHAR(20)    PRIMARY KEY,
    redirect_uris JSONB       NOT NULL,
    client_name   TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);

CREATE TABLE oauth_codes (
    code_hash             TEXT        PRIMARY KEY,
    client_id             CHAR(20)    NOT NULL REFERENCES oauth_clients(id),
    api_key_id            CHAR(20)    NOT NULL REFERENCES api_keys(id),
    redirect_uri          TEXT        NOT NULL,
    code_challenge        TEXT        NOT NULL,
    code_challenge_method TEXT        NOT NULL,
    scope                 TEXT,
    expires_at            TIMESTAMPTZ NOT NULL,
    consumed_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- client_id is NULL for a web-page session minted by /login.
CREATE TABLE oauth_tokens (
    id                 CHAR(20)    PRIMARY KEY,
    token_hash         TEXT        NOT NULL UNIQUE,
    client_id          CHAR(20)    REFERENCES oauth_clients(id),
    api_key_id         CHAR(20)    NOT NULL REFERENCES api_keys(id),
    scope              TEXT,
    expires_at         TIMESTAMPTZ NOT NULL,
    refresh_token_hash TEXT        UNIQUE,
    refresh_expires_at TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Single-use invites minted from the CLI. Redeeming one on the OAuth
-- authorize page mints the newcomer's api key, records it here, and
-- approves the connection.
CREATE TABLE invites (
    id          CHAR(20)    PRIMARY KEY,
    code_hash   TEXT        NOT NULL UNIQUE,
    note        TEXT        NOT NULL DEFAULT '',
    expires_at  TIMESTAMPTZ,
    used_at     TIMESTAMPTZ,
    api_key_id  CHAR(20)    REFERENCES api_keys(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX invites_active ON invites (created_at)
    WHERE used_at IS NULL AND deleted_at IS NULL;

-- The example resource. Replace it with your own (docs/adding-a-resource.md).
CREATE TABLE notes (
    id          CHAR(20)    PRIMARY KEY,
    title       TEXT        NOT NULL,
    body        TEXT        NOT NULL DEFAULT '',
    created_by  CHAR(20)    REFERENCES api_keys(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX notes_active ON notes (created_at DESC) WHERE deleted_at IS NULL;

-- migrate:down

DROP TABLE notes;
DROP TABLE invites;
DROP TABLE oauth_tokens;
DROP TABLE oauth_codes;
DROP TABLE oauth_clients;
DROP TABLE api_keys;
