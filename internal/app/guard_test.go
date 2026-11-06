package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/KevinMcHugh/app-template/internal/app"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
)

// locks answers LockForWrite with one row's updatedAt.
type locks struct{ at time.Time }

func (l locks) LockForWrite(context.Context, dbgen.LockForWriteParams) (pgtype.Timestamptz, error) {
	return pgtype.Timestamptz{Time: l.at, Valid: true}, nil
}

func TestClaim(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 123456000, time.UTC)
	q := locks{at}
	ctx := context.Background()
	g := app.Guard{Kind: "note", ID: "n1", Seen: at}

	if err := app.Claim(ctx, q, app.Guard{Kind: "note", ID: "n1", Seen: at.Add(-time.Second)}); !errors.Is(err, app.ErrConflict) {
		t.Errorf("a stale copy: %v, want a conflict", err)
	}
	if err := app.Claim(ctx, q, app.Guard{Kind: "note", ID: "n1"}); !errors.Is(err, app.ErrBadInput) {
		t.Errorf("no updatedAt: %v, want bad input", err)
	}

	// The receipt is signed only by a claim of the guard it was made for.
	rctx, r := app.ExpectClaim(ctx, g)
	if err := app.Claim(rctx, q, app.Guard{Kind: "note", ID: "n2", Seen: at}); err != nil || r.Claimed() {
		t.Fatalf("claiming another row: %v, claimed %v", err, r.Claimed())
	}
	if err := app.Claim(rctx, q, g); err != nil || !r.Claimed() {
		t.Fatalf("claiming the guard: %v, claimed %v", err, r.Claimed())
	}
}
