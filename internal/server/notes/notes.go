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

	"github.com/jackc/pgx/v5/pgtype"
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
	PageNotes(ctx context.Context, arg dbgen.PageNotesParams) ([]dbgen.Note, error)
}

// ListEndpoint is a page of the notes, newest first. It is public.
type ListEndpoint struct {
	Store ListStore
	Links app.Links
}

// Page is the page the caller asked for.
func (ListEndpoint) Page(req apigen.ListNotesRequestObject) app.PageRequest {
	return app.PageRequest{List: "note", Limit: req.Params.Limit, After: req.Params.After}
}

// newest is where a page of a list sorted newest first starts.
type newest struct {
	CreatedAt time.Time
	ID        string
}

func (e ListEndpoint) Interact(ctx context.Context, req apigen.ListNotesRequestObject) (app.Page[dbgen.Note], error) {
	return app.Paginate(e.Page(req),
		func(after *newest, limit int32) ([]dbgen.Note, error) {
			arg := dbgen.PageNotesParams{Lim: limit}
			if after != nil {
				arg.AfterCreatedAt, arg.AfterID = pgtype.Timestamptz{Time: after.CreatedAt, Valid: true}, &after.ID
			}
			return e.Store.PageNotes(ctx, arg)
		},
		func(n dbgen.Note) newest { return newest{n.CreatedAt.Time, n.ID} })
}

func (e ListEndpoint) Build(p app.Page[dbgen.Note]) app.Page[Note] {
	return app.MapPage(p, func(r dbgen.Note) Note { return ToViewModel(r, e.Links) })
}

func (e ListEndpoint) Render(p app.Page[Note]) apigen.ListNotesResponseObject {
	return apigen.ListNotes200JSONResponse{Notes: app.MapPage(p, toAPI).Items, Next: p.Next}
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
	InTx(ctx context.Context, fn func(q *dbgen.Queries) error) error
}

// UpdateEndpoint changes a note's title, body, or both; a field left nil is
// left alone. It is Guarded: the caller sends back the updatedAt it loaded,
// and a change over someone else's newer one is refused (app.ErrConflict).
// The write happens in InTx, which checks the guard.
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
	var row dbgen.Note
	err := e.Store.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		row, err = q.UpdateNote(ctx, arg)
		return app.NotFound(err, "note", req.NoteId)
	})
	return row, err
}

// Guard names the row the update changes and the version the caller saw.
func (UpdateEndpoint) Guard(req apigen.UpdateNoteRequestObject) app.Guard {
	return app.Guard{Kind: "note", ID: req.NoteId, Seen: req.Body.UpdatedAt}
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
