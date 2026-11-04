// Package auth carries who a request is from, and the key and token
// formats. A request acts as an api key, which belongs to a user (the
// person); there are no tenants, so every caller sees the same data, and
// the key is recorded on what it creates.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// Token prefixes. The prefix tells the middleware which table to look in,
// and tells a person what they are holding. Rename them for your app (the
// session cookie in middleware.go and the security schemes in
// api/openapi.yaml name them too). KeyPrefix must be a prefix of the
// others, and api keys never contain "_" after it, so the access-token
// check in Resolve can run first.
const (
	KeyPrefix          = "app_"    // api keys, minted by `server keys create`
	AccessTokenPrefix  = "app_at_" // OAuth access tokens and web sessions
	RefreshTokenPrefix = "app_rt_" // OAuth refresh tokens
	AuthCodePrefix     = "app_ac_" // OAuth authorization codes
	InvitePrefix       = "app_iv_" // single-use invites, minted by `server invites create`
)

// GenerateKey returns a new raw api key. It is shown to a person once; only
// Hash(key) is stored.
func GenerateKey() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return KeyPrefix + strings.ToLower(enc), nil
}

// RandToken returns prefix and 32 random bytes, URL-safe base64.
func RandToken(prefix string) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Hash is the hex SHA-256 stored in place of every key and token.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Caller is who a request acts as.
type Caller struct {
	// KeyID is the api key behind the request: used directly, or through an
	// OAuth token or web session it approved.
	KeyID string
	// UserID is the person the key belongs to. "" for a key minted before
	// there were users and never given one.
	UserID string
}

type ctxKey struct{}

// WithCaller returns ctx carrying c.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// CallerFrom is the request's caller; the zero Caller outside one (a CLI
// command, a test).
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(ctxKey{}).(Caller)
	return c
}

// KeyID is the api key the request acts as, or "" outside an authenticated
// request.
func KeyID(ctx context.Context) string { return CallerFrom(ctx).KeyID }

// UserID is the person the request acts for, or "".
func UserID(ctx context.Context) string { return CallerFrom(ctx).UserID }

// KeyIDPtr is KeyID as a nullable column value.
func KeyIDPtr(ctx context.Context) *string {
	if v := KeyID(ctx); v != "" {
		return &v
	}
	return nil
}
