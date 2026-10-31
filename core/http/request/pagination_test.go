package request_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/pagination"
	"arkoren.dev/foundation/problem"
)

// TestPageDefaults verifies missing parameters use the defaults.
func TestPageDefaults(t *testing.T) {
	t.Parallel()

	params, err := request.Page(httptest.NewRequest(http.MethodGet, "/", nil))

	if err != nil || params != (pagination.PageParams{
		Page:    1,
		PerPage: 25,
	}) {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestPageValues verifies present parameters are read.
func TestPageValues(t *testing.T) {
	t.Parallel()

	params, err := request.Page(httptest.NewRequest(http.MethodGet, "/?page=4&per_page=10", nil))

	if err != nil || params != (pagination.PageParams{
		Page:    4,
		PerPage: 10,
	}) {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestPageExplicitZero verifies a present zero is raised to one.
func TestPageExplicitZero(t *testing.T) {
	t.Parallel()

	params, err := request.Page(httptest.NewRequest(http.MethodGet, "/?page=0&per_page=0", nil))

	if err != nil || params != (pagination.PageParams{
		Page:    1,
		PerPage: 1,
	}) {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestPageNegative verifies negative values are raised to one.
func TestPageNegative(t *testing.T) {
	t.Parallel()

	params, err := request.Page(httptest.NewRequest(http.MethodGet, "/?page=-1&per_page=-5", nil))

	if err != nil || params != (pagination.PageParams{
		Page:    1,
		PerPage: 1,
	}) {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestPageCapsPerPage verifies per_page respects the maximum.
func TestPageCapsPerPage(t *testing.T) {
	t.Parallel()

	params, err := request.Page(httptest.NewRequest(http.MethodGet, "/?per_page=500", nil))

	if err != nil || params.PerPage != 100 {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestPageNotANumber verifies a malformed value returns a 400 problem.
func TestPageNotANumber(t *testing.T) {
	t.Parallel()

	_, err := request.Page(httptest.NewRequest(http.MethodGet, "/?page=x", nil))
	var details problem.Details

	if !errors.As(err, &details) || details.Status() != http.StatusBadRequest {
		t.Fatalf("got %v", err)
	}
}

// TestPageEmptyValue verifies an empty value returns a 400 problem.
func TestPageEmptyValue(t *testing.T) {
	t.Parallel()

	_, err := request.Page(httptest.NewRequest(http.MethodGet, "/?per_page=", nil))
	var details problem.Details

	if !errors.As(err, &details) || details.Title() != request.ErrInvalidQueryParameter.Title() {
		t.Fatalf("got %v", err)
	}
}

// TestPageOverflow verifies an overflowing page offset is rejected.
func TestPageOverflow(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/?page=9223372036854775807&per_page=100", nil)

	if _, err := request.Page(r); !errors.Is(err, pagination.ErrPageOverflow) {
		t.Fatalf("got %v, want ErrPageOverflow", err)
	}
}

// TestPageOptions verifies option-based configuration.
func TestPageOptions(t *testing.T) {
	t.Parallel()

	params, err := request.Page(httptest.NewRequest(http.MethodGet, "/?per_page=80", nil),
		pagination.WithDefaultPage(3),
		pagination.WithDefaultPerPage(10),
		pagination.WithMaxPerPage(50),
	)

	if err != nil || params != (pagination.PageParams{
		Page:    3,
		PerPage: 50,
	}) {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestPageInvalidMaximum verifies a negative maximum is rejected.
func TestPageInvalidMaximum(t *testing.T) {
	t.Parallel()

	_, err := request.Page(httptest.NewRequest(http.MethodGet, "/", nil), pagination.WithMaxPerPage(-1))

	if !errors.Is(err, pagination.ErrInvalidConfig) {
		t.Fatalf("got %v, want ErrInvalidConfig", err)
	}
}

// TestCursorDefaults verifies missing parameters use the default.
func TestCursorDefaults(t *testing.T) {
	t.Parallel()

	params, err := request.Cursor(httptest.NewRequest(http.MethodGet, "/", nil))

	if err != nil || params != (pagination.CursorParams{
		PerPage: 25,
	}) {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestCursorValues verifies present parameters are read.
func TestCursorValues(t *testing.T) {
	t.Parallel()

	params, err := request.Cursor(httptest.NewRequest(http.MethodGet, "/?cursor=abc&per_page=10", nil))

	if err != nil || params != (pagination.CursorParams{
		Cursor:  "abc",
		PerPage: 10,
	}) {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestCursorNegative verifies a negative per_page is raised to one.
func TestCursorNegative(t *testing.T) {
	t.Parallel()

	params, err := request.Cursor(httptest.NewRequest(http.MethodGet, "/?per_page=-1", nil))

	if err != nil || params.PerPage != 1 {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestCursorCapsPerPage verifies per_page respects the maximum.
func TestCursorCapsPerPage(t *testing.T) {
	t.Parallel()

	params, err := request.Cursor(httptest.NewRequest(http.MethodGet, "/?per_page=500", nil))

	if err != nil || params.PerPage != 100 {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestCursorNotANumber verifies a malformed value returns a 400 problem.
func TestCursorNotANumber(t *testing.T) {
	t.Parallel()

	_, err := request.Cursor(httptest.NewRequest(http.MethodGet, "/?per_page=x", nil))
	var details problem.Details

	if !errors.As(err, &details) || details.Status() != http.StatusBadRequest {
		t.Fatalf("got %v", err)
	}
}

// TestCursorOptions verifies option-based configuration.
func TestCursorOptions(t *testing.T) {
	t.Parallel()

	params, err := request.Cursor(httptest.NewRequest(http.MethodGet, "/", nil),
		pagination.WithCursorDefaultPerPage(7),
		pagination.WithCursorMaxPerPage(9),
	)

	if err != nil || params.PerPage != 7 {
		t.Fatalf("got %+v, %v", params, err)
	}
}

// TestCursorInvalidMaximum verifies a negative maximum is rejected.
func TestCursorInvalidMaximum(t *testing.T) {
	t.Parallel()

	_, err := request.Cursor(httptest.NewRequest(http.MethodGet, "/", nil), pagination.WithCursorMaxPerPage(-1))

	if !errors.Is(err, pagination.ErrInvalidConfig) {
		t.Fatalf("got %v, want ErrInvalidConfig", err)
	}
}
