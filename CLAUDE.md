# CLAUDE.md

Orientation for Claude Code sessions in this repo. See @README.md for the pitch and @docs/architecture.md for how it fits together.

## Never edit

- `internal/api/gen/**`: regenerate with `make generate` after editing `api/openapi.yaml`.
- `internal/db/gen/**`: regenerate with `make generate` after editing `db/queries/*.sql` or a migration.

To change something in a generated file, edit its source (the spec, a query or a migration) and regenerate. If the generator itself needs tweaking, edit `sqlc.yaml` or `oapi-codegen.yaml`. CI runs `make check`, which fails when generated code is stale.

## Core invariants

- **IDs are xids.** Stored as `CHAR(20)`, created with `xid.New().String()`.
- **Soft delete.** Every live table has `created_at`, `updated_at`, `deleted_at`. Every query filters `deleted_at IS NULL`. Deletes set `deleted_at = NOW()`.
- **The spec decides who may call what.** An operation is private unless its spec entry says `security: []`. Never make a route public in the router. `TestPublicOperations` pins the public set, so update it when you deliberately change the set.
- **One endpoint per operation, shared by every surface.** REST runs it with `server.Run`. MCP tools and pages run it with `server.BuildViewModel`, then present the view model their own way. Put a rule in the endpoint, never in a tool or a page handler.
- **Who is calling comes from the context.** `auth.KeyID(ctx)` (or `auth.KeyIDPtr` for a nullable `created_by`). Never take it as a tool argument or a request field.
- **Credentials are minted by the CLI only** (`server keys …`, `server invites …`). Do not add API or MCP operations that create keys or invites.
- **Pages run no script.** The CSP is `default-src 'none'`. Use forms that post and redirect, not JavaScript.

## The Endpoint pattern

`Endpoint[Req, M, VM, Res]` (in `internal/server/endpoint.go`):

- `Interact(ctx, req) (M, error)` does the work through a narrow `Store` interface the endpoint declares itself, listing only the methods it calls. `*app.Store` satisfies them all.
- `Build(M) VM` is a pure transformation to the view model, which carries JSON tags because MCP returns it as is.
- `Render(VM) Res` wraps the view model in the generated response type.

Endpoints return `app.ErrNotFound` or `app.ErrBadInput` (wrapped with `%w` and a message). `internal/server/server.go` maps them to the spec's 404 and 422, the pages map them to the same statuses, and MCP returns them as tool errors.

## Adding a resource

Follow @docs/adding-a-resource.md. In short: migration → queries → spec → `make generate` → `internal/server/<resource>/` → wire into `server.go` → MCP tools in `internal/appmcp` → pages in `internal/web` → a case in `e2e_test.go`.

## Testing

- `go test ./...` runs everything that needs no database.
- Set `TEST_DATABASE_URL` to a scratch Postgres database **that the tests may wipe** (each test drops and recreates the `public` schema) to include the end-to-end tests in `internal/web/e2e_test.go`.
- Run `go vet ./...` and `go test ./...` before calling a change done.

## Migrations

`db/migrations/YYYYMMDDHHMMSS_description.sql` in dbmate format (`-- migrate:up` / `-- migrate:down`). Create one with `make new-migration NAME=…`. Never edit an applied migration; add a new one. The deploy's rollback swaps only the binary, so write migrations that the previous binary can still run on.

## Commit style

- One-line subject, present tense, lowercase after the type prefix (`feat:`, `fix:`, `chore:`, `docs:`).
- The body explains *why*, not *what* the diff shows.
- Avoid apostrophes in commit messages composed with a `$(cat <<'EOF' …)` heredoc.
- A change that makes a doc wrong fixes the doc in the same commit.
