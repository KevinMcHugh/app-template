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
| `internal/app` | `Store` (every sqlc query plus `InTx` and hand-written transactions), `Guard` (the stale-write check `InTx` runs), `Paginate` and `Page` (keyset pages of a list), `Links` (absolute URLs for view models), `ErrNotFound`/`ErrBadInput`/`ErrConflict`, `Name` |
| `internal/server` | `Endpoint`, `Run`, `BuildViewModel`, and the `Guarded` and `Paged` extensions; `Server`, which implements the generated `StrictServerInterface`, maps errors to typed responses, and mounts the API with the spec-driven auth guard |
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
- Errors: wrap `app.ErrNotFound` or `app.ErrBadInput` with a message (`app.NotFound` converts pgx's no-rows). `server.go` turns them into the spec's 404 and 422 bodies (and `app.ErrConflict` into a 409), the pages turn them into statuses or a redirect with `?err=`, and MCP returns them as tool errors.

### Stale writes: `Guarded`

When two people (or a person and their Claude) have the same note open, the second save would silently replace the first. So every PATCH takes the `updatedAt` of the thing as the caller last loaded it, and is refused with a 409 if it has changed since. The caller reloads, sees the other change, and makes theirs again.

An endpoint opts in by implementing one more method beside the three above:

```go
type Guarded[Req any] interface {
	Guard(req Req) app.Guard // the row it changes and the updatedAt the caller sent
}

func (UpdateEndpoint) Guard(req apigen.UpdateNoteRequestObject) app.Guard {
	return app.Guard{Kind: "note", ID: req.NoteId, Seen: req.Body.UpdatedAt}
}
```

and doing its write in `InTx`. `Run` and `BuildViewModel` put the guard on the context, and `Store.InTx` checks it first thing in the transaction: it locks the row (`LockForWrite`, in `db/queries/guard.sql`, `SELECT … FOR UPDATE`), compares its `updated_at` with what the caller saw, and returns `app.ErrConflict` if they differ, or `app.ErrBadInput` if the caller sent none. The lock is held until the write commits, so two saves racing from the same copy cannot both pass; exactly one lands. Because the check is inside `InTx`, it comes after validation, so a bad field is still a 422.

Every surface sends the version back: API clients in the PATCH body, the note page in a hidden `updated_at` field on its edit form (a stale save redirects back with a message), and the `update_note` tool as a required `updatedAt` argument (a stale one is a tool error telling Claude to read the note again).

Three things keep it from being forgotten or skipped:

- `server.go` runs every PATCH through `RunGuarded`, which takes a `GuardedEndpoint`, so an update endpoint without `Guard` does not compile.
- `Run` fails closed if a guarded endpoint finished without opening a transaction, because then nothing compared the versions (`TestUncheckedGuardFailsClosed`).
- `TestEveryPatchIsGuarded` checks the spec: every PATCH body requires `updatedAt`, and every PATCH declares the 409.

The check runs once per request, in the first `InTx`; a second transaction in the same request would otherwise take the first one's write for someone else's. Deletes are not guarded.

### Lists: `Paged`

Every list operation returns a page: it takes `limit` (1 to 200, 100 if left out) and `after` (the `next` of the page before), and its body carries `next`, empty on the last page. Pages are keyset, not offset: a list's query orders by a key that is unique per row (its sort columns, ending in the id) and starts after the key of the last row the caller saw, so a page is as cheap at the end of a list as at the start, and a row added or deleted while someone pages neither repeats nor skips the rows around it.

A list endpoint implements one more method beside the three, naming its list and reading the page off the request, and its model is that one page:

```go
type Paged[Req any] interface {
	Page(req Req) app.PageRequest // the list's name, and the limit and after the caller sent
}

func (ListEndpoint) Page(req apigen.ListNotesRequestObject) app.PageRequest {
	return app.PageRequest{List: "note", Limit: req.Params.Limit, After: req.Params.After}
}

func (e ListEndpoint) Interact(ctx context.Context, req apigen.ListNotesRequestObject) (app.Page[dbgen.Note], error) {
	return app.Paginate(e.Page(req),
		func(after *newest, limit int32) ([]dbgen.Note, error) { /* PageNotes, after the key */ },
		func(n dbgen.Note) newest { return newest{n.CreatedAt.Time, n.ID} })
}
```

`app.Paginate` does the rest: it checks the limit, decodes the cursor (refusing one from another list, or one that is not a cursor, with `ErrBadInput`), asks the query for one row more than the page so it knows whether there is a next page without counting, and encodes the last row's key as `next`. `Build` and `Render` map the page with `app.MapPage`. The cursor is the key in base64 JSON with the list's name: opaque to callers, and a position, not a permission, since the query still filters to what the caller may see. If the resource has members, call `Access` before `Paginate`, so a non-member with a bad limit still gets a 404.

Every surface pages: the API by `limit` and `after`, `list_notes` by the same arguments (and the server's instructions tell Claude to follow `next`), and the index page by an Older notes link that carries `after`. Code that needs a whole list follows `next` with `server.All`, or keeps a `List…` query of every row beside the `Page…` one.

Three things keep a list from going unpaged:

- `server.go` runs every list through `RunPaged`, which takes a `PagedEndpoint` (a `Paged` endpoint whose model is an `app.Page`), so a list endpoint that returns a slice does not compile.
- `Run` fails closed if a paged endpoint returned more than the caller asked for (`TestOverlongPageFailsClosed`).
- `TestEveryListIsPaged` checks the spec: every `list…` operation takes `limit` and `after`, its body requires `next`, and it declares the 422.

## Conventions

- **xid ids** in `CHAR(20)`. They are sortable, URL-safe and short enough to read aloud.
- **Soft delete everywhere.** Every live table has `created_at`, `updated_at`, `deleted_at`, and every query filters `deleted_at IS NULL`. A delete is an `UPDATE`, and `:execrows` tells the endpoint whether anything was there.
- **Partial updates.** `COALESCE(sqlc.narg(x), x)` in SQL and a pointer field in the spec (`x-go-type-skip-optional-pointer: false`) mean a PATCH sends only what changes. Everything else optional stays a plain value (`prefer-skip-optional-pointer`), so conversions need no pointer per field. The one field a PATCH always sends is `updatedAt`, as loaded, so a stale write is refused (see `Guarded` above).
- **Users, no tenants.** A user is a person, and every api key belongs to one (`auth.UserID`). Everyone sees the same data, and the api key behind a request is recorded on what it creates (`created_by`). See "Adding owners and members" or "Adding tenants" below if your app needs isolation.

## Why it is this way

Most of these were learned the hard way in inventory or Creature Lab.

- **The API is generated from OpenAPI.** Creature Lab first shipped with hand-written JSON routes on the theory that it was too small to need a generator. That left no contract: a client had to read the Go to learn a body's shape, and public versus private lived in router groups nobody could audit at a glance. With the spec, the strict server will only return responses the spec declares, and `var _ apigen.StrictServerInterface = (*Server)(nil)` fails the build until every operation is implemented.
- **Auth is decided from the spec, not by oapi-codegen.** oapi-codegen v2.8's chi server does not put an operation's security requirements on the request context. Its strict middleware knows the operation, but it runs after the body is decoded, so an anonymous caller sending a bad body would get a 400 instead of a 401. So `Server.Mount` installs a chi middleware that looks up the matched route pattern in `PublicOperations()` (read from the embedded spec) and fails closed for anything not listed.
- **Server-rendered pages, no React.** Inventory ships a Vite/React UI, which means an npm build on every deploy and a second toolchain to keep current. Creature Lab's html/template pages mean the binary is the whole deploy. They also allow a `default-src 'none'` CSP, which matters when the pages show text a model wrote. If you need a rich client, add one, but consider whether forms that post and redirect will do first.
- **Generators run through `go run …@version`.** sqlc, oapi-codegen and dbmate stay out of `go.mod`, so their large dependency trees do not land in the app's module graph, and the versions are pinned in one place (the Makefile).
- **MCP is at `/mcp/rpc`, not `/mcp`.** The sprites.dev proxy intercepts the bare `/mcp` path, and POSTs hang before reaching the app.
- **The server never calls a model.** The connected client (the person's own Claude) does the thinking, so usage costs whatever their plan costs. If you add server-side generation, put it behind its own key, a stored budget, and a cost recorded per call.
- **An empty `{{define}}` does not override a `{{block}}`.** Go templates treat a whitespace-only definition as empty and keep the original. Creature Lab's login page did this and ended up with a Sign out button. Give an override some content.

## Adding owners and members

When a resource belongs to some people and not others (a document with collaborators, a project, a game), give it members. [rpg](https://github.com/KevinMcHugh/rpg) does this for its games:

1. A `<resource>_members` table of (resource id, user id, role), with a unique index so a user is in a resource once, and one so it has one owner. Whoever creates the resource becomes its owner in the same transaction.
2. One query that, given what a request is about (the resource or anything in it) and the caller's user, returns the resource it belongs to and the caller's role. Every endpoint calls a helper on it (`app.Access`) before anything else, so REST, MCP and the pages all get the rule.
3. Something in a resource the caller is not a member of is a **404**, exactly like something that does not exist, never a 403, so a resource does not leak by existing. A 403 is for a member whose role does not allow the action.
4. Lists filter by membership (`JOIN <resource>_members … WHERE user_id = $1`).
5. Members join by a single-use link the owner makes over the API. Store only a hash of its code, and redeem it on a page where the person signs in (with Google, which makes their account if they are new), never through the API or MCP, so a link is not a credential.
6. A test that sends every operation in the spec as a stranger and expects a 404 from each, so an endpoint that forgets the check fails. Server commands get a system caller that reaches everything.

Unlike tenants, a user can be in many resources, so the resource comes from what a request is about rather than from the key.

## Adding tenants

Inventory is multi-tenant; this template is not. To add tenants:

1. Add a `tenants` table and a `tenant_id` column on `api_keys` and on every resource table. Scope the unique indexes by tenant.
2. Have `auth.Resolve` return the key's tenant too, and put it on the context (`auth.TenantID(ctx)`).
3. Give every query a `tenant_id = $n` filter, and every endpoint `auth.TenantID(ctx)` to pass in. Never take a tenant as a request field or a tool argument.
4. Mint tenants from the CLI (`server bootstrap --tenant NAME`), never over the API.
