# Adding a resource, end to end

A recipe for a new CRUD resource, using `widget` as the example. The notes resource (`internal/server/notes`) is a complete worked example of every step; copying it and renaming is usually the quickest start.

## 1. Migration

```sh
make new-migration NAME=create_widgets
```

```sql
-- migrate:up

CREATE TABLE widgets (
    id          CHAR(20)    PRIMARY KEY,
    name        TEXT        NOT NULL,
    created_by  CHAR(20)    REFERENCES api_keys(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX widgets_active ON widgets (created_at DESC) WHERE deleted_at IS NULL;

-- migrate:down

DROP TABLE widgets;
```

Never edit an applied migration; add another.

## 2. Queries

`db/queries/widgets.sql`:

```sql
-- name: CreateWidget :one
INSERT INTO widgets (id, name, created_by) VALUES ($1, $2, $3) RETURNING *;

-- name: GetWidget :one
SELECT * FROM widgets WHERE id = $1 AND deleted_at IS NULL;

-- name: ListWidgets :many
SELECT * FROM widgets WHERE deleted_at IS NULL ORDER BY created_at DESC;

-- name: UpdateWidget :one
UPDATE widgets SET name = COALESCE(sqlc.narg(name), name), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL RETURNING *;

-- name: DeleteWidget :execrows
UPDATE widgets SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL;
```

Every read and write filters `deleted_at IS NULL`.

## 3. Spec

Add paths and schemas to `api/openapi.yaml`, following the notes entries. Then decide who may call each operation:

- Leave `security` out and the operation is **private**, inheriting the global bearer-or-cookie requirement.
- Add `security: []` only to make it **public**. Public responses also get `Access-Control-Allow-Origin: *`.

For a PATCH body, mark each optional field `x-go-type-skip-optional-pointer: false`, so "not sent" differs from "sent empty".

Name path parameters so they do not collide with schema names: oapi-codegen generates a Go type for both. Creature Lab's `Form` parameter clashed with its `Form` schema, for example.

## 4. Generate

```sh
make generate
```

The build now fails at `var _ apigen.StrictServerInterface = (*Server)(nil)` until step 6 is done. That is expected.

## 5. Endpoints

Create `internal/server/widgets/widgets.go`, using `notes/notes.go` as the model:

- a view model `Widget` with JSON tags, `ToViewModel(dbgen.Widget, app.Links) Widget`, and an unexported `toAPI(Widget) apigen.Widget`;
- for each operation, a narrow `Store` interface and an endpoint struct with `Interact`, `Build` and `Render`;
- validation in `Interact`, returning `fmt.Errorf("%w: …", app.ErrBadInput)` or `app.NotFound(err, "widget", id)`;
- `auth.KeyIDPtr(ctx)` for `created_by`.

If a view model links to a page, add a method to `app.Links`.

If an operation needs a transaction, add a method to `app.Store` (see `RedeemInvite`) and to the `server.Store` interface.

## 6. Wire into the server

In `internal/server/server.go`, add one method per operation and map errors to the responses the spec declares:

```go
func (s *Server) GetWidget(ctx context.Context, req apigen.GetWidgetRequestObject) (apigen.GetWidgetResponseObject, error) {
	resp, err := Run(ctx, widgets.GetEndpoint{Store: s.store, Links: s.links}, req)
	if isNotFound(err) {
		return apigen.GetWidget404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	return resp, err
}
```

Check `req.Body == nil` in operations that take a body, and return the 400.

## 7. MCP tools

If Claude should see it, add tools in `internal/appmcp/appmcp.go`: an input struct with `jsonschema` descriptions, an output struct wrapping the view model, and a handler that calls `server.BuildViewModel` with a request object it fills in. Keep tool descriptions specific about when to use the tool. Say "confirm with the user first" on anything destructive, and update `Instructions` if the typical session changes.

## 8. Pages

If people should see it in the browser, add a template in `internal/web/templates/` and its name to `parsePages`, page handlers in `pages.go` that call `server.BuildViewModel`, and routes in `server.go`:

- reads in the `auth.Optional` group, with forms wrapped in `{{if .SignedIn}}`;
- writes in the `auth.Require` group, which also checks `sameOrigin`. Hiding a form is only for looks; this group is what actually stops an anonymous write.

Post, then redirect with `redirect(w, r, path, msg, isErr)`. The pages run no script.

## 9. Tests

- If the public set changed, update `TestPublicOperations` in `internal/server/server_test.go`, and add the new operations to `TestAnonymousCallers`.
- Add a flow to `internal/web/e2e_test.go`: create, read, update, delete over the API, plus the MCP tools. Run it with `TEST_DATABASE_URL` set.

## 10. Docs

If the resource has rules a newcomer would otherwise have to reverse-engineer, write them down in `docs/`.

## Common pitfalls

- Forgetting `make generate` after editing the spec or SQL. The compiler will not find the new types, and CI's `make check` fails.
- A query that scans deleted rows (a missing `AND deleted_at IS NULL`).
- Putting a rule in an MCP tool or a page handler instead of the endpoint, so the other surfaces skip it.
- Making a route public in the router instead of with `security: []` in the spec.
