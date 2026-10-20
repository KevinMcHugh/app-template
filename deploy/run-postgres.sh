#!/usr/bin/env bash
# Foreground postgres launcher for the sprites.dev deploy (docs/deploy.md).
#
# The sprite services layer needs a foreground command it can supervise, so
# this wraps postgres directly rather than going through pg_ctlcluster.
#
# /var/run/postgresql is tmpfs on the sprite base image: it disappears on
# every reboot. Postgres will not start without it (it puts its socket and
# lock file there), so recreate it here before exec.
set -euo pipefail

PG_VERSION="${PG_VERSION:-18}"

sudo mkdir -p /var/run/postgresql
sudo chown postgres:postgres /var/run/postgresql
sudo chmod 2775 /var/run/postgresql

exec sudo -u postgres "/usr/lib/postgresql/$PG_VERSION/bin/postgres" \
  -D "/var/lib/postgresql/$PG_VERSION/main" \
  -c "config_file=/etc/postgresql/$PG_VERSION/main/postgresql.conf"
