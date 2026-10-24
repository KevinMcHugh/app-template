# Deploy (sprites.dev)

The app runs on a [sprites.dev](https://sprites.dev) sprite: a small persistent VM that pauses when idle and wakes on a request. Postgres runs on the same sprite as a supervised service. The binary is built there from a git checkout, and CI redeploys by running one script over `sprite exec`.

The recipe below uses `myapp` for the sprite, the service, the checkout directory and the database. They share a name on purpose: `deploy/redeploy.sh` and the CI workflow derive all four from it.

## First deploy

```sh
APP=myapp
REPO_URL=https://github.com/you/myapp.git

sprite create --skip-console $APP
sprite use $APP
sprite exec -- bash -lc 'sudo apt-get update -qq && sudo apt-get install -y -qq postgresql postgresql-contrib git'
sprite exec -- bash -lc 'sudo curl -fsSL -o /usr/local/bin/dbmate \
  https://github.com/amacneil/dbmate/releases/latest/download/dbmate-linux-amd64 && sudo chmod +x /usr/local/bin/dbmate'
sprite exec -- bash -lc "cd ~ && git clone $REPO_URL $APP"

# Postgres as a sprite service. The launcher recreates /var/run/postgresql,
# which is tmpfs on the sprite image and is wiped on every reboot. Set
# PG_VERSION in the service env if apt installed something other than 18.
sprite exec -- bash -lc "sudo install -m 0755 ~/$APP/deploy/run-postgres.sh /usr/local/bin/run-postgres.sh"
sprite exec -- bash -lc '/.sprite/bin/sprite-env services create postgres --cmd /usr/local/bin/run-postgres.sh --no-stream'
sprite exec -- bash -lc "sudo -u postgres createuser -s sprite; sudo -u postgres createdb -O sprite $APP"

# Config goes on the service (--env) and nowhere else: no env files, no
# exports in ~/.profile. See docs/configuration.md. One-off commands get
# DATABASE_URL inline.
DSN="postgres:///$APP?host=/var/run/postgresql&sslmode=disable"
sprite exec -- bash -lc "cd ~/$APP && DATABASE_URL='$DSN' dbmate --migrations-dir db/migrations --no-dump-schema up && go build -o bin/server ./cmd/server"
sprite exec -- bash -lc "DATABASE_URL='$DSN' ~/$APP/bin/server keys create --name you"   # SAVE THE KEY

# PUBLIC_URL must be the https URL, because OAuth discovery advertises it.
# Add any secrets the app needs to the same --env list.
sprite exec -- bash -lc "/.sprite/bin/sprite-env services create $APP \
  --cmd /home/sprite/$APP/bin/server --args serve --dir /home/sprite/$APP \
  --needs postgres --http-port 8080 \
  --env 'DATABASE_URL=$DSN,PORT=8080,PUBLIC_URL=https://$APP-<org>.sprites.app' \
  --no-stream"
sprite config update -s $APP --url-auth public
```

The sprite needs the Go version in `go.mod`. `go build` fetches that toolchain itself if the installed Go is older.

For someone else, mint an invite they paste when connecting Claude: `sprite exec -s myapp -- bash -lc "DATABASE_URL='postgres:///myapp?host=/var/run/postgresql&sslmode=disable' ~/myapp/bin/server invites create --note ana --expires-in 168h"`.

**Config and secrets** live only in the service's `--env`. Changing one means recreating the service with every variable; [configuration.md](configuration.md) has the command and the reasons.

## Deploying through the Fly.io MCP connector

Claude can do the first deploy itself with the Fly.io connector (`create_sprite`, `exec`, `service_create`, `checkpoint_create`), as it did for mr-ebay. Three things differ from the CLI recipe:

- **Names.** The connector can only create and reach sprites whose names start with its grant's prefix (for example `mcp-`), so name the sprite, the checkout, the service and the database all `mcp-myapp`. `redeploy.sh` derives them from the checkout's directory name, so nothing else changes.
- **Service environment.** The connector's `service_create` has no environment option. Create the app's service with `sprite-env services create … --env …` through the connector's `exec`, exactly as in the recipe above. Do not route around the gap with an env file and a launcher script: mr-ebay's first deploy did, and it was taken out ([configuration.md](configuration.md)).
- **URL auth.** A new sprite's URL is private, and the connector cannot change that. Someone with the CLI runs `sprite config update -s mcp-myapp --url-auth public` once, before claude.ai can connect.

## Redeploys

A push to `main` redeploys, once CI is set up:

1. In the GitHub repo's Settings → Secrets and variables → Actions, add the **variable** `SPRITE_NAME` (here `myapp`).
2. Create a token at [sprites.dev/account](https://sprites.dev/account) for the org that owns the sprite, and save it as the **secret** `SPRITES_TOKEN`.

Until `SPRITE_NAME` is set, the deploy job is skipped and only the tests run. To redeploy without a push, use "Run workflow" on the Actions tab, which works from the GitHub mobile app. By hand, the script is the whole redeploy: `sprite exec -s myapp -- bash -lc '~/myapp/deploy/redeploy.sh'`.

`deploy/redeploy.sh`:

1. fast-forwards the checkout to `BRANCH` on `origin` and re-runs the pulled copy of itself, so a change to the deploy steps applies in the same deploy. `BRANCH` defaults to whatever the checkout has out. CI sets it to `main`, which also moves a checkout that was on another branch (say, the feature branch a first deploy came from) over to `main`. Like the merge, the switch refuses to run over local edits;
2. takes a sprite checkpoint named for the commit (`sprite-env checkpoints list`, then `sprite-env checkpoints restore <id>` to go back);
3. reinstalls `run-postgres.sh` and restarts postgres, but only if that file changed;
4. waits for postgres, then runs `dbmate up`;
5. builds `bin/server.new`, swaps it in (keeping `.prev`), and restarts the service;
6. polls `/health` for 30 s. If it never answers, it puts `.prev` back, restarts again, and fails the run.

## Why it is built this way

- **The binary is built on the sprite, not in CI.** Deploying stays "run one script", whether CI or a person runs it, and the binary's VCS stamp matches the checkout it runs from.
- **Deploys run from GitHub Actions, as Creature Lab's do.** `.github/workflows/ci.yml` tests every push and pull request against a Postgres service container. After a push to `main`, its deploy job installs the `sprite` CLI on the runner, signs in with `SPRITES_TOKEN`, and runs `redeploy.sh` on the sprite over `sprite exec`. Only `main` deploys; a branch deploys by hand or through the Fly.io connector. The same steps have deployed Creature Lab from mars-sim's workflow since its first run.
- **CI pushes; the sprite does not poll.** A paused sprite's timers do not fire, and keeping it awake to poll costs more than it is worth. `sprite exec` wakes it.
- **Deploys queue and never cancel each other** (`cancel-in-progress: false`). A deploy killed after migrating but before restarting is worse than one that waits.
- **`--ff-only`, not `reset --hard`.** If someone edits the checkout on the sprite, the deploy stops instead of throwing the edit away.
- **The rollback swaps only the binary.** Migrations stay applied, so it helps only when the old binary still runs on the new schema. Write migrations that way: add columns with defaults, and drop things one deploy after the code stops using them.

## Connect Claude

- **claude.ai:** add a custom connector at `https://myapp-<org>.sprites.app/mcp/rpc` with automatic client registration, then paste an `app_` key when asked, or an `app_iv_` invite the first time.
- **Claude Code:** `claude mcp add --transport http myapp https://myapp-<org>.sprites.app/mcp/rpc --header "Authorization: Bearer app_…"`.
