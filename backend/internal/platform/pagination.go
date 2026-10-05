package platform

import (
	"net/http"
	"strconv"
	"strings"
)

// MaxRequestBodyBytes caps inbound JSON payloads at 1 MiB. Larger control-plane
// payloads should reference blobs in object storage rather than inline bytes.
const MaxRequestBodyBytes = 1 << 20

// DefaultPageSize and MaxPageSize bound every list endpoint.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Page describes a validated offset/limit pair plus its cursor metadata.
type Page struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// PageResult wraps a slice of records with cursor metadata for list responses.
type PageResult[T any] struct {
	Items   []T  `json:"items"`
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}

// NewPageResult assembles a list envelope, computing HasMore from total and
// the requested window.
func NewPageResult[T any](items []T, total, limit, offset int) PageResult[T] {
	if items == nil {
		items = []T{}
	}
	return PageResult[T]{
		Items:   items,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+len(items) < total,
	}
}

// ParsePage reads `limit` and `offset` query parameters, clamping to
// MaxPageSize and rejecting negative or non-numeric values.
func ParsePage(r *http.Request) (Page, error) {
	q := r.URL.Query()

	page := Page{Limit: DefaultPageSize, Offset: 0}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Page{}, NewBadRequest("limit must be an integer")
		}
		if n < 1 {
			return Page{}, NewBadRequest("limit must be greater than 0")
		}
		if n > MaxPageSize {
			n = MaxPageSize
		}
		page.Limit = n
	}

	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Page{}, NewBadRequest("offset must be an integer")
		}
		if n < 0 {
			return Page{}, NewBadRequest("offset must not be negative")
		}
		page.Offset = n
	}

	return page, nil
}

// Sort is a validated `sort_by` / `sort_order` pair from the query string.
type Sort struct {
	By    string
	Order string
}

// ParseSort validates sorting against the set of columns the endpoint allows,
// which prevents unvalidated identifiers reaching the SQL layer.
func ParseSort(r *http.Request, allowed map[string]string, fallback Sort) (Sort, error) {
	q := r.URL.Query()

	out := fallback

	if raw := q.Get("sort_by"); raw != "" {
		column, ok := allowed[raw]
		if !ok {
			return Sort{}, NewBadRequest("sort_by %q is not a sortable field", raw)
		}
		out.By = column
	}

	switch order := q.Get("sort_order"); order {
	case "":
		out.Order = fallback.Order
	case "asc", "desc":
		out.Order = order
	case "ASC", "DESC":
		out.Order = strings.ToLower(order)
	default:
		return Sort{}, NewBadRequest("sort_order must be one of: asc, desc")
	}

	return out, nil
}

// Direction returns the SQL keyword for the sort order. Only "desc" yields
// "DESC"; every other value is coerced to the safe ascending default.
func (s Sort) Direction() string {
	if s.Order == "desc" {
		return "DESC"
	}
	return "ASC"
}
