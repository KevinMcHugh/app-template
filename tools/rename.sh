#!/usr/bin/env bash
# Make a copy of the template your own app, in one go:
#
#   tools/rename.sh github.com/you/widgets widgets "Widget Works" ww
#
# arguments: the new Go module path, the app's slug (repo, sprite and
# service name), its display name, and the prefix for its keys and tokens
# (ww gives ww_ api keys, ww_at_ access tokens, a ww_session cookie). Run it
# once, from the repo root, on a clean checkout, then `git diff` to review.
set -euo pipefail

if [ $# -ne 4 ]; then
  sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
fi
MODULE="$1" SLUG="$2" NAME="$3" PREFIX="$4"
OLD_MODULE="github.com/KevinMcHugh/app-template"

case "$PREFIX" in
  *[!a-z0-9]* | "") echo "prefix must be lowercase letters and digits" >&2; exit 1 ;;
esac

cd "$(dirname "$0")/.."
files() { git ls-files -- "$@" | grep -v '^internal/\(api\|db\)/gen/' || true; }

# Module path: every import, go.mod, and the generator configs.
files '*.go' go.mod '*.yaml' '*.md' | xargs sed -i "s#$OLD_MODULE#$MODULE#g"
# Display name and slug: docs, the spec's title, app.Name.
files '*.md' '*.yaml' internal/app/app.go | xargs sed -i -e "s#App Template#$NAME#g" -e "s#app-template#$SLUG#g"
# Token prefixes and the session cookie. Only where they are defined or
# documented, so nothing else named app_ is touched.
sed -i "s#\"app_#\"${PREFIX}_#g" internal/auth/auth.go internal/auth/middleware.go
files api/openapi.yaml '*.md' | xargs sed -i -e "s#\bapp_\(at_\|rt_\|ac_\|iv_\|session\b\)#${PREFIX}_\1#g" -e "s#\`app_\`#\`${PREFIX}_\`#g" -e "s#\bapp_ api key#${PREFIX}_ api key#g" -e "s#\bapp_…#${PREFIX}_…#g"

# The generated code embeds the spec and imports the module, so regenerate
# rather than edit it.
make generate
gofmt -w internal/auth
go build ./...
echo "Renamed to $NAME ($MODULE). Review with: git diff"
