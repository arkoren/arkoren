package request

import (
	"net/http"

	"arkoren.dev/core/pagination"
)

// Query parameter names read by [Page] and [Cursor].
const (
	// pageParam is the query parameter holding the page number.
	pageParam = "page"

	// perPageParam is the query parameter holding the page size.
	perPageParam = "per_page"

	// cursorParam is the query parameter holding the opaque cursor.
	cursorParam = "cursor"
)

// Page reads the page and per_page query parameters into offset pagination
// parameters and normalizes them with [pagination.PageParams.Normalize]
// and opts. A missing parameter uses the default, a present value below
// one is raised to one, and a value that is not an integer returns
// [ErrInvalidQueryParameter]. A maximum below one returns
// [pagination.ErrInvalidConfig].
//
// Example:
//
//	params, err := request.Page(r)
//	if err != nil {
//		return err
//	}
//	_ = params.PerPage
func Page(r *http.Request, opts ...pagination.Option) (pagination.PageParams, error) {
	page, err := paginationInt(r, pageParam)

	if err != nil {
		return pagination.PageParams{}, err
	}

	perPage, err := paginationInt(r, perPageParam)

	if err != nil {
		return pagination.PageParams{}, err
	}

	return pagination.PageParams{
		Page:    page,
		PerPage: perPage,
	}.Normalize(opts...)
}

// Cursor reads the cursor and per_page query parameters into cursor
// pagination parameters and normalizes them with
// [pagination.CursorParams.Normalize] and opts. A missing per_page uses the
// default, a present value below one is raised to one, and a value that is
// not an integer returns [ErrInvalidQueryParameter]. A maximum below one
// returns [pagination.ErrInvalidConfig].
//
// Example:
//
//	params, err := request.Cursor(r)
//	if err != nil {
//		return err
//	}
//	_ = params.Cursor
func Cursor(r *http.Request, opts ...pagination.CursorOption) (pagination.CursorParams, error) {
	perPage, err := paginationInt(r, perPageParam)

	if err != nil {
		return pagination.CursorParams{}, err
	}

	return pagination.CursorParams{
		Cursor:  QueryOr(r, cursorParam, ""),
		PerPage: perPage,
	}.Normalize(opts...)
}

// paginationInt reads an integer query parameter. An absent parameter
// yields zero, which normalization replaces with the default. A present
// value is raised to one so that an explicit zero is not mistaken for
// absent.
func paginationInt(r *http.Request, name string) (int, error) {
	if !HasQuery(r, name) {
		return 0, nil
	}

	value, err := Query[int](r, name)

	if err != nil {
		return 0, err
	}

	return max(value, 1), nil
}
