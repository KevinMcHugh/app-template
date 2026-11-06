package app_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/KevinMcHugh/app-template/internal/app"
)

// rows is a list in memory, sorted by its key, with a fetch that pages it
// the way a keyset query does.
type rows []string

func (r rows) fetch(after *string, limit int32) ([]string, error) {
	i := 0
	if after != nil {
		i, _ = slices.BinarySearch(r, *after)
		if i < len(r) && r[i] == *after {
			i++
		}
	}
	return r[i:min(len(r), i+int(limit))], nil
}

func key(s string) string { return s }

func TestPaginate(t *testing.T) {
	list := rows{"a", "b", "c", "d", "e"}
	var got []string
	req := app.PageRequest{List: "letter", Limit: 2}
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("paging never ended")
		}
		p, err := app.Paginate(req, list.fetch, key)
		if err != nil {
			t.Fatal(err)
		}
		if p.Len() > 2 {
			t.Fatalf("page of %d for a limit of 2", p.Len())
		}
		got = append(got, p.Items...)
		if p.Next == "" {
			break
		}
		req.After = p.Next
	}
	if !slices.Equal(got, list) {
		t.Fatalf("paged %v, want %v", got, list)
	}

	// A list that fits on one page has no next, and the default limit holds.
	p, err := app.Paginate(app.PageRequest{List: "letter"}, list.fetch, key)
	if err != nil || p.Next != "" || p.Len() != len(list) {
		t.Fatalf("one page: %+v %v", p, err)
	}
	// A page that ends exactly at the end of the list has no next either.
	p, err = app.Paginate(app.PageRequest{List: "letter", Limit: 5}, list.fetch, key)
	if err != nil || p.Next != "" {
		t.Fatalf("an exact page: %+v %v", p, err)
	}
}

func TestPaginateRefuses(t *testing.T) {
	list := rows{"a", "b", "c"}
	first, _ := app.Paginate(app.PageRequest{List: "letter", Limit: 1}, list.fetch, key)
	for name, req := range map[string]app.PageRequest{
		"a negative limit":           {List: "letter", Limit: -1},
		"a limit over the most":      {List: "letter", Limit: app.MaxLimit + 1},
		"a cursor that is not one":   {List: "letter", After: "not a cursor"},
		"a cursor from another list": {List: "word", After: first.Next},
	} {
		if _, err := app.Paginate(req, list.fetch, key); !errors.Is(err, app.ErrBadInput) {
			t.Errorf("%s: %v, want bad input", name, err)
		}
	}
}
