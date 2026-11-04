package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
)

// ErrConflict is a write over a row that changed after the caller loaded
// it: someone else saved in between. REST maps it to 409.
var ErrConflict = errors.New("conflict")

// Guard is a stale-write check: the row a write changes (a kind, as
// Access names kinds, and an id) and its updatedAt as the caller last
// loaded it. An endpoint that changes a stored row names its Guard
// (server.Guarded), and the write is refused with ErrConflict unless the
// row is still as the caller saw it. That way two people editing the same
// line cannot silently overwrite each other: the second to save is told,
// and reloads.
type Guard struct {
	Kind string
	ID   string
	Seen time.Time
}

// GuardState is a Guard on its way through one request: WithGuard puts it
// on the context, and Store.InTx checks it at the start of the endpoint's
// transaction, holding the row's lock until the write commits.
type GuardState struct {
	Guard   Guard
	checked bool
}

// Checked reports whether a transaction checked the guard. server.Run
// fails closed when it did not, since that means the write it guards ran
// outside InTx with nothing comparing the versions.
func (s *GuardState) Checked() bool { return s.checked }

type guardKey struct{}

// WithGuard puts g on ctx for the next InTx to check.
func WithGuard(ctx context.Context, g Guard) (context.Context, *GuardState) {
	s := &GuardState{Guard: g}
	return context.WithValue(ctx, guardKey{}, s), s
}

// LockStore is what checking a guard reads.
type LockStore interface {
	LockForWrite(ctx context.Context, arg dbgen.LockForWriteParams) (pgtype.Timestamptz, error)
}

// checkGuard locks the guarded row for the rest of q's transaction and
// compares its updatedAt with the one the caller saw. It runs once per
// request, in the first InTx: a second transaction in the same request
// would otherwise see the first one's write as someone else's.
func checkGuard(ctx context.Context, q LockStore) error {
	s, _ := ctx.Value(guardKey{}).(*GuardState)
	if s == nil || s.checked {
		return nil
	}
	g := s.Guard
	what := strings.ReplaceAll(g.Kind, "_", " ")
	if g.Seen.IsZero() {
		return fmt.Errorf("%w: updatedAt is required: send the %s's updatedAt as you last loaded it", ErrBadInput, what)
	}
	cur, err := q.LockForWrite(ctx, dbgen.LockForWriteParams{Kind: g.Kind, TargetID: g.ID})
	if err != nil {
		return NotFound(err, what, g.ID)
	}
	if !cur.Time.Equal(g.Seen) {
		return fmt.Errorf("%w: someone else changed this %s at %s, after you loaded it; reload it, and make your change again",
			ErrConflict, what, cur.Time.UTC().Format(time.RFC3339))
	}
	s.checked = true
	return nil
}
