// Package oauth is the minimum OAuth 2.1 authorization server an MCP client
// needs to reach the app, after the inventory app's internal/oauth with
// tenants and Google sign-in taken out (as Creature Lab has it).
//
// The flow: a client registers itself (POST /oauth/register), sends its user
// to /oauth/authorize, where they paste an api key (or, the first time, an
// invite, which mints them one), gets a code back on
// its redirect_uri, and trades the code and its PKCE verifier at
// /oauth/token for an access token (24 hours) and a refresh token (30 days).
// The tokens act as the api key that approved them. claude.ai custom
// connectors speak only this; Claude Code and scripts can send the api key
// itself as a bearer token instead.
package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/xid"

	"github.com/KevinMcHugh/app-template/internal/auth"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
)

// Lifetimes.
const (
	AccessTokenTTL  = 24 * time.Hour
	RefreshTokenTTL = 30 * 24 * time.Hour
	AuthCodeTTL     = 10 * time.Minute
	// SessionTTL is a web page session's, minted by /login.
	SessionTTL = 7 * 24 * time.Hour
)

// Handler serves the OAuth endpoints.
type Handler struct {
	// Issuer is the app's public origin, as discovery documents advertise it.
	Issuer string
	// AppName is shown on the authorize page.
	AppName string
	Q       *dbgen.Queries
	// Invites redeems an invite pasted on the authorize page. Nil turns
	// invites off: only existing api keys can authorize.
	Invites InviteRedeemer
}

// InviteRedeemer spends an invite, minting the newcomer's api key, and runs
// approve with it on the same transaction. *app.Store is one.
type InviteRedeemer interface {
	RedeemInvite(ctx context.Context, raw string, approve func(q *dbgen.Queries, apiKeyID string) error) error
}

// Mount attaches every endpoint to r. All of them are public: discovery is
// public by spec, and authorize and token check credentials themselves.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/.well-known/oauth-protected-resource", h.protectedResource)
	// RFC 9728 lets a client ask with the resource's path appended.
	r.Get("/.well-known/oauth-protected-resource/*", h.protectedResource)
	r.Get("/.well-known/oauth-authorization-server", h.authorizationServer)
	r.Post("/oauth/register", h.register)
	r.Get("/oauth/authorize", h.authorizeGET)
	r.Post("/oauth/authorize", h.authorizePOST)
	r.Post("/oauth/token", h.token)
}

// MintSession issues a web session token for an api key: an access token
// with no client and SessionTTL to live.
func (h *Handler) MintSession(ctx context.Context, apiKeyID string) (string, error) {
	raw, err := auth.RandToken(auth.AccessTokenPrefix)
	if err != nil {
		return "", err
	}
	err = h.Q.CreateOAuthToken(ctx, dbgen.CreateOAuthTokenParams{
		ID:        xid.New().String(),
		TokenHash: auth.Hash(raw),
		ApiKeyID:  apiKeyID,
		ExpiresAt: at(time.Now().Add(SessionTTL)),
	})
	return raw, err
}

// RevokeSession ends a web session.
func (h *Handler) RevokeSession(ctx context.Context, raw string) error {
	return h.Q.RevokeOAuthTokenByHash(ctx, auth.Hash(raw))
}

// ---- discovery --------------------------------------------------------------

func (h *Handler) protectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 h.Issuer + "/mcp/rpc",
		"authorization_servers":    []string{h.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
	})
}

func (h *Handler) authorizationServer(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                h.Issuer,
		"authorization_endpoint":                h.Issuer + "/oauth/authorize",
		"token_endpoint":                        h.Issuer + "/oauth/token",
		"registration_endpoint":                 h.Issuer + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
	})
}

// ---- registration -----------------------------------------------------------

// register is RFC 7591 dynamic client registration. Every client is public:
// no secret, PKCE instead.
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "malformed JSON body")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if p, err := url.Parse(u); err != nil || p.Scheme == "" || p.Host == "" {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be absolute URLs")
			return
		}
	}
	uris, _ := json.Marshal(req.RedirectURIs)
	var name *string
	if req.ClientName != "" {
		name = &req.ClientName
	}
	client, err := h.Q.CreateOAuthClient(r.Context(), dbgen.CreateOAuthClientParams{ID: xid.New().String(), RedirectUris: uris, ClientName: name})
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "persist client")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ID,
		"client_name":                req.ClientName,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

// ---- authorize --------------------------------------------------------------

type authorizeParams struct {
	ClientID, RedirectURI, State, CodeChallenge, CodeChallengeMethod, Scope string
}

func parseAuthorize(v url.Values) (authorizeParams, string) {
	p := authorizeParams{
		ClientID:            v.Get("client_id"),
		RedirectURI:         v.Get("redirect_uri"),
		State:               v.Get("state"),
		CodeChallenge:       v.Get("code_challenge"),
		CodeChallengeMethod: v.Get("code_challenge_method"),
		Scope:               v.Get("scope"),
	}
	switch {
	case v.Get("response_type") != "code":
		return p, "response_type must be 'code'"
	case p.ClientID == "" || p.RedirectURI == "":
		return p, "client_id and redirect_uri are required"
	case p.CodeChallenge == "":
		return p, "code_challenge is required (PKCE)"
	case p.CodeChallengeMethod != "S256":
		return p, "only the S256 code_challenge_method is supported"
	}
	return p, ""
}

// checkClient confirms the client exists and registered this redirect_uri.
func (h *Handler) checkClient(ctx context.Context, p authorizeParams) string {
	client, err := h.Q.GetOAuthClient(ctx, p.ClientID)
	if err != nil {
		return "unknown client_id"
	}
	var uris []string
	if json.Unmarshal(client.RedirectUris, &uris) != nil {
		return "client has no usable redirect_uris"
	}
	for _, u := range uris {
		if u == p.RedirectURI {
			return ""
		}
	}
	return "redirect_uri not registered for this client"
}

func (h *Handler) authorizeGET(w http.ResponseWriter, r *http.Request) {
	p, msg := parseAuthorize(r.URL.Query())
	if msg == "" {
		msg = h.checkClient(r.Context(), p)
	}
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	renderAuthorize(w, h.AppName, http.StatusOK, p, "")
}

// authorizePOST checks the pasted key, or redeems the pasted invite, mints a
// one-time code bound to the client, redirect_uri and PKCE challenge, and
// sends the browser back. A newcomer can only get in with an invite; there
// is no open sign-up.
func (h *Handler) authorizePOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	p, msg := parseAuthorize(r.Form)
	if msg == "" {
		msg = h.checkClient(r.Context(), p)
	}
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.FormValue("api_key"))
	if raw == "" {
		renderAuthorize(w, h.AppName, http.StatusBadRequest, p, "Paste your api key, or an invite if you are new.")
		return
	}
	var code string
	if strings.HasPrefix(raw, auth.InvitePrefix) {
		if h.Invites == nil {
			renderAuthorize(w, h.AppName, http.StatusBadRequest, p, "Invites are not accepted here.")
			return
		}
		err := h.Invites.RedeemInvite(r.Context(), raw, func(q *dbgen.Queries, apiKeyID string) error {
			var err error
			code, err = mintCode(r.Context(), q, apiKeyID, p)
			return err
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				renderAuthorize(w, h.AppName, http.StatusBadRequest, p, "That invite is not recognized, or it has expired or already been used.")
				return
			}
			http.Error(w, "invite redemption failed", http.StatusInternalServerError)
			return
		}
	} else {
		key, err := h.Q.GetAPIKeyByHash(r.Context(), auth.Hash(raw))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				renderAuthorize(w, h.AppName, http.StatusBadRequest, p, "That api key is not recognized.")
				return
			}
			http.Error(w, "auth lookup failed", http.StatusInternalServerError)
			return
		}
		if code, err = mintCode(r.Context(), h.Q, key.ID, p); err != nil {
			http.Error(w, "code persist failed", http.StatusInternalServerError)
			return
		}
	}
	dest, err := url.Parse(p.RedirectURI)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	q := dest.Query()
	q.Set("code", code)
	if p.State != "" {
		q.Set("state", p.State)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusSeeOther)
}

// mintCode stores a one-time authorization code for apiKeyID on q.
func mintCode(ctx context.Context, q *dbgen.Queries, apiKeyID string, p authorizeParams) (string, error) {
	code, err := auth.RandToken(auth.AuthCodePrefix)
	if err != nil {
		return "", err
	}
	var scope *string
	if p.Scope != "" {
		scope = &p.Scope
	}
	return code, q.CreateOAuthCode(ctx, dbgen.CreateOAuthCodeParams{
		CodeHash:            auth.Hash(code),
		ClientID:            p.ClientID,
		ApiKeyID:            apiKeyID,
		RedirectUri:         p.RedirectURI,
		CodeChallenge:       p.CodeChallenge,
		CodeChallengeMethod: p.CodeChallengeMethod,
		Scope:               scope,
		ExpiresAt:           at(time.Now().Add(AuthCodeTTL)),
	})
}

// ---- token ------------------------------------------------------------------

func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "bad form encoding")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		h.tokenFromCode(w, r)
	case "refresh_token":
		h.tokenFromRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code and refresh_token are supported")
	}
}

func (h *Handler) tokenFromCode(w http.ResponseWriter, r *http.Request) {
	code, verifier := r.Form.Get("code"), r.Form.Get("code_verifier")
	clientID, redirectURI := r.Form.Get("client_id"), r.Form.Get("redirect_uri")
	if code == "" || verifier == "" || clientID == "" || redirectURI == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code, code_verifier, client_id and redirect_uri are all required")
		return
	}
	row, err := h.Q.ConsumeOAuthCode(r.Context(), auth.Hash(code))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code is unknown, expired, or already used")
			return
		}
		oauthError(w, http.StatusInternalServerError, "server_error", "code lookup failed")
		return
	}
	switch {
	case row.ClientID != clientID:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code was issued to a different client")
		return
	case row.RedirectUri != redirectURI:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the one used at /authorize")
		return
	case !verifyPKCE(verifier, row.CodeChallenge):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verifier does not match code_challenge")
		return
	}
	h.issuePair(w, r, row.ClientID, row.ApiKeyID, row.Scope)
}

// tokenFromRefresh rotates a refresh token: the old pair is revoked and a
// new one issued, so each refresh token works once.
func (h *Handler) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	raw := r.Form.Get("refresh_token")
	if raw == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	hash := auth.Hash(raw)
	row, err := h.Q.GetOAuthTokenByRefreshHash(r.Context(), &hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is unknown, expired, or already used")
			return
		}
		oauthError(w, http.StatusInternalServerError, "server_error", "refresh token lookup failed")
		return
	}
	clientID := ""
	if row.ClientID != nil {
		clientID = *row.ClientID
	}
	if c := r.Form.Get("client_id"); c != "" && c != clientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token was issued to a different client")
		return
	}
	if err := h.Q.RevokeOAuthToken(r.Context(), row.ID); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "refresh token revoke failed")
		return
	}
	h.issuePair(w, r, clientID, row.ApiKeyID, row.Scope)
}

func (h *Handler) issuePair(w http.ResponseWriter, r *http.Request, clientID, apiKeyID string, scope *string) {
	access, err := auth.RandToken(auth.AccessTokenPrefix)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token generation failed")
		return
	}
	refresh, err := auth.RandToken(auth.RefreshTokenPrefix)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token generation failed")
		return
	}
	refreshHash := auth.Hash(refresh)
	var client *string
	if clientID != "" {
		client = &clientID
	}
	now := time.Now()
	if err := h.Q.CreateOAuthToken(r.Context(), dbgen.CreateOAuthTokenParams{
		ID:               xid.New().String(),
		TokenHash:        auth.Hash(access),
		ClientID:         client,
		ApiKeyID:         apiKeyID,
		Scope:            scope,
		ExpiresAt:        at(now.Add(AccessTokenTTL)),
		RefreshTokenHash: &refreshHash,
		RefreshExpiresAt: at(now.Add(RefreshTokenTTL)),
	}); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token persist failed")
		return
	}
	resp := map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(AccessTokenTTL.Seconds()),
		"refresh_token": refresh,
	}
	if scope != nil {
		resp["scope"] = *scope
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// ---- helpers ----------------------------------------------------------------

func verifyPKCE(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func at(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func renderAuthorize(w http.ResponseWriter, appName string, status int, p authorizeParams, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = authorizeTmpl.Execute(w, struct {
		AppName, KeyPrefix, InvitePrefix string
		Params                           authorizeParams
		Error                            string
	}{appName, auth.KeyPrefix, auth.InvitePrefix, p, msg})
}

var authorizeTmpl = template.Must(template.New("authorize").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.AppName}} · Authorize</title>
<style>
:root { color-scheme: light dark; --bg:#f7f8fa; --fg:#14171c; --muted:#5b6370; --line:#d5d9e0; --accent:#2f5bd3; --err:#b00020; }
@media (prefers-color-scheme: dark) { :root { --bg:#0f1115; --fg:#e8eaee; --muted:#9aa3b0; --line:#2a2f38; --accent:#7ea2ff; --err:#ff9aa8; } }
body { background:var(--bg); color:var(--fg); font:16px/1.5 system-ui, sans-serif; max-width:30rem; margin:4rem auto; padding:0 1rem; }
p { color:var(--muted); }
form { display:grid; gap:1rem; margin-top:1.5rem; }
input { font:inherit; font-family:ui-monospace, monospace; padding:.6rem .8rem; border:1px solid var(--line); border-radius:6px; background:transparent; color:var(--fg); }
button { font:inherit; padding:.6rem 1rem; border:0; border-radius:6px; background:var(--accent); color:#fff; cursor:pointer; }
.err { color:var(--err); }
</style>
</head>
<body>
<h1>Connect to {{.AppName}}</h1>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<p>Paste your {{.AppName}} api key (it starts with <code>{{.KeyPrefix}}</code>) to let this client act as you.</p>
<p>New here? Paste the invite you were given (it starts with <code>{{.InvitePrefix}}</code>). It works once, and connects this client as you from then on.</p>
<form method="POST">
<input type="password" name="api_key" autocomplete="off" autofocus placeholder="{{.KeyPrefix}}… or {{.InvitePrefix}}…" aria-label="API key or invite">
<input type="hidden" name="response_type" value="code">
<input type="hidden" name="client_id" value="{{.Params.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.Params.RedirectURI}}">
<input type="hidden" name="state" value="{{.Params.State}}">
<input type="hidden" name="code_challenge" value="{{.Params.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.Params.CodeChallengeMethod}}">
<input type="hidden" name="scope" value="{{.Params.Scope}}">
<button type="submit">Allow</button>
</form>
</body>
</html>`))
