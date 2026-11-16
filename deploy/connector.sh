#!/usr/bin/env bash
# Store an OAuth client's id and secret in a Sprites connector, so the app's
# sprite can reach the provider's token endpoint without holding the secret.
#
#   SPRITES_TOKEN=… deploy/connector.sh NAME BASE_URL TOKEN_PATH SPRITE
#
#   NAME        the connector's name in the org, e.g. myapp-google; running
#               again with the same name replaces its secret (rotation)
#   BASE_URL    the provider's origin, e.g. https://oauth2.googleapis.com
#   TOKEN_PATH  the one path the connector will forward, e.g. /token
#   SPRITE      the sprite allowed to use it (a name prefix, so pass the
#               whole name)
#
# Run it on your own machine, never on the sprite and never through Claude:
# it asks for the id and secret without echoing them, and sends them to the
# Sprites API on stdin, so they are not in your shell history, in argv, or in
# a conversation. It prints the URL to set as the app's *_TOKEN_URL
# (docs/configuration.md).
#
# The connector sends "Authorization: Basic base64(id:secret)", which is how
# Google's and eBay's token endpoints accept client credentials, and only to
# TOKEN_PATH. SPRITES_TOKEN needs full access to create connectors.
set -euo pipefail

if [[ $# -ne 4 ]]; then
  sed -n '5,13p' "$0" >&2
  exit 2
fi
name=$1 base=${2%/} path=$3 sprite=$4
: "${SPRITES_TOKEN:?set SPRITES_TOKEN to a Sprites API token with full access}"
[[ $base == https://* ]] || { echo "BASE_URL must be https" >&2; exit 2; }
[[ $path == /* ]] || { echo "TOKEN_PATH must start with /" >&2; exit 2; }
[[ $name =~ ^[A-Za-z0-9_-]+$ && $sprite =~ ^[A-Za-z0-9_-]+$ ]] || { echo "NAME and SPRITE: letters, digits, - and _ only" >&2; exit 2; }

read -rp "client id: " id
read -rsp "client secret (not echoed): " secret
echo
# Basic credentials are form-encoded before base64 (RFC 6749 2.3.1). Google
# and eBay ids and secrets never need it; refuse rather than encode wrongly.
for v in "$id" "$secret"; do
  [[ -n $v && $v =~ ^[A-Za-z0-9._~-]+$ ]] || { echo "id and secret must be non-empty and URL-safe" >&2; exit 2; }
done
basic=$(printf '%s:%s' "$id" "$secret" | base64 | tr -d '\n')
unset secret

resp=$(printf '{"name":"%s","description":"OAuth client credentials for %s","base_api_url":"%s","access_token":"%s","auth_method":"header","auth_header_prefix":"Basic","access_policy":{"name_prefix":"%s","allowed_endpoints":["%s"]}}' \
  "$name" "$sprite" "$base" "$basic" "$sprite" "$path" |
  curl -sS --fail-with-body -X POST https://api.sprites.dev/v1/oauth/connections/custom_api \
    -H "Authorization: Bearer $SPRITES_TOKEN" -H "Content-Type: application/json" --data-binary @-)
unset basic

conn=$(printf '%s' "$resp" | grep -o '"id" *: *"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
if [[ -z $conn ]]; then
  echo "unexpected response: $resp" >&2
  exit 1
fi
echo "connector $conn: $name, for sprite $sprite, forwarding only $path"
echo "token URL: https://api.sprites.dev/v1/gateway/custom_api/$conn$path"
