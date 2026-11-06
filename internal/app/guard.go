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
// (server.Guarded) and claims it (Claim) first thing in the transaction
// that writes the row, and the write is refused with ErrConflict unless the
// row is still as the caller saw it. That way two people editing the same
// line cannot silently overwrite each other: the second to save is told,
// and reloads.
type Guard struct {
	Kind string
	ID   string
	Seen time.Time
}

// LockStore is what claiming a guard reads: the transaction's queries.
type LockStore interface {
	LockForWrite(ctx context.Context, arg dbgen.LockForWriteParams) (pgtype.Timestamptz, error)
}

// Claim locks g's row for the rest of q's transaction and compares its
// updatedAt with the one the caller saw: ErrConflict if someone saved
// since, ErrBadInput if the caller sent none. The lock is held until the
// transaction ends, so the write that follows in it is made over exactly
// the version the caller saw, and of two saves racing from one copy only
// one gets past here. Call it first in the transaction that writes the row:
//
//	err := e.Store.InTx(ctx, func(q *dbgen.Queries) error {
//		if err := app.Claim(ctx, q, e.Guard(req)); err != nil {
//			return err
//		}
//		// the write, on q
//	})
func Claim(ctx context.Context, q LockStore, g Guard) error {
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
	if r, _ := ctx.Value(receiptKey{}).(*Receipt); r != nil && r.Guard == g {
		r.claimed = true
	}
	return nil
}

// Receipt records whether the guard an endpoint declared was claimed.
// server.Run asks for one (ExpectClaim) before a Guarded endpoint runs and
// fails closed if it comes back unclaimed: that would mean the endpoint
// wrote without comparing versions, or compared a different row's.
type Receipt struct {
	Guard   Guard
	claimed bool
}

// Claimed reports whether Claim was called with the expected guard.
func (r *Receipt) Claimed() bool { return r.claimed }

type receiptKey struct{}

// ExpectClaim puts a receipt for g on ctx, for Claim to sign.
func ExpectClaim(ctx context.Context, g Guard) (context.Context, *Receipt) {
	r := &Receipt{Guard: g}
	return context.WithValue(ctx, receiptKey{}, r), r
}
