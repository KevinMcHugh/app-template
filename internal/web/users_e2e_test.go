package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/xid"

	"github.com/KevinMcHugh/app-template/internal/auth"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
	"github.com/KevinMcHugh/app-template/internal/web"
)

// googleAccounts are the people the fake Google knows, by the code its
// consent screen would hand back: the test sends the browser's callback
// itself, with one of these codes.
var googleAccounts = map[string]map[string]any{
	"code-bea": {"sub": "g-bea", "email": "bea@example.com", "name": "Bea"},
	"code-cal": {"sub": "g-cal", "email": "cal@example.com", "name": "Cal"},
	"code-dee": {"sub": "g-dee", "email": "dee@example.com", "name": "Dee"},
}

// fakeGoogle stands in for Google's token and userinfo endpoints.
func fakeGoogle(t *testing.T) web.GoogleConfig {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_secret") != "secret" || googleAccounts[r.FormValue("code")] == nil {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at-" + r.FormValue("code")})
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		info := googleAccounts[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")]
		if info == nil {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return web.GoogleConfig{ClientID: "client", ClientSecret: "secret", AuthURL: ts.URL + "/auth", TokenURL: ts.URL + "/token", UserInfoURL: ts.URL + "/userinfo"}
}

// session is the session token a response set, or "".
func session(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// google runs a Google sign-in started at start (with session, if any) as
// the fake Google account behind code, and returns the callback's response.
func (e *env) google(start, token, code string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.url+start, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("%s: %d", start, resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("redirect_uri") != e.url+"/auth/google/callback" {
		e.t.Fatalf("google redirect %s", loc)
	}
	cb, err := e.client.Get(e.url + "/auth/google/callback?" + url.Values{"state": {loc.Query().Get("state")}, "code": {code}}.Encode())
	if err != nil {
		e.t.Fatal(err)
	}
	cb.Body.Close()
	return cb
}

type meBody struct {
	KeyID, Name string
	User        *struct{ ID, Name string }
}

// TestUsers covers who a request acts for: a key's user, a newcomer who
// signs up with Google through an invite, signing in again, linking Google
// to an account a key made, and a stranger with no invite.
func TestUsers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	var me meBody
	e.do("GET", "/api/me", e.key, nil, http.StatusOK, &me)
	if me.User == nil || me.User.Name != "test" {
		t.Fatalf("me %+v", me)
	}
	keySession := func() string {
		resp, err := e.client.PostForm(e.url+"/login", url.Values{"api_key": {e.key}, "next": {"/"}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return session(resp)
	}()

	// A stranger with no invite gets no account.
	if cb := e.google("/auth/google", "", "code-dee"); cb.StatusCode != http.StatusForbidden || session(cb) != "" {
		t.Fatalf("a stranger: %d", cb.StatusCode)
	}

	// A newcomer signs up with an invite and lands where they were going.
	raw, _ := auth.RandToken(auth.InvitePrefix)
	if _, err := e.q.CreateInvite(ctx, dbgen.CreateInviteParams{ID: xid.New().String(), CodeHash: auth.Hash(raw), Note: "cal"}); err != nil {
		t.Fatal(err)
	}
	cb := e.google("/auth/google?"+url.Values{"invite": {raw}, "next": {"/notes/x"}}.Encode(), "", "code-cal")
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/notes/x" || session(cb) == "" {
		t.Fatalf("sign-up: %d %s", cb.StatusCode, cb.Header.Get("Location"))
	}
	e.do("GET", "/api/me", session(cb), nil, http.StatusOK, &me)
	if me.User == nil || me.User.Name != "Cal" {
		t.Fatalf("google user %+v", me)
	}
	cal := me.User.ID
	// What they make is recorded as theirs, like anyone's.
	e.do("POST", "/api/notes", session(cb), map[string]any{"title": "from cal"}, http.StatusCreated, nil)
	// The invite is spent.
	if cb := e.google("/auth/google?invite="+raw, "", "code-dee"); cb.StatusCode != http.StatusForbidden {
		t.Fatalf("a spent invite: %d", cb.StatusCode)
	}
	// Signing in again needs no invite, and is the same person.
	cb = e.google("/auth/google", "", "code-cal")
	e.do("GET", "/api/me", session(cb), nil, http.StatusOK, &me)
	if cb.StatusCode != http.StatusSeeOther || me.User.ID != cal {
		t.Fatalf("signing in again: %d %+v", cb.StatusCode, me)
	}

	// Someone with a key adds Google as a way in, but not someone else's.
	if cb := e.google("/auth/google?link=1", keySession, "code-bea"); cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("link: %d", cb.StatusCode)
	}
	e.do("GET", "/api/me", session(e.google("/auth/google", "", "code-bea")), nil, http.StatusOK, &me)
	if me.KeyID != e.keyID {
		t.Fatalf("a linked sign-in should act as the key's user: %+v", me)
	}
	if cb := e.google("/auth/google?link=1", keySession, "code-cal"); cb.StatusCode != http.StatusForbidden {
		t.Fatalf("linking someone else's google: %d", cb.StatusCode)
	}

	// Invite codes stay out of the log.
	if got := web.Redact("/auth/google?invite=" + raw + "&next=/"); strings.Contains(got, raw) {
		t.Fatalf("redacted %q", got)
	}
}

// TestConnectWithGoogle connects a claude.ai-style client by signing in
// with Google on the authorize page.
func TestConnectWithGoogle(t *testing.T) {
	e := newEnv(t)
	const redirect = "https://claude.example/callback"
	var reg struct {
		ClientID string `json:"client_id"`
	}
	e.do("POST", "/oauth/register", "", map[string]any{"redirect_uris": []string{redirect}}, http.StatusCreated, &reg)
	// Bea is the test user, signing in with Google.
	keySession := func() string {
		resp, _ := e.client.PostForm(e.url+"/login", url.Values{"api_key": {e.key}, "next": {"/"}})
		resp.Body.Close()
		return session(resp)
	}()
	e.google("/auth/google?link=1", keySession, "code-bea")

	verifier, challenge := pkce()
	authz := url.Values{"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"}}
	resp, err := e.client.Get(e.url + "/oauth/authorize?" + authz.Encode())
	if err != nil {
		t.Fatal(err)
	}
	var page strings.Builder
	buf := make([]byte, 1<<16)
	n, _ := resp.Body.Read(buf)
	page.Write(buf[:n])
	resp.Body.Close()
	i := strings.Index(page.String(), `href="/auth/google?`)
	if i < 0 {
		t.Fatalf("authorize page has no google link: %s", page.String())
	}
	start := page.String()[i+len(`href="`):]
	start = strings.ReplaceAll(start[:strings.Index(start, `"`)], "&amp;", "&")
	cb := e.google(start, "", "code-bea")
	loc, _ := url.Parse(cb.Header.Get("Location"))
	if cb.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc.String(), redirect) || loc.Query().Get("state") != "xyz" {
		t.Fatalf("google authorize: %d %s", cb.StatusCode, loc)
	}
	tr, err := e.client.PostForm(e.url+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"code_verifier": {verifier}, "client_id": {reg.ClientID}, "redirect_uri": {redirect}})
	if err != nil || tr.StatusCode != http.StatusOK {
		t.Fatalf("token: %v %v", tr.StatusCode, err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(tr.Body).Decode(&tok)
	tr.Body.Close()
	var me meBody
	e.do("GET", "/api/me", tok.AccessToken, nil, http.StatusOK, &me)
	if me.KeyID != e.keyID || me.User == nil || me.User.Name != "test" {
		t.Fatalf("the connector acts as %+v", me)
	}
}
