// Package me is the endpoint that says who a request acts as, and for
// whom: the smallest example of reading the caller from the context.
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
	// User is the person the key belongs to; nil for a key that has none.
	User *User `json:"user,omitempty"`
}

// User is a person.
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Store interface {
	GetAPIKey(ctx context.Context, id string) (dbgen.ApiKey, error)
	GetUser(ctx context.Context, id string) (dbgen.User, error)
}

// Endpoint is the api key the request acts as, and its user. The route is
// private, so the auth middleware has put a key on the context before this
// runs.
type Endpoint struct{ Store Store }

func (e Endpoint) Interact(ctx context.Context, _ apigen.GetMeRequestObject) (Me, error) {
	id := auth.KeyID(ctx)
	if id == "" {
		return Me{}, errors.New("me: no api key on the context")
	}
	k, err := e.Store.GetAPIKey(ctx, id)
	if err != nil {
		return Me{}, err
	}
	m := Me{KeyID: k.ID, Name: k.Name}
	if k.UserID != nil {
		u, err := e.Store.GetUser(ctx, *k.UserID)
		if err != nil {
			return Me{}, err
		}
		m.User = &User{ID: u.ID, Name: u.Name}
	}
	return m, nil
}

func (Endpoint) Build(m Me) Me { return m }

func (Endpoint) Render(vm Me) apigen.GetMeResponseObject {
	out := apigen.GetMe200JSONResponse{KeyId: vm.KeyID, Name: vm.Name}
	if vm.User != nil {
		out.User = &apigen.User{Id: vm.User.ID, Name: vm.User.Name}
	}
	return out
}
