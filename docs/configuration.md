# Configuration and secrets

**The rule: config and secrets reach the app only as environment variables on its sprite service** (`sprite-env services create … --env`). No env files: none on the sprite, none in the repo, and no code that loads one (no dotenv). Creature Lab and inventory run this way, and so does every app made from this template.

## Why

- **One place to look.** A service's environment is its whole configuration. A value that might also live in a dotfile, a launcher script or a shell profile means three places to check, and three places to forget when rotating a key.
- **Files outlive their purpose.** A file in the sprite's home directory is copied into every checkpoint, readable by anything running as that user, and one `git add -A` from being committed if it sits in the checkout.
- **Sourcing files breaks values.** A shell-sourced env file reads `&`, spaces and `$` as shell syntax. mr-ebay's first deploy had `DATABASE_URL=postgres:///db?host=…&sslmode=disable` in one, where the unquoted `&` would have run the line in the background and silently dropped the variable.
- **It is what the platform gives you.** The sprite's service manager stores the environment with the service, starts the process with it, and restores it with the sprite.

## What the template reads

| variable | used by | notes |
| --- | --- | --- |
| `DATABASE_URL` | `cmd/server` (serve, keys, invites), `make migrate` | on a sprite: `postgres:///<app>?host=/var/run/postgresql&sslmode=disable` (local socket, no password) |
| `PORT` | `cmd/server serve` | default `8080`; must match the service's `--http-port` |
| `PUBLIC_URL` | `cmd/server serve` | the app's https origin; OAuth discovery advertises it, so it must be exact |
| `TEST_DATABASE_URL` | tests only | a scratch database the end-to-end tests may wipe; never set on a sprite |

Read settings once, at startup, in `cmd/server/main.go`, or in a `FromEnv` constructor that main calls (mr-ebay's `ebay.FromEnv`), and pass the values down. Packages below main never call `os.Getenv`, so tests can configure them directly. An optional secret that is missing should leave the app running and say what is missing (mr-ebay logs "eBay is not configured" and answers searches with that error), not crash it.

## Setting and changing values on a sprite

`services create` replaces the service definition rather than merging into it, so a change means recreating the service with **every** variable:

```sh
sprite exec -s <app> -- bash -lc '/.sprite/bin/sprite-env services delete <app> && \
  /.sprite/bin/sprite-env services create <app> \
    --cmd /home/sprite/<app>/bin/server --args serve --dir /home/sprite/<app> \
    --needs postgres --http-port 8080 \
    --env "DATABASE_URL=postgres:///<app>?host=/var/run/postgresql&sslmode=disable,PORT=8080,PUBLIC_URL=https://<app>-<org>.sprites.app,SOME_SECRET=…" \
    --no-stream'
```

- `--env` is comma-separated, so no value can contain a comma.
- `redeploy.sh` only restarts the service, so the environment carries across deploys untouched.
- **Through the Fly.io MCP connector:** its `service_create` tool has no environment option, so Claude runs the same `sprite-env services create … --env …` command through the connector's `exec` instead. Never work around that gap with a file.
- **Secrets and chat:** a secret Claude sets this way passes through the conversation. For a key you would rather keep out of it, run the command yourself.

## Local development

Export variables in the shell you run `make dev` from (`export DATABASE_URL=…`). Do not create a `.env` file. `.gitignore` still lists `.env*`, but only as a backstop against committing one by accident, not as permission to use one.
