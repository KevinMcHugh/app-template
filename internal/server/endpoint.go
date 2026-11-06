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

// Paged is the extension a list endpoint implements: Page names the list
// and reads the page the caller asked for (the limit and after query
// parameters every list operation takes). The endpoint's model is that one
// page, app.Page[Row], which its Interact gets from app.Paginate:
//
//	func (ListEndpoint) Page(req apigen.ListNotesRequestObject) app.PageRequest {
//		return app.PageRequest{List: "note", Limit: req.Params.Limit, After: req.Params.After}
//	}
//
// Run and BuildViewModel check what comes back: a page longer than the
// caller asked for fails closed, since it means the endpoint ignored the
// limit and a large list would come back whole.
type Paged[Req any] interface {
	Page(req Req) app.PageRequest
}

// PagedEndpoint is a list endpoint: a Paged Endpoint whose model is one
// page of rows and whose view model is one page of views. Server's
// handlers for the list operations run theirs through RunPaged, so a list
// endpoint that does not page fails to compile.
type PagedEndpoint[Req, Row, VM, Res any] interface {
	Endpoint[Req, app.Page[Row], app.Page[VM], Res]
	Paged[Req]
}

// RunPaged is Run for a list endpoint.
func RunPaged[Req, Row, VM, Res any](ctx context.Context, e PagedEndpoint[Req, Row, VM, Res], req Req) (Res, error) {
	return Run[Req, app.Page[Row], app.Page[VM], Res](ctx, e, req)
}

// checkPage refuses a page longer than e's request allows.
func checkPage[Req any](e any, req Req, m any) error {
	p, ok := e.(Paged[Req])
	if !ok {
		return nil
	}
	l, ok := m.(interface{ Len() int })
	if !ok {
		return fmt.Errorf("server: %T is Paged but its model %T is not a page", e, m)
	}
	size, err := p.Page(req).Size()
	if err != nil {
		return err
	}
	if l.Len() > size {
		return fmt.Errorf("server: %T returned %d items for a page of %d", e, l.Len(), size)
	}
	return nil
}

// All is every item of a paged list, following next from page to page,
// for a surface that needs the whole list at once (an MCP tool that
// gathers several lists, say). req makes the request for the page that
// starts at after. Prefer handing the caller a page and its next.
func All[Req, Row, VM any](ctx context.Context, e Interactor[Req, app.Page[Row], app.Page[VM]], req func(after string) Req) ([]VM, error) {
	var out []VM
	after := ""
	for {
		page, err := BuildViewModel(ctx, e, req(after))
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.Next == "" {
			return out, nil
		}
		after = page.Next
	}
}

// Run drives an Endpoint end to end, enforcing its Guard if it has one,
// and the length of its page if it is Paged.
func Run[Req, M, VM, Res any](ctx context.Context, e Endpoint[Req, M, VM, Res], req Req) (Res, error) {
	ctx, checked := guard(ctx, e, req)
	m, err := e.Interact(ctx, req)
	if err == nil {
		err = checked()
	}
	if err == nil {
		err = checkPage(e, req, m)
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
// enforces a Guard and a page's length exactly as Run does.
func BuildViewModel[Req, M, VM any](ctx context.Context, e Interactor[Req, M, VM], req Req) (VM, error) {
	ctx, checked := guard(ctx, e, req)
	m, err := e.Interact(ctx, req)
	if err == nil {
		err = checked()
	}
	if err == nil {
		err = checkPage(e, req, m)
	}
	if err != nil {
		var zero VM
		return zero, err
	}
	return e.Build(m), nil
}
