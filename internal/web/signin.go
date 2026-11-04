package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/xid"

	"github.com/KevinMcHugh/app-template/internal/auth"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
	"github.com/KevinMcHugh/app-template/internal/oauth"
)

// Signing in with Google, after the inventory app's internal/idp.
//
// A Google sign-in finds the user its (provider, subject) belongs to. A
// stranger gets in only with an invite the CLI minted, or by linking Google
// to an account they already have. The sign-in acts as the user's own api
// key, minted the first time and never shown, so a web session from Google
// is exactly a session from pasting a key on /login, and created_by records
// it as it records anyone.

// GoogleConfig turns Google sign-in on when both ClientID and ClientSecret
// are set. The endpoints default to Google's; tests point them at a fake.
type GoogleConfig struct {
	ClientID, ClientSecret         string
	AuthURL, TokenURL, UserInfoURL string
}

func (g GoogleConfig) enabled() bool { return g.ClientID != "" && g.ClientSecret != "" }

func (g GoogleConfig) withDefaults() GoogleConfig {
	if g.AuthURL == "" {
		g.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if g.TokenURL == "" {
		g.TokenURL = "https://oauth2.googleapis.com/token"
	}
	if g.UserInfoURL == "" {
		g.UserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	}
	return g
}

const (
	googleProvider = "google"
	intentTTL      = 10 * time.Minute
)

// authorizeKeys are the OAuth authorize request's fields, carried through
// Google when a claude.ai connector started the sign-in.
var authorizeKeys = []string{"response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "scope"}

// googleStart sends the browser to Google. Its query says why:
//   - next=PATH: sign in to the app, then go there;
//   - invite=CODE: sign up with an invite the CLI minted;
//   - link=1: add Google as a way for the signed-in person to sign in;
//   - the OAuth authorize request's fields: finish connecting a client.
func (s *Server) googleStart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx := r.Context()
	_ = s.Store.PurgeExpiredSSOLoginIntents(ctx)
	state, err := randomState()
	if err != nil {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	arg := dbgen.CreateSSOLoginIntentParams{
		State:     state,
		Provider:  googleProvider,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(intentTTL), Valid: true},
	}
	if next := q.Get("next"); next != "" {
		n := safeNext(next)
		arg.ReturnTo = &n
	}
	if inv := strings.TrimSpace(q.Get("invite")); inv != "" {
		arg.InviteCode = &inv
	}
	if q.Get("link") != "" {
		c := auth.CallerFrom(ctx)
		if c.UserID == "" {
			http.Redirect(w, r, "/login?next="+url.QueryEscape("/auth/google?link=1"), http.StatusSeeOther)
			return
		}
		arg.LinkUserID = &c.UserID
	}
	if q.Get("client_id") != "" {
		ptr := func(k string) *string { v := q.Get(k); return &v }
		arg.ClientID, arg.RedirectUri, arg.CodeChallenge = ptr("client_id"), ptr("redirect_uri"), ptr("code_challenge")
		arg.CodeChallengeMethod, arg.DownstreamState, arg.Scope = ptr("code_challenge_method"), ptr("state"), ptr("scope")
	}
	if err := s.Store.CreateSSOLoginIntent(ctx, arg); err != nil {
		slog.Error("google start", "err", err)
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	g := s.Google.withDefaults()
	params := url.Values{
		"client_id":     {g.ClientID},
		"redirect_uri":  {s.Issuer + "/auth/google/callback"},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
		"prompt":        {"select_account"},
	}
	http.Redirect(w, r, g.AuthURL+"?"+params.Encode(), http.StatusFound)
}

// googleUser is the part of Google's userinfo that matters.
type googleUser struct {
	Subject, Email, Name string
}

// signInError is a refusal shown to the person, as opposed to a failure.
type signInError struct{ msg string }

func (e signInError) Error() string { return e.msg }

func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.render(w, r, http.StatusForbidden, "login", map[string]any{"Next": "/", "Error": "Google did not sign you in (" + e + ")."})
		return
	}
	intent, err := s.Store.ConsumeSSOLoginIntent(ctx, q.Get("state"))
	if err != nil {
		s.render(w, r, http.StatusBadRequest, "login", map[string]any{"Next": "/", "Error": "That sign-in took too long or was already used; try again."})
		return
	}
	info, err := s.googleExchange(ctx, q.Get("code"))
	if err != nil {
		slog.Error("google exchange", "err", err)
		http.Error(w, "Google sign-in failed; try again.", http.StatusBadGateway)
		return
	}
	keyID, err := s.resolveGoogle(ctx, info, intent)
	var refused signInError
	if errors.As(err, &refused) {
		s.render(w, r, http.StatusForbidden, "login", map[string]any{"Next": "/", "Error": refused.msg})
		return
	}
	if err != nil {
		slog.Error("google sign-in", "err", err)
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	if intent.ClientID != nil {
		vals := url.Values{"response_type": {"code"}}
		for k, v := range map[string]*string{"client_id": intent.ClientID, "redirect_uri": intent.RedirectUri, "state": intent.DownstreamState,
			"code_challenge": intent.CodeChallenge, "code_challenge_method": intent.CodeChallengeMethod, "scope": intent.Scope} {
			if v != nil && *v != "" {
				vals.Set(k, *v)
			}
		}
		dest, err := s.OAuth.Complete(ctx, keyID, vals)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	next := "/"
	if intent.ReturnTo != nil {
		next = safeNext(*intent.ReturnTo)
	}
	if err := s.signIn(w, r, keyID); err != nil {
		http.Error(w, "session failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// resolveGoogle finds or makes the user a Google sign-in is, and their
// key, and redeems the intent's invite, all on one transaction.
func (s *Server) resolveGoogle(ctx context.Context, info googleUser, intent dbgen.SsoLoginIntent) (keyID string, err error) {
	invite := ""
	if intent.InviteCode != nil {
		invite = *intent.InviteCode
	}
	err = s.Store.InTx(ctx, func(q *dbgen.Queries) error {
		var userID, cliInvite string
		var display *string
		if info.Name != "" {
			display = &info.Name
		}
		ident, err := q.GetUserIdentity(ctx, dbgen.GetUserIdentityParams{Provider: googleProvider, Subject: info.Subject})
		switch {
		case err == nil:
			if intent.LinkUserID != nil && *intent.LinkUserID != ident.UserID {
				return signInError{"That Google account already signs in someone else here."}
			}
			userID = ident.UserID
			if err := q.TouchUserIdentity(ctx, dbgen.TouchUserIdentityParams{ID: ident.ID, Email: info.Email, DisplayName: display}); err != nil {
				return err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		case intent.LinkUserID != nil:
			userID = *intent.LinkUserID
		case strings.HasPrefix(invite, auth.InvitePrefix):
			inv, err := q.ClaimInviteByHash(ctx, auth.Hash(invite))
			if err != nil {
				return signInError{"That invite is not recognized, or it has expired or already been used."}
			}
			if userID, err = newUser(ctx, q, info); err != nil {
				return err
			}
			cliInvite = inv.ID
		default:
			return signInError{"There is no account here for " + info.Email + ". You need an invite to join."}
		}
		if ident.ID == "" {
			if _, err := q.CreateUserIdentity(ctx, dbgen.CreateUserIdentityParams{
				ID: xid.New().String(), UserID: userID, Provider: googleProvider, Subject: info.Subject, Email: info.Email, DisplayName: display,
			}); err != nil {
				return err
			}
		}
		if keyID, err = userKey(ctx, q, userID); err != nil {
			return err
		}
		if cliInvite != "" {
			if err := q.SetInviteKey(ctx, dbgen.SetInviteKeyParams{ID: cliInvite, ApiKeyID: &keyID, UserID: &userID}); err != nil {
				return err
			}
		}
		return nil
	})
	return keyID, err
}

// newUser makes a user for a Google identity, named as Google names them.
func newUser(ctx context.Context, q *dbgen.Queries, info googleUser) (string, error) {
	name := info.Name
	if name == "" {
		name = info.Email
	}
	u, err := q.CreateUser(ctx, dbgen.CreateUserParams{ID: xid.New().String(), Name: name})
	return u.ID, err
}

// userKey is the key a user's web sessions act as: their first key, or,
// for someone who has only ever signed in with Google, a new one whose raw
// value is thrown away. Nobody can use it but through a sign-in.
func userKey(ctx context.Context, q *dbgen.Queries, userID string) (string, error) {
	k, err := q.GetKeyForUser(ctx, &userID)
	if err == nil {
		return k.ID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	raw, err := auth.GenerateKey()
	if err != nil {
		return "", err
	}
	row, err := q.CreateAPIKey(ctx, dbgen.CreateAPIKeyParams{ID: xid.New().String(), Name: u.Name, KeyHash: auth.Hash(raw), UserID: &userID})
	return row.ID, err
}

// googleExchange trades the callback's code for the person's profile.
func (s *Server) googleExchange(ctx context.Context, code string) (googleUser, error) {
	if code == "" {
		return googleUser{}, errors.New("callback has no code")
	}
	g := s.Google.withDefaults()
	client := &http.Client{Timeout: 10 * time.Second}
	form := url.Values{
		"code": {code}, "client_id": {g.ClientID}, "client_secret": {g.ClientSecret},
		"redirect_uri": {s.Issuer + "/auth/google/callback"}, "grant_type": {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return googleUser{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := doJSON(client, req, &tok); err != nil {
		return googleUser{}, fmt.Errorf("token: %w", err)
	}
	if tok.AccessToken == "" {
		return googleUser{}, errors.New("token response has no access_token")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, g.UserInfoURL, nil)
	if err != nil {
		return googleUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := doJSON(client, req, &info); err != nil {
		return googleUser{}, fmt.Errorf("userinfo: %w", err)
	}
	if info.Sub == "" || info.Email == "" {
		return googleUser{}, errors.New("userinfo has no sub or email")
	}
	return googleUser{Subject: info.Sub, Email: info.Email, Name: info.Name}, nil
}

func doJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, body)
	}
	return json.Unmarshal(body, out)
}

func randomState() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// signIn sets the session cookie for a web session acting as keyID.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request, keyID string) error {
	token, err := s.OAuth.MintSession(r.Context(), keyID)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(oauth.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.Issuer, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// redactCodes keeps invite codes out of the request log: they sit in the
// query of a sign-in that carries one.
func redactCodes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.RequestURI, auth.InvitePrefix) {
			r2 := r.Clone(r.Context())
			r2.RequestURI = redact(r.RequestURI)
			r = r2
		}
		next.ServeHTTP(w, r)
	})
}

func redact(uri string) string {
	for _, p := range []string{auth.InvitePrefix} {
		for {
			i := strings.Index(uri, p)
			if i < 0 {
				break
			}
			j := i + len(p)
			for j < len(uri) && uri[j] != '&' && uri[j] != '/' && uri[j] != '?' {
				j++
			}
			uri = uri[:i] + "[invite]" + uri[j:]
		}
	}
	return uri
}

// Redact is redact, for tests.
var Redact = redact
