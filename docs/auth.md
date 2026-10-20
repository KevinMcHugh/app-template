# Auth

There are no tenants and no open sign-up. An **api key** is a person's identity. Every other credential (OAuth tokens, web sessions) acts as the key that approved it, and `auth.KeyID(ctx)` is how code learns who is calling.

## Credentials

| credential | prefix | minted by | used for |
| --- | --- | --- | --- |
| api key | `app_` | `server keys create --name NAME` | `Authorization: Bearer` from scripts and Claude Code; signing in to the pages |
| OAuth access token | `app_at_` | `/oauth/token` (24 h) | claude.ai custom connectors |
| OAuth refresh token | `app_rt_` | `/oauth/token` (30 d, single use) | getting a new access token |
| web session | `app_at_`, in the `app_session` cookie | `/login` (7 d, HttpOnly, SameSite=Lax) | the pages |
| invite | `app_iv_` | `server invites create --note NAME [--expires-in 168h]` | a newcomer's first OAuth connection |

Only a SHA-256 hash of any of them is stored. Raw values are printed once, by the CLI, and never again. Deleting a key (`server keys delete --id …`) cuts off every token and session it approved, because those lookups join on a live key. `server keys rotate --id …` deletes a key and mints a replacement with the same name.

The prefixes are constants in `internal/auth/auth.go`. `tools/rename.sh` changes them for your app.

## The OAuth flow (claude.ai)

1. claude.ai calls an MCP route with no credential and gets a 401 whose `WWW-Authenticate` header points at `/.well-known/oauth-protected-resource`.
2. It reads discovery, registers itself at `/oauth/register` (RFC 7591 dynamic client registration; public clients, no secret), and sends the person to `/oauth/authorize` with a PKCE S256 challenge.
3. The person pastes an api key, or an invite if they are new. The server mints a one-time code bound to the client, the redirect URI and the challenge, and redirects back.
4. claude.ai trades the code and its verifier at `/oauth/token` for an access/refresh pair. Each refresh revokes the old pair and issues a new one.

Registration stays open, because claude.ai registers before any person is involved and has nowhere to put an invite. A registered client can do nothing until someone approves it with a key or an invite.

## Invites

An invite is redeemed *for* a key. Pasting one on the authorize page claims it (one `UPDATE … WHERE used_at IS NULL`, so two racing redemptions cannot both win), mints an api key named after the invite's note, and stores the OAuth code, all in one transaction. A failure partway leaves the invite unspent. Nobody sees the minted key: the newcomer acts through the tokens it approved, `keys list` shows it by name, and `keys delete` cuts them off like anyone else.

Invites cover OAuth only. Signing in to the pages takes a raw key, so someone who joined by invite can read the public pages but needs a key minted for them to change anything there.

## Public versus private

**The spec decides.** The global `security` in `api/openapi.yaml` requires a bearer token or the session cookie. An operation with `security: []` is public. An operation that says nothing inherits the requirement, so a new operation is private until someone deliberately opens it. `server.PublicOperations()` reads the public set from the embedded spec, and `TestPublicOperations` pins it, so opening an operation is a visible change in review.

- Public API responses carry `Access-Control-Allow-Origin: *`, so another site can fetch them. No credentials are allowed cross-site, so this exposes nothing an anonymous caller could not already get. Private operations send no CORS header.
- The read-only pages use `auth.Optional`: a valid session adds the forms, and a missing, expired or revoked one reads as signed out rather than locking the page.
- Every write route on the pages uses `auth.Require` and `sameOrigin`, which refuses a POST whose `Origin` header names another site. That is a second lock after SameSite=Lax. Hiding a form from a signed-out visitor is only for looks.
- MCP (`/mcp/rpc`) always requires a credential.

## Who can do what

Any valid key can do anything the API allows. There are no roles. If your app needs them, add a column to `api_keys`, return it from `auth.Resolve`, put it on the context next to the key id, and check it in the endpoints that need it, not in the router.
