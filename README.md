# App Template

A starting point for a small Go web app that people use in a browser and Claude uses over the [Model Context Protocol](https://modelcontextprotocol.io). One binary serves the web pages, a JSON API and MCP. It deploys to a [sprites.dev](https://sprites.dev) sprite, which a GitHub workflow redeploys on every push to `main`.

It is distilled from two apps that grew this architecture: [inventory](https://github.com/KevinMcHugh/inventory), a datastore for agents, and Creature Lab, the species catalog in [mars-sim](https://github.com/KevinMcHugh/mars-sim) ([its write-up](https://github.com/KevinMcHugh/mars-sim/blob/main/docs/creature-lab.md)). It keeps what both settled on, and takes Creature Lab's choice wherever the two differ. [docs/architecture.md](docs/architecture.md) explains each choice.

The example resource is **notes** (a title and a body). It shows every layer: migration, SQL, spec, endpoints, MCP tools, pages and tests. Replace it with your own.

## What you get

- **One contract per operation.** `api/openapi.yaml` is the source of truth. oapi-codegen generates a strict server from it, and each operation is an `Endpoint` (Interact → Build → Render). REST, the MCP tools and the web pages all drive the same endpoint, so a rule lives in one place.
- **Postgres through sqlc**, migrations through dbmate, with xid ids, soft delete, and `created_at`/`updated_at`/`deleted_at` on every table.
- **Auth that claude.ai can use:** api keys for scripts and Claude Code; OAuth 2.1 (dynamic client registration and PKCE) for claude.ai custom connectors; single-use invites for newcomers; cookie sessions for the pages. The spec decides who may call what: an operation with `security: []` is public, and everything else is private.
- **Server-rendered pages with no script.** html/template behind a strict CSP. There is no npm step, so the binary is the whole deploy.
- **Tests** that pin which operations are public without needing a database, plus end-to-end tests of the API, OAuth, invites, the pages and a real MCP client session against Postgres.
- **Deploy:** sprite setup steps, a redeploy script (checkpoint, migrate, build, swap the binary, health check, roll back) and a CI workflow that runs it. Config and secrets go on the sprite service's environment, never in env files ([docs/configuration.md](docs/configuration.md)).

## Start a new app from it

```sh
# on GitHub: "Use this template", or clone and re-point origin
tools/rename.sh github.com/you/widgets widgets "Widget Works" ww
```

`rename.sh` rewrites the module path, the display name, the slug and the key prefix (`ww_` keys, `ww_at_` tokens), then regenerates. After that, follow [docs/adding-a-resource.md](docs/adding-a-resource.md) to replace notes with your own resource.

## Run it locally

Prereqs: Go (the version in `go.mod`; `go` fetches it if yours is older) and Postgres. sqlc, oapi-codegen and dbmate run through `go run …@version`, so you do not install them.

```sh
export DATABASE_URL="postgres://localhost/app?sslmode=disable"
createdb app
make dev                 # migrate, then serve on :8080
make key NAME=you        # in another terminal: mint an api key (printed once)
```

Open http://localhost:8080, sign in with the key, and add a note. From a script:

```sh
curl -H "Authorization: Bearer app_…" -d '{"title":"hi"}' localhost:8080/api/notes
curl localhost:8080/api/notes          # public: no key needed
```

Connect Claude Code: `claude mcp add --transport http app http://localhost:8080/mcp/rpc --header "Authorization: Bearer app_…"`.

## Layout

```
api/openapi.yaml        the HTTP API's contract (and which operations are public)
cmd/server/             the binary: serve, keys, invites
db/migrations/          dbmate migrations
db/queries/             sqlc queries
deploy/                 sprite scripts: run-postgres.sh, redeploy.sh
docs/                   how it works and why
internal/app/           Store, Links, shared errors and the app's name
internal/auth/          key/token formats, middleware, who-is-calling context
internal/oauth/         the OAuth 2.1 server claude.ai needs
internal/server/        Endpoint contract, the composed REST server, one package per resource
internal/appmcp/        MCP tools over the same endpoints
internal/web/           the router, the pages (templates/), end-to-end tests
internal/api/gen/       oapi-codegen output: do not edit
internal/db/gen/        sqlc output: do not edit
tools/rename.sh         make a copy your own
```

## Make targets

| target | what it does |
| --- | --- |
| `make dev` | migrate, then run the server on :8080 |
| `make generate` | regenerate sqlc and oapi-codegen output after any SQL, migration or spec change |
| `make check` | fail if the generated code is stale (CI runs it) |
| `make migrate` / `make rollback` | dbmate up / roll back the last migration |
| `make new-migration NAME=…` | create a timestamped migration file |
| `make key NAME=…` | mint an api key |
| `make invite NOTE=…` | mint a single-use invite |
| `make build` | compile to `bin/server` |
| `make test` | run the tests; set `TEST_DATABASE_URL` to a scratch database **the tests may wipe** to include the end-to-end ones |

## Docs

- [Architecture](docs/architecture.md): the layers, the request flow, and why it is built this way
- [Adding a resource](docs/adding-a-resource.md): the end-to-end recipe
- [Auth](docs/auth.md): keys, OAuth, invites, sessions, and public versus private
- [Deploy](docs/deploy.md): the sprites.dev recipe (by CLI or through the Fly.io MCP connector) and CI redeploys
- [Configuration](docs/configuration.md): config and secrets live on the sprite service's environment, never in env files
- [CLAUDE.md](CLAUDE.md): orientation for Claude Code sessions
