# Configuration and secrets

**The rule: secrets live in Sprites connectors, and everything else reaches the app as environment variables on its sprite service** (`sprite-env services create … --env`). No env files: none on the sprite, none in the repo, and no code that loads one (no dotenv). No secret on the service's environment either, once a connector can hold it.

## Secrets: Sprites connectors

A [Sprites connector](https://docs.sprites.dev/concepts/connectors/) keeps a credential in the Sprites control plane, encrypted, and adds it to requests on their way out. The app calls `https://api.sprites.dev/v1/gateway/custom_api/<connection id>/<path>` with no credential. The gateway knows which sprite is calling from its platform identity, checks the connector's access policy (which sprites, and which paths), and forwards to the provider with the stored credential attached. The secret is never on the sprite, so nothing there can read it, log it, copy it into a checkpoint, or leak it. Rotating it is one call, with no redeploy.

The catalog has no provider for signing in with Google or for eBay, so these use the **Custom API** connector, which holds one token and sends it in one header to one base URL. That is enough for an OAuth client, because its secret is used in exactly one place: the token endpoint, where it authenticates as HTTP Basic `client_id:client_secret`. So the app's connector holds `base64(id:secret)`, sends it as `Authorization: Basic …`, and forwards only the token path. Everything after that uses the short-lived token the endpoint returns, which the app keeps in memory and sends to the provider directly.

| integration | connector base URL | the one path it forwards | the app's setting |
| --- | --- | --- | --- |
| Sign in with Google | `https://oauth2.googleapis.com` | `/token` | `GOOGLE_TOKEN_URL` |

An app made from the template adds a row for each provider it calls the same way (mr-ebay: eBay's client-credentials token, `https://api.ebay.com`, `/identity/v1/oauth2/token`, `EBAY_TOKEN_URL`).

Create or rotate one on your own machine:

```sh
SPRITES_TOKEN=… deploy/connector.sh <app>-google https://oauth2.googleapis.com /token <app>
```

It asks for the client id and secret without echoing them and sends them to the Sprites API on stdin, then prints the token URL to put on the service's environment. Running it again with the same name replaces the secret in place. The access policy it sets lets only sprites whose name starts with `<app>` use the connector, and only for that one path, so even code on the sprite that goes wrong can do nothing with it but mint tokens the app could mint anyway. (A name prefix also matches `<app>-staging`; give the sprites distinct prefixes if that matters.) `SPRITES_TOKEN` is the token from [sprites.dev/account](https://sprites.dev/account), the same kind CI deploys with.

Run it yourself, not through Claude: a secret typed into a conversation has left your hands. Claude can set up everything around it (the service, the URL), and check the connector from the sprite with `curl -sS https://api.sprites.dev/v1/gateway/list`, which names the connectors the sprite may use and never shows a credential.

**When a secret cannot go in a connector**, such as one the app itself must hold (a signing key, a database password for a remote database), it goes on the service's environment like any other value, and the reasons below apply. Prefer a design that keeps it off the sprite.

## Everything else: the service's environment

- **One place to look.** A service's environment is its whole configuration. A value that might also live in a dotfile, a launcher script or a shell profile means three places to check, and three places to forget.
- **Files outlive their purpose.** A file in the sprite's home directory is copied into every checkpoint, readable by anything running as that user, and one `git add -A` from being committed if it sits in the checkout.
- **Sourcing files breaks values.** A shell-sourced env file reads `&`, spaces and `$` as shell syntax. mr-ebay's first deploy had `DATABASE_URL=postgres:///db?host=…&sslmode=disable` in one, where the unquoted `&` would have run the line in the background and silently dropped the variable.
- **It is what the platform gives you.** The sprite's service manager stores the environment with the service, starts the process with it, and restores it with the sprite.

## What the template reads

| variable | used by | notes |
| --- | --- | --- |
| `DATABASE_URL` | `cmd/server` (serve, keys, invites), `make migrate` | on a sprite: `postgres:///<app>?host=/var/run/postgresql&sslmode=disable` (local socket, no password) |
| `PORT` | `cmd/server serve` | default `8080`; must match the service's `--http-port` |
| `PUBLIC_URL` | `cmd/server serve` | the app's https origin; OAuth discovery advertises it, so it must be exact |
| `GOOGLE_CLIENT_ID` | `cmd/server serve` | not a secret: it is in every sign-in link. Set, with a way to authenticate (below), it turns on "Sign in with Google" ([auth.md](auth.md)). An OAuth client of type "Web application" in Google Cloud, with `PUBLIC_URL` + `/auth/google/callback` as its authorized redirect URI |
| `GOOGLE_TOKEN_URL` | `cmd/server serve` | on a sprite: the Google connector's token URL, which `deploy/connector.sh` prints. Default `https://oauth2.googleapis.com/token` |
| `GOOGLE_CLIENT_SECRET` | `cmd/server serve` | **off a sprite only**: local development, where there is no gateway. Never set on a sprite, because the connector holds it |
| `TEST_DATABASE_URL` | tests only | a scratch database the end-to-end tests may wipe; never set on a sprite |

Read settings once, at startup, in `cmd/server/main.go`, or in a `FromEnv` constructor that main calls (mr-ebay's `ebay.FromEnv`), and pass the values down. Packages below main never call `os.Getenv`, so tests can configure them directly. An optional integration that is not configured should leave the app running and say what is missing (mr-ebay logs "eBay is not configured" and answers searches with that error), not crash it.

The code for an OAuth client follows from the connector: send client credentials as HTTP Basic, and only when the app holds the secret itself. With no secret, the token URL is a connector, and the request leaves with no credential. Tests stand a fake connector in front of the fake provider that fails if the app sends a credential of its own (`internal/web/signin_test.go`).

## Setting and changing values on a sprite

`services create` replaces the service definition rather than merging into it, so a change means recreating the service with **every** variable:

```sh
sprite exec -s <app> -- bash -lc '/.sprite/bin/sprite-env services delete <app> && \
  /.sprite/bin/sprite-env services create <app> \
    --cmd /home/sprite/<app>/bin/server --args serve --dir /home/sprite/<app> \
    --needs postgres --http-port 8080 \
    --env "DATABASE_URL=postgres:///<app>?host=/var/run/postgresql&sslmode=disable,PORT=8080,PUBLIC_URL=https://<app>-<org>.sprites.app,GOOGLE_CLIENT_ID=….apps.googleusercontent.com,GOOGLE_TOKEN_URL=https://api.sprites.dev/v1/gateway/custom_api/conn_…/token" \
    --no-stream'
```

- `--env` is comma-separated, so no value can contain a comma.
- `redeploy.sh` only restarts the service, so the environment carries across deploys untouched.
- Rotating a secret in a connector changes nothing here.
- **Through the Fly.io MCP connector:** its `service_create` tool has no environment option, so Claude runs the same `sprite-env services create … --env …` command through the connector's `exec` instead. Never work around that gap with a file. None of these values is a secret, so they can pass through a conversation.

## Moving a secret from the environment to a connector

1. Create the connector with `deploy/connector.sh`.
2. Recreate the service with the token URL added and the secret left out.
3. Check the integration end to end (for Google, sign in), then rotate the secret at the provider and run `deploy/connector.sh` again with the new one, since the old one sat in the service's environment and its checkpoints.

## Local development

Export variables in the shell you run `make dev` from (`export DATABASE_URL=…`). The gateway answers only sprites, so locally an integration needs its secret exported too (`GOOGLE_CLIENT_SECRET`), or stays off; use a separate OAuth client for development, so the production secret stays in its connector. Do not create a `.env` file. `.gitignore` still lists `.env*`, but only as a backstop against committing one by accident, not as permission to use one.
