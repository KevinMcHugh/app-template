package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Page sizes: what a list returns when the caller does not say, and the
// most it returns when they do.
const (
	DefaultLimit = 100
	MaxLimit     = 200
)

// PageRequest is the page a caller asked for in a list: how many (0 for
// DefaultLimit) and where to start (the Next of the page before, or "" for
// the first page). Every list operation takes them as its limit and after
// query parameters, and its endpoint names the list (server.Paged).
type PageRequest struct {
	List  string // what the list is of ("note"), so a cursor from another list is refused
	Limit int
	After string
}

// Size is the page's length, or ErrBadInput when the caller asked for one
// out of bounds.
func (p PageRequest) Size() (int, error) {
	switch {
	case p.Limit == 0:
		return DefaultLimit, nil
	case p.Limit < 1 || p.Limit > MaxLimit:
		return 0, fmt.Errorf("%w: limit is %d; it must be from 1 to %d", ErrBadInput, p.Limit, MaxLimit)
	}
	return p.Limit, nil
}

// Page is one page of a list: its items, in the list's order, and the
// cursor to send as after for the next page, empty on the last page.
type Page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next"`
}

// Len is how many items the page holds; server.Run checks it against the
// limit the caller asked for.
func (p Page[T]) Len() int { return len(p.Items) }

// MapPage is p with each item made into a U, for an endpoint's Build.
func MapPage[T, U any](p Page[T], f func(T) U) Page[U] {
	out := Page[U]{Items: make([]U, len(p.Items)), Next: p.Next}
	for i, it := range p.Items {
		out.Items[i] = f(it)
	}
	return out
}

// Paginate is one page of a keyset-paginated list, as p asks. The list's
// query orders its rows by a key that is unique per row (the sort columns,
// ending in the id), and takes the key of the last row on the page before
// as where to start. key gives a row's key; fetch runs the query, starting after
// the given key (nil for the first page) and returning at most limit rows.
// Paginate asks fetch for one row more than the page holds, to know
// whether there is a next page without counting.
//
// The cursor it hands out is the last row's key, in JSON, with the list's
// name so a cursor from one list is refused by another; it is opaque to
// callers, who only ever send back what they were given. A cursor is a
// position, not a permission: the query still filters to what the caller
// may see.
func Paginate[Row, Key any](p PageRequest, fetch func(after *Key, limit int32) ([]Row, error), key func(Row) Key) (Page[Row], error) {
	size, err := p.Size()
	if err != nil {
		return Page[Row]{}, err
	}
	after, err := decodeCursor[Key](p.List, p.After)
	if err != nil {
		return Page[Row]{}, err
	}
	rows, err := fetch(after, int32(size+1))
	if err != nil {
		return Page[Row]{}, err
	}
	if len(rows) <= size {
		return Page[Row]{Items: rows}, nil
	}
	rows = rows[:size]
	next, err := encodeCursor(p.List, key(rows[size-1]))
	return Page[Row]{Items: rows, Next: next}, err
}

// cursor is what a Next holds before it is encoded.
type cursor[Key any] struct {
	List  string `json:"l"`
	After Key    `json:"a"`
}

func encodeCursor[Key any](list string, k Key) (string, error) {
	b, err := json.Marshal(cursor[Key]{List: list, After: k})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor[Key any](list, s string) (*Key, error) {
	if s == "" {
		return nil, nil
	}
	bad := fmt.Errorf("%w: after is not a cursor from this list of %s; send the next value a page of it returned", ErrBadInput, list)
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, bad
	}
	var c cursor[Key]
	if err := json.Unmarshal(b, &c); err != nil || c.List != list {
		return nil, bad
	}
	return &c.After, nil
}
