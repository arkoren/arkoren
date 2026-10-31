package request

import (
	"net/http"

	"arkoren.dev/foundation/problem"
)

// ErrInvalidQueryParameter is returned when a query parameter is absent, empty,
// or cannot be parsed as the requested type. The problem carries the
// "parameter" name and the "expected_type" Go type.
//
// Example:
//
//	failure := request.ErrInvalidQueryParameter.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrInvalidQueryParameter = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/request:invalid-query-parameter")
	details.Title("Invalid query parameter")
	details.Detail("The query parameter has an invalid value.")
	details.Status(http.StatusBadRequest)
})

// HasQuery reports whether the named parameter exists in net/url's parsed query,
// regardless of its value. Malformed pairs omitted by net/url are not present.
// It is a presence check, not validation of the complete raw query string.
//
// Example:
//
//	if request.HasQuery(r, "verbose") {
//		return response.String(w, http.StatusOK, "verbose mode")
//	}
//	return response.Status(w, http.StatusNoContent)
func HasQuery(r *http.Request, name string) bool {
	return r.URL.Query().Has(name)
}

// Query retrieves the first value of the named query parameter and parses it
// as T, with the same rules as [Param]. An absent or empty value is an error
// for every T, including string.
//
// URL decoding follows net/url.URL.Query, including its omission of malformed
// query pairs. Scalar conversion follows strconv, not a JSON Schema: float
// types accept NaN and infinities, and bool accepts values such as "1".
// OpenAPI metadata does not add validation; enforce application constraints
// separately when the route requires a stricter value or URL grammar.
//
// It returns [ErrInvalidQueryParameter] with the parameter name and type
// attached, and the parse error as its cause when there is one.
//
// Example:
//
//	limit, err := request.Query[int](r, "limit")
//	if err != nil {
//		return err
//	}
//	_ = limit
func Query[T Scalar](r *http.Request, name string) (T, error) {
	return parseScalar[T](r.URL.Query().Get(name), name, ErrInvalidQueryParameter)
}

// QueryOr is like [Query] but returns fallback when the parameter is absent,
// empty, or cannot be parsed as T. A present but empty "?q=" therefore also
// yields the fallback.
//
// Example:
//
//	search := request.QueryOr(r, "q", "")
func QueryOr[T Scalar](r *http.Request, name string, fallback T) T {
	value, err := Query[T](r, name)

	if err != nil {
		return fallback
	}

	return value
}
