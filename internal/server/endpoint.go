// Package server is the REST layer. api/openapi.yaml is the source of
// truth, oapi-codegen turns it into a strict server interface
// (internal/api/gen), and Server implements that interface by running one
// Endpoint per operation. The MCP tools and the web pages drive the same
// endpoints through BuildViewModel, so a rule lives in one place whichever
// surface calls it. See docs/architecture.md.
package server

import (
	"context"
	"fmt"

	"github.com/KevinMcHugh/app-template/internal/app"
)

// Endpoint is the MVVM contract for a single HTTP operation.
//
//   - Req is the parsed input (an oapi-codegen *RequestObject).
//   - M  is the domain model an Interact produces from the store.
//   - VM is the view model, a pure transformation of M for presentation.
//   - Res is the typed response (an oapi-codegen *ResponseObject).
//
// Each endpoint holds its own narrow Store interface, so it declares
// exactly the persistence it needs.
type Endpoint[Req, M, VM, Res any] interface {
	Interact(ctx context.Context, req Req) (M, error)
	Build(m M) VM
	Render(vm VM) Res
}

// Guarded is the extension an Endpoint implements when it changes a row a
// caller has already loaded (every PATCH): Guard names that row and the
// updatedAt the caller sent back. Run and BuildViewModel put the guard on
// the context, and Store.InTx checks it at the start of the endpoint's
// transaction, locking the row, so a write over someone else's newer save
// is refused with app.ErrConflict (409) instead of silently replacing it.
// The endpoint itself only has to name the guard and do its write in InTx:
//
//	func (UpdateEndpoint) Guard(req apigen.UpdateNoteRequestObject) app.Guard {
//		return app.Guard{Kind: "note", ID: req.NoteId, Seen: req.Body.UpdatedAt}
//	}
type Guarded[Req any] interface {
	Guard(req Req) app.Guard
}

// GuardedEndpoint is an Endpoint with a Guard. Server's handlers for the
// operations that take an updatedAt run theirs through RunGuarded, so
// leaving the guard off one fails to compile.
type GuardedEndpoint[Req, M, VM, Res any] interface {
	Endpoint[Req, M, VM, Res]
	Guarded[Req]
}

// guard puts e's guard on ctx, if it has one, and returns a check that it
// was enforced. A guarded endpoint whose write never went through InTx
// fails closed rather than quietly skipping the comparison.
func guard[Req any](ctx context.Context, e any, req Req) (context.Context, func() error) {
	g, ok := e.(Guarded[Req])
	if !ok {
		return ctx, func() error { return nil }
	}
	ctx, s := app.WithGuard(ctx, g.Guard(req))
	return ctx, func() error {
		if !s.Checked() {
			return fmt.Errorf("server: %T is Guarded but never opened a transaction, so its guard went unchecked", e)
		}
		return nil
	}
}

// Run drives an Endpoint end to end, enforcing its Guard if it has one.
func Run[Req, M, VM, Res any](ctx context.Context, e Endpoint[Req, M, VM, Res], req Req) (Res, error) {
	ctx, checked := guard(ctx, e, req)
	m, err := e.Interact(ctx, req)
	if err == nil {
		err = checked()
	}
	if err != nil {
		var zero Res
		return zero, err
	}
	return e.Render(e.Build(m)), nil
}

// Interactor is the Interact+Build half of Endpoint. Every Endpoint
// satisfies it, so the MCP tools and web pages reuse an endpoint's logic and
// view model while presenting it their own way.
type Interactor[Req, M, VM any] interface {
	Interact(ctx context.Context, req Req) (M, error)
	Build(m M) VM
}

// RunGuarded is Run for an endpoint that must carry a Guard.
func RunGuarded[Req, M, VM, Res any](ctx context.Context, e GuardedEndpoint[Req, M, VM, Res], req Req) (Res, error) {
	return Run[Req, M, VM, Res](ctx, e, req)
}

// BuildViewModel drives Interact then Build, stopping short of Render. It
// enforces a Guard exactly as Run does.
func BuildViewModel[Req, M, VM any](ctx context.Context, e Interactor[Req, M, VM], req Req) (VM, error) {
	ctx, checked := guard(ctx, e, req)
	m, err := e.Interact(ctx, req)
	if err == nil {
		err = checked()
	}
	if err != nil {
		var zero VM
		return zero, err
	}
	return e.Build(m), nil
}
