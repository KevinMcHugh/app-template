package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
)

// fakeGoogleToken is Google's token and userinfo endpoints, accepting only
// client:secret sent as HTTP Basic.
func fakeGoogleToken(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		if id != "client" || secret != "secret" || r.FormValue("client_secret") != "" || r.FormValue("code") != "c" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at"})
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "g-1", "email": "a@example.com", "name": "A"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// fakeConnector is a Sprites custom API connector in front of upstream: it
// refuses a request that carries its own credential, and adds the Basic
// credential it holds, as a connector made with auth_header_prefix "Basic"
// does.
func fakeConnector(t *testing.T, upstream string) *httptest.Server {
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the app sent a credential to the connector: %q", r.Header.Get("Authorization"))
		}
		r.SetBasicAuth("client", "secret")
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestGoogleExchange(t *testing.T) {
	google := fakeGoogleToken(t)
	connector := fakeConnector(t, google.URL)
	for name, g := range map[string]GoogleConfig{
		"secret held by the app": {ClientID: "client", ClientSecret: "secret", TokenURL: google.URL + "/token"},
		"secret in a connector":  {ClientID: "client", TokenURL: connector.URL + "/token"},
	} {
		t.Run(name, func(t *testing.T) {
			g.UserInfoURL = google.URL + "/userinfo"
			if !g.enabled() {
				t.Fatal("not enabled")
			}
			s := &Server{Google: g, Issuer: "https://app.example"}
			u, err := s.googleExchange(context.Background(), "c")
			if err != nil {
				t.Fatal(err)
			}
			if u.Subject != "g-1" || u.Email != "a@example.com" {
				t.Fatalf("got %+v", u)
			}
		})
	}
}

func TestGoogleEnabled(t *testing.T) {
	for _, c := range []struct {
		g    GoogleConfig
		want bool
	}{
		{GoogleConfig{}, false},
		{GoogleConfig{ClientID: "c"}, false},
		{GoogleConfig{ClientSecret: "s", TokenURL: "https://t"}, false},
		{GoogleConfig{ClientID: "c", ClientSecret: "s"}, true},
		{GoogleConfig{ClientID: "c", TokenURL: "https://api.sprites.dev/v1/gateway/custom_api/conn_x/token"}, true},
	} {
		if got := c.g.enabled(); got != c.want {
			t.Errorf("%+v: enabled() = %v, want %v", c.g, got, c.want)
		}
	}
}
