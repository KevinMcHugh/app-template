#!/usr/bin/env bash
# Redeploy the app on its sprite: pull main, migrate, rebuild, restart, and
# check /health. Run it on the sprite (docs/deploy.md):
#
#   sprite exec -s <app> -- bash -lc '~/<repo>/deploy/redeploy.sh'
#
# The Deploy GitHub workflow runs exactly this after every push to main, so
# a laptop is only needed for the first deploy.
#
# APP names the sprite service; REPO is the checkout on the sprite. Both
# default to the repository's directory name, so a renamed copy of the
# template needs no edits here as long as the sprite, the service and the
# checkout share that name.
set -euo pipefail

SELF="$(readlink -f "${BASH_SOURCE[0]}")"
REPO="${REPO:-$(dirname "$(dirname "$SELF")")}"
APP="${APP:-$(basename "$REPO")}"
DB_NAME="${DB_NAME:-$APP}"
SPRITE_ENV="${SPRITE_ENV:-/.sprite/bin/sprite-env}"
export DATABASE_URL="${DATABASE_URL:-postgres:///$DB_NAME?host=/var/run/postgresql&sslmode=disable}"

# Pull first, then run the pulled copy of this script, so a change to the
# deploy steps takes effect in the same deploy. --ff-only refuses to deploy
# over local edits on the sprite instead of discarding them.
if [ -z "${REDEPLOY_PULLED:-}" ]; then
  git -C "$REPO" fetch -q origin main
  git -C "$REPO" merge -q --ff-only origin/main
  REDEPLOY_PULLED=1 exec bash "$REPO/deploy/redeploy.sh" "$@"
fi
cd "$REPO"
echo "deploying $APP at $(git rev-parse --short HEAD): $(git log -1 --format=%s)"

# A checkpoint to restore if this deploy goes wrong (sprite-env checkpoints
# list, then sprite-env checkpoints restore <id>, which restarts the sprite).
# Not fatal: a deploy should not fail on it.
"$SPRITE_ENV" checkpoints create --comment "before deploying $(git rev-parse --short HEAD)" ||
  echo "warning: checkpoint failed; deploying anyway" >&2

# The postgres launcher, if it changed.
if ! cmp -s deploy/run-postgres.sh /usr/local/bin/run-postgres.sh; then
  sudo install -m 0755 deploy/run-postgres.sh /usr/local/bin/run-postgres.sh
  "$SPRITE_ENV" services restart postgres
fi

# Waking the sprite to exec starts its services, so postgres may still be
# coming up.
for _ in $(seq 30); do
  pg_isready -q -h /var/run/postgresql && break
  sleep 1
done
pg_isready -h /var/run/postgresql

dbmate --migrations-dir db/migrations --no-dump-schema up

# Build beside the live binary and swap it in, so a failed build leaves the
# running server alone, and keep the old one to roll back to.
BIN=bin/server
go build -o "$BIN.new" ./cmd/server
if [ -f "$BIN" ]; then cp -f "$BIN" "$BIN.prev"; fi
mv -f "$BIN.new" "$BIN"
"$SPRITE_ENV" services restart "$APP"

healthy() {
  for _ in $(seq 30); do
    curl -fs -o /dev/null http://localhost:8080/health && return 0
    sleep 1
  done
  return 1
}
if healthy; then
  echo "$APP is up on $(git rev-parse --short HEAD)"
  exit 0
fi

# Unhealthy: put the previous binary back. Migrations are not rolled back,
# so this only helps when the old binary still runs on the new schema.
echo "$APP failed its health check; rolling back the binary" >&2
if [ -f "$BIN.prev" ]; then
  mv -f "$BIN.prev" "$BIN"
  "$SPRITE_ENV" services restart "$APP"
  healthy && echo "previous binary is serving again" >&2
fi
exit 1
