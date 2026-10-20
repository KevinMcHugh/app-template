// Package notes holds the endpoints for the example resource: list, get,
// create, update and delete. Copy it as the starting point for a resource
// of your own (docs/adding-a-resource.md).
package notes

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/xid"

	apigen "github.com/KevinMcHugh/app-template/internal/api/gen"
	"github.com/KevinMcHugh/app-template/internal/app"
	"github.com/KevinMcHugh/app-template/internal/auth"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
)

// Limits, in characters.
const (
	MaxTitle = 200
	MaxBody  = 20000
)

// -----------------------------------------------------------------------------
// View model
// -----------------------------------------------------------------------------

// Note is a note as every surface shows it. The MCP tools return it as is,
// the pages render it, and the API converts it with toAPI.
type Note struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	URL       string    `json:"url"`
}

// ToViewModel is the view of a stored note.
func ToViewModel(row dbgen.Note, links app.Links) Note {
	return Note{
		ID:        row.ID,
		Title:     row.Title,
		Body:      row.Body,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
		URL:       links.Note(row.ID),
	}
}

func toAPI(n Note) apigen.Note {
	return apigen.Note{
		Id:        n.ID,
		Title:     n.Title,
		Body:      n.Body,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		Url:       n.URL,
	}
}

// -----------------------------------------------------------------------------
// Validation
// -----------------------------------------------------------------------------

// cleanTitle trims a title and checks its bounds.
func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%w: title is required", app.ErrBadInput)
	}
	if n := utf8.RuneCountInString(s); n > MaxTitle {
		return "", fmt.Errorf("%w: title is %d characters; the limit is %d", app.ErrBadInput, n, MaxTitle)
	}
	return s, nil
}

// cleanBody trims a body and checks its bounds.
func cleanBody(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n > MaxBody {
		return "", fmt.Errorf("%w: body is %d characters; the limit is %d", app.ErrBadInput, n, MaxBody)
	}
	return s, nil
}

// -----------------------------------------------------------------------------
// List notes
// -----------------------------------------------------------------------------

type ListStore interface {
	ListNotes(ctx context.Context) ([]dbgen.Note, error)
}

// ListEndpoint is every note, newest first. It is public.
type ListEndpoint struct {
	Store ListStore
	Links app.Links
}

func (e ListEndpoint) Interact(ctx context.Context, _ apigen.ListNotesRequestObject) ([]dbgen.Note, error) {
	return e.Store.ListNotes(ctx)
}

func (e ListEndpoint) Build(rows []dbgen.Note) []Note {
	out := make([]Note, len(rows))
	for i, r := range rows {
		out[i] = ToViewModel(r, e.Links)
	}
	return out
}

func (e ListEndpoint) Render(vms []Note) apigen.ListNotesResponseObject {
	out := make([]apigen.Note, len(vms))
	for i, n := range vms {
		out[i] = toAPI(n)
	}
	return apigen.ListNotes200JSONResponse{Notes: out}
}

// -----------------------------------------------------------------------------
// Get note
// -----------------------------------------------------------------------------

type GetStore interface {
	GetNote(ctx context.Context, id string) (dbgen.Note, error)
}

// GetEndpoint is one note. It is public.
type GetEndpoint struct {
	Store GetStore
	Links app.Links
}

func (e GetEndpoint) Interact(ctx context.Context, req apigen.GetNoteRequestObject) (dbgen.Note, error) {
	row, err := e.Store.GetNote(ctx, req.NoteId)
	return row, app.NotFound(err, "note", req.NoteId)
}

func (e GetEndpoint) Build(row dbgen.Note) Note { return ToViewModel(row, e.Links) }

func (e GetEndpoint) Render(vm Note) apigen.GetNoteResponseObject {
	return apigen.GetNote200JSONResponse(toAPI(vm))
}

// -----------------------------------------------------------------------------
// Create note
// -----------------------------------------------------------------------------

type CreateStore interface {
	CreateNote(ctx context.Context, arg dbgen.CreateNoteParams) (dbgen.Note, error)
}

// CreateEndpoint saves a new note, recording the api key that made it.
type CreateEndpoint struct {
	Store CreateStore
	Links app.Links
}

func (e CreateEndpoint) Interact(ctx context.Context, req apigen.CreateNoteRequestObject) (dbgen.Note, error) {
	title, err := cleanTitle(req.Body.Title)
	if err != nil {
		return dbgen.Note{}, err
	}
	body, err := cleanBody(req.Body.Body)
	if err != nil {
		return dbgen.Note{}, err
	}
	return e.Store.CreateNote(ctx, dbgen.CreateNoteParams{
		ID:        xid.New().String(),
		Title:     title,
		Body:      body,
		CreatedBy: auth.KeyIDPtr(ctx),
	})
}

func (e CreateEndpoint) Build(row dbgen.Note) Note { return ToViewModel(row, e.Links) }

func (e CreateEndpoint) Render(vm Note) apigen.CreateNoteResponseObject {
	return apigen.CreateNote201JSONResponse(toAPI(vm))
}

// -----------------------------------------------------------------------------
// Update note
// -----------------------------------------------------------------------------

type UpdateStore interface {
	UpdateNote(ctx context.Context, arg dbgen.UpdateNoteParams) (dbgen.Note, error)
}

// UpdateEndpoint changes a note's title, body, or both; a field left nil is
// left alone.
type UpdateEndpoint struct {
	Store UpdateStore
	Links app.Links
}

func (e UpdateEndpoint) Interact(ctx context.Context, req apigen.UpdateNoteRequestObject) (dbgen.Note, error) {
	arg := dbgen.UpdateNoteParams{ID: req.NoteId}
	if t := req.Body.Title; t != nil {
		title, err := cleanTitle(*t)
		if err != nil {
			return dbgen.Note{}, err
		}
		arg.Title = &title
	}
	if b := req.Body.Body; b != nil {
		body, err := cleanBody(*b)
		if err != nil {
			return dbgen.Note{}, err
		}
		arg.Body = &body
	}
	row, err := e.Store.UpdateNote(ctx, arg)
	return row, app.NotFound(err, "note", req.NoteId)
}

func (e UpdateEndpoint) Build(row dbgen.Note) Note { return ToViewModel(row, e.Links) }

func (e UpdateEndpoint) Render(vm Note) apigen.UpdateNoteResponseObject {
	return apigen.UpdateNote200JSONResponse(toAPI(vm))
}

// -----------------------------------------------------------------------------
// Delete note
// -----------------------------------------------------------------------------

type DeleteStore interface {
	DeleteNote(ctx context.Context, id string) (int64, error)
}

// DeleteEndpoint soft-deletes a note. Its view model is just "it happened".
type DeleteEndpoint struct{ Store DeleteStore }

func (e DeleteEndpoint) Interact(ctx context.Context, req apigen.DeleteNoteRequestObject) (bool, error) {
	n, err := e.Store.DeleteNote(ctx, req.NoteId)
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, fmt.Errorf("%w: note %s", app.ErrNotFound, req.NoteId)
	}
	return true, nil
}

func (DeleteEndpoint) Build(ok bool) bool { return ok }

func (DeleteEndpoint) Render(bool) apigen.DeleteNoteResponseObject {
	return apigen.DeleteNote204Response{}
}
