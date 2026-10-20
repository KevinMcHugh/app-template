# Architecture

## Request flow

```
                     ┌──────────── internal/web (chi router) ────────────┐
browser  ──/, /notes/{id}──▶ auth.Optional ─▶ page handler ─┐            │
browser  ──POST /notes…────▶ auth.Require  ─▶ page handler ─┤            │
script   ──/api/…──────────▶ spec guard ──▶ strict server ─┼─▶ Endpoint ─┼─▶ Store ─▶ Postgres
Claude   ──/mcp/rpc────────▶ auth.Require  ─▶ MCP tool ─────┘   (Interact│   (sqlc)
claude.ai──/oauth/…, /.well-known/…  ─▶ internal/oauth             Build) │
                     └───────────────────────────────────────────────────┘
```

Every surface ends in the same `Endpoint`. REST runs all three steps (`server.Run`). A page or an MCP tool runs `Interact` and `Build` (`server.BuildViewModel`), then renders the view model as HTML or returns it as structured tool output.

## Packages

| package | holds |
| --- | --- |
| `cmd/server` | `main`: `serve`, plus the `keys` and `invites` admin commands |
| `internal/app` | `Store` (every sqlc query plus hand-written transactions), `Links` (absolute URLs for view models), `ErrNotFound`/`ErrBadInput`, `Name` |
| `internal/server` | `Endpoint`, `Run`, `BuildViewModel`; `Server`, which implements the generated `StrictServerInterface`, maps errors to typed responses, and mounts the API with the spec-driven auth guard |
| `internal/server/<resource>` | one `Endpoint` per operation, each with its own narrow `Store` interface, plus the resource's view model and its API conversion |
| `internal/appmcp` | MCP tools and the server's `Instructions` |
| `internal/web` | the router, page handlers and templates, and the end-to-end tests |
| `internal/auth` | key and token formats, hashing, `Require`/`Optional` middleware, and the caller on the context |
| `internal/oauth` | the OAuth 2.1 authorization server (discovery, registration, authorize, token, refresh) |
| `internal/api/gen`, `internal/db/gen` | generated; never edited |

## The Endpoint pattern

```go
type Endpoint[Req, M, VM, Res any] interface {
	Interact(ctx context.Context, req Req) (M, error) // the work, through a narrow Store
	Build(m M) VM                                     // pure: domain model → view model
	Render(vm VM) Res                                 // view model → generated response type
}
```

- `Req` is the generated `…RequestObject`, even when an MCP tool or a page is the caller. They fill one in themselves, so input validation lives in the endpoint and nowhere else.
- Each endpoint declares a `Store` interface with only the methods it calls (`notes.GetStore` is just `GetNote`). This keeps an endpoint's reach obvious and makes a fake easy to write. `*app.Store` satisfies every one, because it embeds `*dbgen.Queries` and sqlc generates every method.
- View models carry JSON tags, because MCP tools return them as is.
- Errors: wrap `app.ErrNotFound` or `app.ErrBadInput` with a message (`app.NotFound` converts pgx's no-rows). `server.go` turns them into the spec's 404 and 422 bodies, the pages turn them into statuses or a redirect with `?err=`, and MCP returns them as tool errors.

## Conventions

- **xid ids** in `CHAR(20)`. They are sortable, URL-safe and short enough to read aloud.
- **Soft delete everywhere.** Every live table has `created_at`, `updated_at`, `deleted_at`, and every query filters `deleted_at IS NULL`. A delete is an `UPDATE`, and `:execrows` tells the endpoint whether anything was there.
- **Partial updates.** `COALESCE(sqlc.narg(x), x)` in SQL and a pointer field in the spec (`x-go-type-skip-optional-pointer: false`) mean a PATCH sends only what changes. Everything else optional stays a plain value (`prefer-skip-optional-pointer`), so conversions need no pointer per field.
- **No tenants.** Everyone sees the same data, and the api key behind a request is recorded on what it creates (`created_by`). See "Adding tenants" below if your app needs isolation.

## Why it is this way

Most of these were learned the hard way in inventory or Creature Lab.

- **The API is generated from OpenAPI.** Creature Lab first shipped with hand-written JSON routes on the theory that it was too small to need a generator. That left no contract: a client had to read the Go to learn a body's shape, and public versus private lived in router groups nobody could audit at a glance. With the spec, the strict server will only return responses the spec declares, and `var _ apigen.StrictServerInterface = (*Server)(nil)` fails the build until every operation is implemented.
- **Auth is decided from the spec, not by oapi-codegen.** oapi-codegen v2.8's chi server does not put an operation's security requirements on the request context. Its strict middleware knows the operation, but it runs after the body is decoded, so an anonymous caller sending a bad body would get a 400 instead of a 401. So `Server.Mount` installs a chi middleware that looks up the matched route pattern in `PublicOperations()` (read from the embedded spec) and fails closed for anything not listed.
- **Server-rendered pages, no React.** Inventory ships a Vite/React UI, which means an npm build on every deploy and a second toolchain to keep current. Creature Lab's html/template pages mean the binary is the whole deploy. They also allow a `default-src 'none'` CSP, which matters when the pages show text a model wrote. If you need a rich client, add one, but consider whether forms that post and redirect will do first.
- **Generators run through `go run …@version`.** sqlc, oapi-codegen and dbmate stay out of `go.mod`, so their large dependency trees do not land in the app's module graph, and the versions are pinned in one place (the Makefile).
- **MCP is at `/mcp/rpc`, not `/mcp`.** The sprites.dev proxy intercepts the bare `/mcp` path, and POSTs hang before reaching the app.
- **The server never calls a model.** The connected client (the person's own Claude) does the thinking, so usage costs whatever their plan costs. If you add server-side generation, put it behind its own key, a stored budget, and a cost recorded per call.
- **An empty `{{define}}` does not override a `{{block}}`.** Go templates treat a whitespace-only definition as empty and keep the original. Creature Lab's login page did this and ended up with a Sign out button. Give an override some content.

## Adding tenants

Inventory is multi-tenant; this template is not. To add tenants:

1. Add a `tenants` table and a `tenant_id` column on `api_keys` and on every resource table. Scope the unique indexes by tenant.
2. Have `auth.Resolve` return the key's tenant too, and put it on the context (`auth.TenantID(ctx)`).
3. Give every query a `tenant_id = $n` filter, and every endpoint `auth.TenantID(ctx)` to pass in. Never take a tenant as a request field or a tool argument.
4. Mint tenants from the CLI (`server bootstrap --tenant NAME`), never over the API.
