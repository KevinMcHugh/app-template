// Package me is the endpoint that says who a request acts as: the smallest
// example of reading the caller from the context.
package me

import (
	"context"
	"errors"

	apigen "github.com/KevinMcHugh/app-template/internal/api/gen"
	"github.com/KevinMcHugh/app-template/internal/auth"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
)

// Me is the caller.
type Me struct {
	KeyID string `json:"keyId"`
	Name  string `json:"name"`
}

type Store interface {
	GetAPIKey(ctx context.Context, id string) (dbgen.ApiKey, error)
}

// Endpoint is the api key the request acts as. The route is private, so the
// auth middleware has put a key on the context before this runs.
type Endpoint struct{ Store Store }

func (e Endpoint) Interact(ctx context.Context, _ apigen.GetMeRequestObject) (dbgen.ApiKey, error) {
	id := auth.KeyID(ctx)
	if id == "" {
		return dbgen.ApiKey{}, errors.New("me: no api key on the context")
	}
	return e.Store.GetAPIKey(ctx, id)
}

func (Endpoint) Build(k dbgen.ApiKey) Me { return Me{KeyID: k.ID, Name: k.Name} }

func (Endpoint) Render(vm Me) apigen.GetMeResponseObject {
	return apigen.GetMe200JSONResponse{KeyId: vm.KeyID, Name: vm.Name}
}
