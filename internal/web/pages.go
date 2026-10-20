package web

import (
	"bytes"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	apigen "github.com/KevinMcHugh/app-template/internal/api/gen"
	"github.com/KevinMcHugh/app-template/internal/app"
	"github.com/KevinMcHugh/app-template/internal/auth"
	"github.com/KevinMcHugh/app-template/internal/server"
	"github.com/KevinMcHugh/app-template/internal/server/notes"
)

// The pages drive the same endpoints as the JSON API and MCP, through
// server.BuildViewModel, and render the view models as HTML.

func parsePages() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "index", "note"} {
		pages[name] = template.Must(template.New("layout.html").
			ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html"))
	}
	return pages
}

// render writes a page through a buffer, so a template error becomes a 500
// rather than half a page. Every page learns whether the visitor is signed
// in (the public pages hide their forms from anyone who is not) and gets the
// one-line message a redirect left in ?msg= or ?err=.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data map[string]any) {
	data["AppName"] = app.Name
	data["SignedIn"] = auth.KeyID(r.Context()) != ""
	data["Path"] = r.URL.RequestURI()
	if _, ok := data["Flash"]; !ok {
		data["Flash"] = r.URL.Query().Get("msg")
	}
	if _, ok := data["Error"]; !ok {
		data["Error"] = r.URL.Query().Get("err")
	}
	var buf bytes.Buffer
	if err := s.pages[name].Execute(&buf, data); err != nil {
		slog.Error("render", "page", name, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// statusOf maps the endpoints' errors to HTTP statuses.
func statusOf(err error) int {
	switch {
	case errors.Is(err, app.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, app.ErrBadInput):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// fail answers a page request whose endpoint failed.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := statusOf(err)
	if status == http.StatusInternalServerError {
		slog.Error("page", "path", r.URL.Path, "err", err)
		http.Error(w, "internal error", status)
		return
	}
	http.Error(w, err.Error(), status)
}

// redirect sends the browser to path with a one-line message, err or not,
// that the next page shows (post/redirect/get, with no script and no
// session state).
func redirect(w http.ResponseWriter, r *http.Request, path, msg string, isErr bool) {
	key := "msg"
	if isErr {
		key = "err"
	}
	http.Redirect(w, r, path+"?"+key+"="+url.QueryEscape(msg), http.StatusSeeOther)
}

// ---- notes ------------------------------------------------------------------

func (s *Server) indexPage(w http.ResponseWriter, r *http.Request) {
	list, err := server.BuildViewModel(r.Context(), notes.ListEndpoint{Store: s.Store, Links: s.Links}, apigen.ListNotesRequestObject{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "index", map[string]any{"Notes": list})
}

func (s *Server) notePage(w http.ResponseWriter, r *http.Request) {
	n, err := server.BuildViewModel(r.Context(), notes.GetEndpoint{Store: s.Store, Links: s.Links}, apigen.GetNoteRequestObject{NoteId: chi.URLParam(r, "id")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "note", map[string]any{"Note": n})
}

func (s *Server) createNote(w http.ResponseWriter, r *http.Request) {
	n, err := server.BuildViewModel(r.Context(), notes.CreateEndpoint{Store: s.Store, Links: s.Links}, apigen.CreateNoteRequestObject{
		Body: &apigen.NoteCreate{Title: r.FormValue("title"), Body: r.FormValue("body")},
	})
	if errors.Is(err, app.ErrBadInput) {
		redirect(w, r, "/", err.Error(), true)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/notes/"+url.PathEscape(n.ID), "Saved.", false)
}

func (s *Server) updateNote(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	title, body := r.FormValue("title"), r.FormValue("body")
	_, err := server.BuildViewModel(r.Context(), notes.UpdateEndpoint{Store: s.Store, Links: s.Links}, apigen.UpdateNoteRequestObject{
		NoteId: id,
		Body:   &apigen.NoteUpdate{Title: &title, Body: &body},
	})
	if errors.Is(err, app.ErrBadInput) {
		redirect(w, r, "/notes/"+url.PathEscape(id), err.Error(), true)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/notes/"+url.PathEscape(id), "Saved.", false)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	if _, err := server.BuildViewModel(r.Context(), notes.DeleteEndpoint{Store: s.Store}, apigen.DeleteNoteRequestObject{NoteId: chi.URLParam(r, "id")}); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/", "Deleted.", false)
}
