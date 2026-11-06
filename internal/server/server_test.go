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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	apigen "github.com/KevinMcHugh/app-template/internal/api/gen"
	"github.com/KevinMcHugh/app-template/internal/app"
	dbgen "github.com/KevinMcHugh/app-template/internal/db/gen"
	"github.com/KevinMcHugh/app-template/internal/server"
)

// fakeStore answers the public reads from memory. Any other query panics
// through the nil Store, which is the point: a private operation must be
// refused before it reaches the store.
type fakeStore struct {
	server.Store
	note dbgen.Note
}

func (f fakeStore) PageNotes(context.Context, app.PageRequest) (app.Page[dbgen.Note], error) {
	return app.Page[dbgen.Note]{Items: []dbgen.Note{f.note}}, nil
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

// TestEveryPatchIsGuarded checks the stale-write contract on every PATCH,
// including ones added after this test was: the body requires the
// updatedAt the caller loaded, and the operation declares the 409 it gets
// when that is stale. Server's handlers run them with RunGuarded, so the
// endpoint behind each one has to name its Guard to compile.
func TestEveryPatchIsGuarded(t *testing.T) {
	spec, err := apigen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for path, item := range spec.Paths.Map() {
		op := item.Patch
		if op == nil {
			continue
		}
		n++
		body := op.RequestBody.Value.Content.Get("application/json").Schema.Value
		if !slices.Contains(body.Required, "updatedAt") {
			t.Errorf("PATCH %s: the body does not require updatedAt", path)
		}
		if p := body.Properties["updatedAt"]; p == nil || p.Value.Format != "date-time" {
			t.Errorf("PATCH %s: updatedAt is not a date-time", path)
		}
		if op.Responses.Status(http.StatusConflict) == nil {
			t.Errorf("PATCH %s: no 409 response", path)
		}
	}
	if n == 0 {
		t.Fatal("no PATCH operations in the spec")
	}
}

// noStore is the store the fake endpoints below are handed; they use none.
type noStore struct{}

// guardedNoTx is a Guarded endpoint that never opens a transaction, so
// nothing checks its guard.
type guardedNoTx struct{}

func (guardedNoTx) Interact(context.Context, noStore, string) (string, error) { return "wrote", nil }
func (guardedNoTx) Build(m string) string                                     { return m }
func (guardedNoTx) Render(vm string) string                                   { return vm }
func (guardedNoTx) Guard(string) app.Guard {
	return app.Guard{Kind: "note", ID: "n1", Seen: time.Now()}
}

type unguarded struct{ guardedNoTx }

func (unguarded) Guard() {} // not Guarded: the signature does not match

// A Guarded endpoint whose write skipped InTx fails closed on every
// surface, rather than writing with no comparison; others are untouched.
func TestUncheckedGuardFailsClosed(t *testing.T) {
	ctx := context.Background()
	if _, err := server.RunGuarded(ctx, noStore{}, guardedNoTx{}, "req"); err == nil {
		t.Fatal("Run passed a guard nothing checked")
	}
	if _, err := server.BuildViewModel(ctx, noStore{}, guardedNoTx{}, "req"); err == nil {
		t.Fatal("BuildViewModel passed a guard nothing checked")
	}
	if got, err := server.Run(ctx, noStore{}, unguarded{}, "req"); err != nil || got != "wrote" {
		t.Fatalf("an unguarded endpoint: %q, %v", got, err)
	}
}

// TestEveryListIsPaged checks the pagination contract on every list
// operation, including ones added after this test was: it takes limit and
// after, its body carries the next cursor, and it declares the 422 it gets
// for a bad limit or cursor. Server's handlers run them with RunPaged, so
// the endpoint behind each one has to page to compile.
func TestEveryListIsPaged(t *testing.T) {
	spec, err := apigen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for path, item := range spec.Paths.Map() {
		op := item.Get
		if op == nil || !strings.HasPrefix(op.OperationID, "List") {
			continue
		}
		n++
		for _, name := range []string{"limit", "after"} {
			if op.Parameters.GetByInAndName("query", name) == nil {
				t.Errorf("GET %s: no %s parameter", path, name)
			}
		}
		body := op.Responses.Status(http.StatusOK).Value.Content.Get("application/json").Schema.Value
		if !slices.Contains(body.Required, "next") {
			t.Errorf("GET %s: the body has no next", path)
		}
		if op.Responses.Status(http.StatusUnprocessableEntity) == nil {
			t.Errorf("GET %s: no 422 response", path)
		}
	}
	if n == 0 {
		t.Fatal("no list operations in the spec")
	}
}

// overlong is a Paged endpoint that ignores the limit it was asked for.
type overlong struct{}

func (overlong) Interact(context.Context, noStore, int) (app.Page[int], error) {
	return app.Page[int]{Items: []int{1, 2, 3}}, nil
}
func (overlong) Build(p app.Page[int]) app.Page[int] { return p }
func (overlong) Render(p app.Page[int]) int          { return p.Len() }
func (overlong) Page(limit int) app.PageRequest      { return app.PageRequest{List: "n", Limit: limit} }

// A list endpoint that returns more than the caller asked for fails closed
// on every surface, rather than handing back a large list whole.
func TestOverlongPageFailsClosed(t *testing.T) {
	ctx := context.Background()
	if _, err := server.RunPaged(ctx, noStore{}, overlong{}, 2); err == nil {
		t.Fatal("Run passed a page of 3 for a limit of 2")
	}
	if _, err := server.BuildViewModel(ctx, noStore{}, overlong{}, 2); err == nil {
		t.Fatal("BuildViewModel passed a page of 3 for a limit of 2")
	}
	if n, err := server.RunPaged(ctx, noStore{}, overlong{}, 3); err != nil || n != 3 {
		t.Fatalf("a page within its limit: %d, %v", n, err)
	}
}
