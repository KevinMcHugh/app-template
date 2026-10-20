// Package server is the REST layer. api/openapi.yaml is the source of
// truth, oapi-codegen turns it into a strict server interface
// (internal/api/gen), and Server implements that interface by running one
// Endpoint per operation. The MCP tools and the web pages drive the same
// endpoints through BuildViewModel, so a rule lives in one place whichever
// surface calls it. See docs/architecture.md.
package server

import "context"

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

// Run drives an Endpoint end to end.
func Run[Req, M, VM, Res any](ctx context.Context, e Endpoint[Req, M, VM, Res], req Req) (Res, error) {
	m, err := e.Interact(ctx, req)
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

// BuildViewModel drives Interact then Build, stopping short of Render.
func BuildViewModel[Req, M, VM any](ctx context.Context, e Interactor[Req, M, VM], req Req) (VM, error) {
	m, err := e.Interact(ctx, req)
	if err != nil {
		var zero VM
		return zero, err
	}
	return e.Build(m), nil
}
