package web_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/xid"

	"github.com/KevinMcHugh/app-template/internal/app"
	"github.com/KevinMcHugh/app-template/internal/auth"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
	"github.com/KevinMcHugh/app-template/internal/server/notes"
	"github.com/KevinMcHugh/app-template/internal/web"
)

// The end-to-end tests need a Postgres they may wipe: point
// TEST_DATABASE_URL at a scratch database. Without it they skip, so
// `go test ./...` passes on a machine with no Postgres.
const dbEnv = "TEST_DATABASE_URL"

type env struct {
	t      *testing.T
	url    string
	key    string
	keyID  string
	q      *dbgen.Queries
	client *http.Client // never follows redirects
}

func newEnv(t *testing.T) *env {
	dsn := os.Getenv(dbEnv)
	if dsn == "" {
		t.Skip(dbEnv + " not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrate(t, pool)

	ts := httptest.NewServer(nil)
	t.Cleanup(ts.Close)
	store := app.NewStore(pool)
	ts.Config.Handler = web.New(store, ts.URL).Handler()

	raw, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateAPIKey(ctx, dbgen.CreateAPIKeyParams{ID: xid.New().String(), Name: "test", KeyHash: auth.Hash(raw)})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, url: ts.URL, key: raw, keyID: key.ID, q: store.Queries, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// migrate rebuilds the schema from the dbmate files' up sections.
func migrate(t *testing.T, pool *pgxpool.Pool) {
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("../../db/migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(b), "-- migrate:down")
		if _, err := pool.Exec(ctx, up); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
}

// do sends a request with a bearer token (if any) and decodes a JSON reply
// into out (if non-nil), failing unless the status is want.
func (e *env) do(method, path, token string, body any, want int, out any) *http.Response {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.url+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, data, err)
		}
	}
	return resp
}

func TestAPIFlow(t *testing.T) {
	e := newEnv(t)

	resp := e.do("GET", "/api/me", "", nil, http.StatusUnauthorized, nil)
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource") {
		t.Fatalf("401 does not point at OAuth discovery: %q", resp.Header.Get("WWW-Authenticate"))
	}
	e.do("GET", "/api/me", auth.KeyPrefix+"not-a-key", nil, http.StatusUnauthorized, nil)
	e.do("POST", "/api/notes", "", map[string]any{"title": "x"}, http.StatusUnauthorized, nil)

	var me struct{ KeyID, Name string }
	e.do("GET", "/api/me", e.key, nil, http.StatusOK, &me)
	if me.KeyID != e.keyID || me.Name != "test" {
		t.Fatalf("me %+v", me)
	}

	var n notes.Note
	e.do("POST", "/api/notes", e.key, map[string]any{"title": "  first  ", "body": "hello"}, http.StatusCreated, &n)
	if n.Title != "first" || n.Body != "hello" || n.URL != e.url+"/notes/"+n.ID {
		t.Fatalf("created %+v", n)
	}
	e.do("POST", "/api/notes", e.key, map[string]any{"title": "  "}, http.StatusUnprocessableEntity, nil)
	e.do("POST", "/api/notes", e.key, map[string]any{"title": strings.Repeat("x", notes.MaxTitle+1)}, http.StatusUnprocessableEntity, nil)

	// A PATCH changes only what it sends.
	var upd notes.Note
	e.do("PATCH", "/api/notes/"+n.ID, e.key, map[string]any{"body": "changed"}, http.StatusOK, &upd)
	if upd.Title != "first" || upd.Body != "changed" {
		t.Fatalf("updated %+v", upd)
	}
	e.do("PATCH", "/api/notes/nope", e.key, map[string]any{"body": "x"}, http.StatusNotFound, nil)

	// Reads are public.
	var list struct{ Notes []notes.Note }
	resp = e.do("GET", "/api/notes", "", nil, http.StatusOK, &list)
	if len(list.Notes) != 1 || list.Notes[0].ID != n.ID {
		t.Fatalf("list %+v", list)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("public read has no CORS header")
	}
	e.do("GET", "/api/notes/"+n.ID, "", nil, http.StatusOK, nil)

	// Delete is soft, and a second one is a 404.
	e.do("DELETE", "/api/notes/"+n.ID, e.key, nil, http.StatusNoContent, nil)
	e.do("DELETE", "/api/notes/"+n.ID, e.key, nil, http.StatusNotFound, nil)
	e.do("GET", "/api/notes/"+n.ID, "", nil, http.StatusNotFound, nil)
}

// pkce is a verifier and its S256 challenge.
func pkce() (verifier, challenge string) {
	verifier = "a-verifier-that-is-long-enough-for-pkce-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestOAuthFlow(t *testing.T) {
	e := newEnv(t)
	const redirect = "https://claude.example/callback"

	var reg struct {
		ClientID string `json:"client_id"`
	}
	e.do("POST", "/oauth/register", "", map[string]any{"redirect_uris": []string{redirect}, "client_name": "test"}, http.StatusCreated, &reg)

	verifier, challenge := pkce()
	form := url.Values{
		"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {redirect},
		"state": {"xyz"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	resp, err := e.client.Get(e.url + "/oauth/authorize?" + form.Encode())
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize page: %v %v", resp.StatusCode, err)
	}
	form.Set("api_key", auth.KeyPrefix+"wrong")
	if resp, _ = e.client.PostForm(e.url+"/oauth/authorize", form); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a wrong key should not authorize: %d", resp.StatusCode)
	}
	form.Set("api_key", e.key)
	resp, err = e.client.PostForm(e.url+"/oauth/authorize", form)
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorize: %v %v", resp.StatusCode, err)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("redirect %s", loc)
	}

	token := func(v url.Values, want int) map[string]any {
		t.Helper()
		resp, err := e.client.PostForm(e.url+"/oauth/token", v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != want {
			t.Fatalf("token: %d %v, want %d", resp.StatusCode, out, want)
		}
		return out
	}
	codeGrant := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"code_verifier": {verifier}, "client_id": {reg.ClientID}, "redirect_uri": {redirect}}
	tok := token(codeGrant, http.StatusOK)
	token(codeGrant, http.StatusBadRequest) // a code works once

	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	e.do("GET", "/api/me", access, nil, http.StatusOK, nil)

	refreshGrant := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}
	next := token(refreshGrant, http.StatusOK)
	token(refreshGrant, http.StatusBadRequest) // a refresh token works once
	e.do("GET", "/api/me", access, nil, http.StatusUnauthorized, nil)
	e.do("GET", "/api/me", next["access_token"].(string), nil, http.StatusOK, nil)

	// Deleting the key cuts off every token it approved.
	if err := e.q.DeleteAPIKey(context.Background(), e.keyID); err != nil {
		t.Fatal(err)
	}
	e.do("GET", "/api/me", next["access_token"].(string), nil, http.StatusUnauthorized, nil)
}

func TestInviteFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const redirect = "https://claude.example/callback"
	var reg struct {
		ClientID string `json:"client_id"`
	}
	e.do("POST", "/oauth/register", "", map[string]any{"redirect_uris": []string{redirect}}, http.StatusCreated, &reg)

	invite := func(note string, expires time.Duration) string {
		t.Helper()
		raw, _ := auth.RandToken(auth.InvitePrefix)
		arg := dbgen.CreateInviteParams{ID: xid.New().String(), CodeHash: auth.Hash(raw), Note: note}
		if expires != 0 {
			arg.ExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(expires), Valid: true}
		}
		if _, err := e.q.CreateInvite(ctx, arg); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	verifier, challenge := pkce()
	// authorize pastes credential on the authorize page and returns the
	// status and, on success, the code.
	authorize := func(credential string) (int, string) {
		t.Helper()
		form := url.Values{
			"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {redirect},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"},
			"api_key": {credential},
		}
		resp, err := e.client.PostForm(e.url+"/oauth/authorize", form)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		return resp.StatusCode, loc.Query().Get("code")
	}

	raw := invite("ana", 0)
	status, code := authorize(raw)
	if status != http.StatusSeeOther || code == "" {
		t.Fatalf("redeem invite: %d", status)
	}
	if status, _ = authorize(raw); status != http.StatusBadRequest {
		t.Fatalf("an invite works once: %d", status)
	}
	if status, _ = authorize(auth.InvitePrefix + "not-an-invite"); status != http.StatusBadRequest {
		t.Fatalf("unknown invite: %d", status)
	}
	if status, _ = authorize(invite("late", -time.Minute)); status != http.StatusBadRequest {
		t.Fatalf("expired invite: %d", status)
	}

	resp, err := e.client.PostForm(e.url+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"code_verifier": {verifier}, "client_id": {reg.ClientID}, "redirect_uri": {redirect}})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("token: %v %v", resp.StatusCode, err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()

	// The invite minted a key named after its note, and the token acts as it.
	var me struct{ KeyID, Name string }
	e.do("GET", "/api/me", tok.AccessToken, nil, http.StatusOK, &me)
	if me.Name != "ana" {
		t.Fatalf("redeeming the invite minted key %+v, want one named ana", me)
	}
	// The newcomer's key is a key like any other: deleting it cuts them off.
	if err := e.q.DeleteAPIKey(ctx, me.KeyID); err != nil {
		t.Fatal(err)
	}
	e.do("GET", "/api/me", tok.AccessToken, nil, http.StatusUnauthorized, nil)
}

func TestWebPages(t *testing.T) {
	e := newEnv(t)
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: e.client.CheckRedirect}

	page := func(path string) string {
		t.Helper()
		resp, err := browser.Get(e.url + path)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %v %v", path, resp.StatusCode, err)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("GET %s: no CSP", path)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}

	// Signed out, writes bounce.
	resp, _ := browser.PostForm(e.url+"/notes", url.Values{"title": {"x"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed-out create: %d", resp.StatusCode)
	}
	resp, _ = browser.PostForm(e.url+"/login", url.Values{"api_key": {"wrong"}, "next": {"/"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", resp.StatusCode)
	}
	resp, _ = browser.PostForm(e.url+"/login", url.Values{"api_key": {e.key}, "next": {"//evil.example"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("login: %d %s (next must stay on this site)", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, _ = browser.PostForm(e.url+"/notes", url.Values{"title": {"Groceries"}, "body": {"eggs\nmilk"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	notePath, _, _ := strings.Cut(resp.Header.Get("Location"), "?")
	noteID := strings.TrimPrefix(notePath, "/notes/")
	if body := page("/"); !strings.Contains(body, "Groceries") || !strings.Contains(body, ">"+noteID+"</code>") {
		t.Fatal("index does not list the note with its id")
	}
	resp, _ = browser.PostForm(e.url+"/notes", url.Values{"title": {""}})
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "err=") {
		t.Fatalf("an empty title should come back as a message: %d %s", resp.StatusCode, loc)
	}

	resp, _ = browser.PostForm(e.url+notePath, url.Values{"title": {"Groceries"}, "body": {"eggs"}})
	if resp.StatusCode != http.StatusSeeOther || strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Fatalf("update: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	signedIn := page(notePath + "?msg=Saved.")
	for _, want := range []string{"eggs", "Saved.", "Edit", "Delete", "Sign out"} {
		if !strings.Contains(signedIn, want) {
			t.Errorf("signed-in note page lacks %q", want)
		}
	}

	// A cross-site form post is refused even with the cookie.
	req, _ := http.NewRequest("POST", e.url+notePath+"/delete", nil)
	req.Header.Set("Origin", "https://evil.example")
	if resp, _ = browser.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin delete: %d", resp.StatusCode)
	}

	browser.PostForm(e.url+"/logout", nil) //nolint:errcheck

	// Signed out, the pages are public and read-only. A stale cookie reads as
	// signed out rather than locking them.
	jar.SetCookies(mustURL(e.url), []*http.Cookie{{Name: auth.SessionCookie, Value: auth.AccessTokenPrefix + "stale"}})
	anon := page(notePath)
	if !strings.Contains(anon, "eggs") || !strings.Contains(anon, "Sign in") {
		t.Fatal("signed-out note page should show the note")
	}
	for _, hidden := range []string{"<form", "Delete", "Sign out"} {
		if strings.Contains(anon, hidden) {
			t.Errorf("signed-out note page shows %q", hidden)
		}
	}
	resp, _ = browser.PostForm(e.url+notePath+"/delete", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed-out delete: %d", resp.StatusCode)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// bearer adds an Authorization header to every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

func TestMCPTools(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:   e.url + "/mcp/rpc",
		HTTPClient: &http.Client{Transport: bearer{e.key, http.DefaultTransport}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	call := func(name string, args map[string]any, out any) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s: tool error %+v", name, res.Content)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	var who struct{ Me struct{ KeyID, Name string } }
	call("whoami", nil, &who)
	if who.Me.KeyID != e.keyID {
		t.Fatalf("whoami %+v", who)
	}

	var created struct{ Note notes.Note }
	call("create_note", map[string]any{"title": "From Claude", "body": "hi"}, &created)
	var updated struct{ Note notes.Note }
	call("update_note", map[string]any{"id": created.Note.ID, "title": "Renamed"}, &updated)
	if updated.Note.Title != "Renamed" || updated.Note.Body != "hi" {
		t.Fatalf("update_note: %+v", updated.Note)
	}
	var list struct{ Notes []notes.Note }
	call("list_notes", nil, &list)
	if len(list.Notes) != 1 {
		t.Fatalf("list_notes: %+v", list)
	}
	var ok struct{ OK bool }
	call("delete_note", map[string]any{"id": created.Note.ID}, &ok)

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_note", Arguments: map[string]any{"id": created.Note.ID}})
	if err != nil || !res.IsError {
		t.Fatalf("a deleted note should come back as a tool error: %v %+v", err, res)
	}

	// Without a credential, MCP is a 401 pointing at OAuth discovery.
	resp, err := http.Post(e.url+"/mcp/rpc", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous MCP: %d", resp.StatusCode)
	}
}
