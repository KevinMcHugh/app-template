package app

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
)

// The Page methods are the store's paged lists: each takes what the list is
// filtered by and the page the caller asked for, and returns that page. The
// keyset query (…After), its sort key and app.Paginate stay here, so a list
// endpoint depends only on "this page of this list" through its narrow
// Store interface, and a fake store can return a page outright.

// Pager is every paged list the store holds. server.Store includes it,
// and a list endpoint's own Store interface names the one it reads.
type Pager interface {
	PageNotes(ctx context.Context, p PageRequest) (Page[dbgen.Note], error)
}

var _ Pager = (*Store)(nil)

// byTime is where a page of a list sorted by time starts, as the cursor
// holds it.
type byTime struct {
	At time.Time
	ID string
}

// PageNotes is a page of the notes, newest first.
func (s *Store) PageNotes(ctx context.Context, p PageRequest) (Page[dbgen.Note], error) {
	return Paginate(p,
		func(after *byTime, limit int32) ([]dbgen.Note, error) {
			arg := dbgen.NotesAfterParams{Lim: limit}
			if after != nil {
				arg.AfterCreatedAt, arg.AfterID = pgtype.Timestamptz{Time: after.At, Valid: true}, &after.ID
			}
			return s.NotesAfter(ctx, arg)
		},
		func(n dbgen.Note) byTime { return byTime{n.CreatedAt.Time, n.ID} })
}
