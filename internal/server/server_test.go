package server_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/KevinMcHugh/app-template/internal/app"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
	"github.com/KevinMcHugh/app-template/internal/server"
)

// fakeStore answers the public reads from memory. Any other query panics
// through the nil Querier, which is the point: a private operation must be
// refused before it reaches the store.
type fakeStore struct {
	dbgen.Querier
	note dbgen.Note
}

func (f fakeStore) ListNotes(context.Context) ([]dbgen.Note, error) {
	return []dbgen.Note{f.note}, nil
}

func (f fakeStore) GetNote(_ context.Context, id string) (dbgen.Note, error) {
	if id != f.note.ID {
		return dbgen.Note{}, pgx.ErrNoRows
	}
	return f.note, nil
}

// TestPublicOperations pins which operations anyone may call. Update it when
// the spec marks another operation `security: []`, and only then.
func TestPublicOperations(t *testing.T) {
	got := slices.Sorted(maps.Keys(server.PublicOperations()))
	want := []string{"GET /notes", "GET /notes/{noteId}"}
	if !slices.Equal(got, want) {
		t.Fatalf("public operations %v, want %v", got, want)
	}
}

func TestAnonymousCallers(t *testing.T) {
	store := fakeStore{note: dbgen.Note{ID: "n1", Title: "hello", Body: "world"}}

	r := chi.NewRouter()
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	}
	server.New(store, app.NewLinks("https://app.example")).Mount(r, deny)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/notes", http.StatusOK},
		{"GET", "/api/notes/n1", http.StatusOK},
		{"GET", "/api/notes/nope", http.StatusNotFound},
		{"POST", "/api/notes", http.StatusUnauthorized},
		{"PATCH", "/api/notes/n1", http.StatusUnauthorized},
		{"DELETE", "/api/notes/n1", http.StatusUnauthorized},
		{"GET", "/api/me", http.StatusUnauthorized},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
		if rec.Code != tc.want {
			t.Errorf("%s %s: %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body)
		}
		// Public reads open to any origin; nothing else does.
		cors := rec.Header().Get("Access-Control-Allow-Origin")
		if public := tc.want != http.StatusUnauthorized; public != (cors == "*") {
			t.Errorf("%s %s: Access-Control-Allow-Origin %q", tc.method, tc.path, cors)
		}
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/n1", nil))
	var note struct{ ID, Title, URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if note.ID != "n1" || note.Title != "hello" || note.URL != "https://app.example/notes/n1" {
		t.Fatalf("note %+v", note)
	}
}
