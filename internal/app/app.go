// Package app holds what every surface (REST, MCP, web pages) shares: the
// Store, the errors the surfaces map to their own responses, and Links.
// Domain rules that more than one endpoint needs live here too.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/xid"

	"github.com/KevinMcHugh/app-template/internal/auth"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
)

// Name is the app's display name, on the pages, the OAuth authorize page
// and the MCP server's implementation info.
const Name = "App Template"

// Errors the surfaces (REST, MCP, web) map to their own responses: REST to
// 404 and 422, the pages to the same statuses, and MCP to a tool error.
var (
	ErrNotFound = errors.New("not found")
	ErrBadInput = errors.New("bad input")
)

// MaxBodyBytes bounds a request body.
const MaxBodyBytes = 1 << 20

// Store is the app's persistence: every sqlc query, plus the operations that
// need a transaction. Endpoints each declare a narrow interface of just the
// methods they call, and *Store satisfies all of them.
type Store struct {
	*dbgen.Queries
	Pool *pgxpool.Pool
}

// NewStore builds a Store on pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Queries: dbgen.New(pool), Pool: pool}
}

// RedeemInvite spends a single-use invite: it mints the newcomer's api key
// (named after the invite's note) and runs approve with that key on the same
// transaction, so an invite is only used up if what it was redeemed for
// (an OAuth code) is stored too. An unknown, used, expired or deleted invite
// is pgx.ErrNoRows. The raw key is never shown to anyone: the newcomer acts
// through the OAuth tokens it approves, and deleting the key cuts them off.
func (s *Store) RedeemInvite(ctx context.Context, raw string, approve func(q *dbgen.Queries, apiKeyID string) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	q := s.WithTx(tx)
	inv, err := q.ClaimInviteByHash(ctx, auth.Hash(raw))
	if err != nil {
		return err
	}
	name := inv.Note
	if name == "" {
		name = "invite " + inv.ID
	}
	key, err := auth.GenerateKey()
	if err != nil {
		return err
	}
	row, err := q.CreateAPIKey(ctx, dbgen.CreateAPIKeyParams{ID: xid.New().String(), Name: name, KeyHash: auth.Hash(key)})
	if err != nil {
		return err
	}
	if err := q.SetInviteKey(ctx, dbgen.SetInviteKeyParams{ID: inv.ID, ApiKeyID: &row.ID}); err != nil {
		return err
	}
	if err := approve(q, row.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Links builds the absolute URLs that view models carry.
type Links struct {
	// Base is the app's public origin ("https://app-xyz.sprites.app").
	// Empty gives relative links.
	Base string
}

// NewLinks trims base for joining.
func NewLinks(base string) Links { return Links{Base: strings.TrimRight(base, "/")} }

// Note is a note's page.
func (l Links) Note(id string) string { return l.Base + "/notes/" + id }

// NotFound turns pgx's no-rows into ErrNotFound, naming what was missing.
func NotFound(err error, what, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s %s", ErrNotFound, what, id)
	}
	return err
}
