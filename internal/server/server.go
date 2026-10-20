package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	apigen "github.com/KevinMcHugh/app-template/internal/api/gen"
	"github.com/KevinMcHugh/app-template/internal/app"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
	"github.com/KevinMcHugh/app-template/internal/server/me"
	"github.com/KevinMcHugh/app-template/internal/server/notes"
)

// BaseURL is where the API is mounted: the spec's servers entry.
const BaseURL = "/api"

// Store is everything the endpoints persist through. *app.Store satisfies
// it; each endpoint narrows it to its own interface. Add a method here when
// an endpoint needs a transaction that app.Store implements by hand.
type Store interface {
	dbgen.Querier
}

// Server implements apigen.StrictServerInterface by composing per-resource
// endpoints.
type Server struct {
	store Store
	links app.Links
}

// New builds the API on store, with links for the URLs in its responses.
func New(store Store, links app.Links) *Server {
	return &Server{store: store, links: links}
}

// Compile-time assertion: this fails to build until every operation in the
// spec has a method below.
var _ apigen.StrictServerInterface = (*Server)(nil)

// -----------------------------------------------------------------------------
// Mounting
// -----------------------------------------------------------------------------

// Mount attaches the API to r under BaseURL. requireAuth guards every
// operation except those the spec marks public with `security: []`, so the
// spec, not the router, decides who may call what.
func (s *Server) Mount(r chi.Router, requireAuth func(http.Handler) http.Handler) {
	public := PublicOperations()
	guard := func(next http.Handler) http.Handler {
		protected := requireAuth(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pattern := strings.TrimPrefix(chi.RouteContext(r.Context()).RoutePattern(), BaseURL)
			if public[r.Method+" "+pattern] {
				// Anyone may read these, so any site may too. No
				// credentials are allowed cross-site, so this opens nothing
				// an anonymous caller could not already get.
				w.Header().Set("Access-Control-Allow-Origin", "*")
				next.ServeHTTP(w, r)
				return
			}
			protected.ServeHTTP(w, r)
		})
	}
	strict := apigen.NewStrictHandlerWithOptions(s, nil, apigen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  badRequest,
		ResponseErrorHandlerFunc: internalError,
	})
	r.Group(func(r chi.Router) {
		r.Use(limitBody)
		apigen.HandlerWithOptions(strict, apigen.ChiServerOptions{
			BaseURL:          BaseURL,
			BaseRouter:       r,
			Middlewares:      []apigen.MiddlewareFunc{guard},
			ErrorHandlerFunc: badRequest,
		})
	})
}

// PublicOperations is the set of operations ("GET /notes/{noteId}") whose
// spec entry overrides the global security with an empty list. An operation
// that does not mention security inherits the global requirement, so a new
// endpoint is private until the spec says otherwise.
func PublicOperations() map[string]bool {
	spec, err := apigen.GetSwagger()
	if err != nil {
		// The spec is embedded at build time, so this is a broken build.
		panic("embedded OpenAPI spec: " + err.Error())
	}
	public := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if op.Security != nil && len(*op.Security) == 0 {
				public[method+" "+path] = true
			}
		}
	}
	return public
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, app.MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func badRequest(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, http.StatusBadRequest, err.Error())
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("api", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apigen.Error{Error: msg})
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

func isNotFound(err error) bool { return errors.Is(err, app.ErrNotFound) }

// isUnprocessable is a request that parsed but cannot be done: a field out
// of bounds.
func isUnprocessable(err error) bool { return errors.Is(err, app.ErrBadInput) }

func notFound(err error) apigen.NotFoundJSONResponse {
	return apigen.NotFoundJSONResponse{Error: err.Error()}
}

func unprocessable(err error) apigen.UnprocessableJSONResponse {
	return apigen.UnprocessableJSONResponse{Error: err.Error()}
}

func bodyRequired() apigen.BadRequestJSONResponse {
	return apigen.BadRequestJSONResponse{Error: "body required"}
}

// -----------------------------------------------------------------------------
// Me
// -----------------------------------------------------------------------------

func (s *Server) GetMe(ctx context.Context, req apigen.GetMeRequestObject) (apigen.GetMeResponseObject, error) {
	return Run(ctx, me.Endpoint{Store: s.store}, req)
}

// -----------------------------------------------------------------------------
// Notes
// -----------------------------------------------------------------------------

func (s *Server) ListNotes(ctx context.Context, req apigen.ListNotesRequestObject) (apigen.ListNotesResponseObject, error) {
	return Run(ctx, notes.ListEndpoint{Store: s.store, Links: s.links}, req)
}

func (s *Server) GetNote(ctx context.Context, req apigen.GetNoteRequestObject) (apigen.GetNoteResponseObject, error) {
	resp, err := Run(ctx, notes.GetEndpoint{Store: s.store, Links: s.links}, req)
	if isNotFound(err) {
		return apigen.GetNote404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	return resp, err
}

func (s *Server) CreateNote(ctx context.Context, req apigen.CreateNoteRequestObject) (apigen.CreateNoteResponseObject, error) {
	if req.Body == nil {
		return apigen.CreateNote400JSONResponse{BadRequestJSONResponse: bodyRequired()}, nil
	}
	resp, err := Run(ctx, notes.CreateEndpoint{Store: s.store, Links: s.links}, req)
	if isUnprocessable(err) {
		return apigen.CreateNote422JSONResponse{UnprocessableJSONResponse: unprocessable(err)}, nil
	}
	return resp, err
}

func (s *Server) UpdateNote(ctx context.Context, req apigen.UpdateNoteRequestObject) (apigen.UpdateNoteResponseObject, error) {
	if req.Body == nil {
		return apigen.UpdateNote400JSONResponse{BadRequestJSONResponse: bodyRequired()}, nil
	}
	resp, err := Run(ctx, notes.UpdateEndpoint{Store: s.store, Links: s.links}, req)
	switch {
	case isNotFound(err):
		return apigen.UpdateNote404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case isUnprocessable(err):
		return apigen.UpdateNote422JSONResponse{UnprocessableJSONResponse: unprocessable(err)}, nil
	}
	return resp, err
}

func (s *Server) DeleteNote(ctx context.Context, req apigen.DeleteNoteRequestObject) (apigen.DeleteNoteResponseObject, error) {
	resp, err := Run(ctx, notes.DeleteEndpoint{Store: s.store}, req)
	if isNotFound(err) {
		return apigen.DeleteNote404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	return resp, err
}
