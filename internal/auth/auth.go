// Package auth carries who a request is from, and the key and token
// formats. There are no tenants: every caller sees the same data, and the
// api key that authenticated a request is recorded on what it creates.
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

type ctxKey struct{}

// WithKey returns ctx carrying the id of the api key a request acts as
// (directly, or through an OAuth token or web session it approved).
func WithKey(ctx context.Context, apiKeyID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, apiKeyID)
}

// KeyID is the api key the request acts as, or "" outside an authenticated
// request (a CLI command, a test).
func KeyID(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}

// KeyIDPtr is KeyID as a nullable column value.
func KeyIDPtr(ctx context.Context) *string {
	if v := KeyID(ctx); v != "" {
		return &v
	}
	return nil
}
