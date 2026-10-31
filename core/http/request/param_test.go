package request_test

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// UserID is a named integer type used to verify that named types parse.
type UserID int64

// pathReq returns a request whose path parameter "v" is raw.
func pathReq(raw string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("v", raw)

	return req
}

// queryReq returns a request whose query parameter "v" is raw.
func queryReq(raw string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/?v="+raw, nil)
}

// TestParamParsesEveryScalarKind verifies each supported kind parses.
func TestParamParsesEveryScalarKind(t *testing.T) {
	t.Parallel()

	if v, err := request.Param[string](pathReq("abc"), "v"); err != nil || v != "abc" {
		t.Fatalf("string: %v %v", v, err)
	}

	if v, err := request.Param[bool](pathReq("true"), "v"); err != nil || !v {
		t.Fatalf("bool: %v %v", v, err)
	}

	if v, err := request.Param[int](pathReq("-42"), "v"); err != nil || v != -42 {
		t.Fatalf("int: %v %v", v, err)
	}

	if v, err := request.Param[int8](pathReq("-128"), "v"); err != nil || v != -128 {
		t.Fatalf("int8: %v %v", v, err)
	}

	if v, err := request.Param[int16](pathReq("300"), "v"); err != nil || v != 300 {
		t.Fatalf("int16: %v %v", v, err)
	}

	if v, err := request.Param[int32](pathReq("70000"), "v"); err != nil || v != 70000 {
		t.Fatalf("int32: %v %v", v, err)
	}

	if v, err := request.Param[int64](pathReq("9000000000"), "v"); err != nil || v != 9000000000 {
		t.Fatalf("int64: %v %v", v, err)
	}

	if v, err := request.Param[uint](pathReq("7"), "v"); err != nil || v != 7 {
		t.Fatalf("uint: %v %v", v, err)
	}

	if v, err := request.Param[uint8](pathReq("255"), "v"); err != nil || v != 255 {
		t.Fatalf("uint8: %v %v", v, err)
	}

	if v, err := request.Param[uint16](pathReq("65535"), "v"); err != nil || v != 65535 {
		t.Fatalf("uint16: %v %v", v, err)
	}

	if v, err := request.Param[uint32](pathReq("4294967295"), "v"); err != nil || v != 4294967295 {
		t.Fatalf("uint32: %v %v", v, err)
	}

	if v, err := request.Param[uint64](pathReq("18446744073709551615"), "v"); err != nil || v != 18446744073709551615 {
		t.Fatalf("uint64: %v %v", v, err)
	}

	if v, err := request.Param[float32](pathReq("1.5"), "v"); err != nil || v != 1.5 {
		t.Fatalf("float32: %v %v", v, err)
	}

	if v, err := request.Param[float64](pathReq("-2.25"), "v"); err != nil || v != -2.25 {
		t.Fatalf("float64: %v %v", v, err)
	}
}

// TestParamParsesNamedType verifies named types use their underlying kind.
func TestParamParsesNamedType(t *testing.T) {
	t.Parallel()

	id, err := request.Param[UserID](pathReq("42"), "v")

	if err != nil || id != UserID(42) {
		t.Fatalf("got %v %v", id, err)
	}
}

// TestParamRejectsInvalidValues verifies inputs that must all be rejected.
func TestParamRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	if _, err := request.Param[int8](pathReq("300"), "v"); err == nil {
		t.Fatal("int8 overflow accepted")
	}

	if _, err := request.Param[uint](pathReq("-1"), "v"); err == nil {
		t.Fatal("negative uint accepted")
	}

	if _, err := request.Param[uint8](pathReq("256"), "v"); err == nil {
		t.Fatal("uint8 overflow accepted")
	}

	if _, err := request.Param[float64](pathReq("x"), "v"); err == nil {
		t.Fatal("bad float accepted")
	}

	if _, err := request.Param[bool](pathReq("maybe"), "v"); err == nil {
		t.Fatal("bad bool accepted")
	}

	if _, err := request.Param[int](pathReq("1.5"), "v"); err == nil {
		t.Fatal("fractional int accepted")
	}
}

// TestParamEmptyAndMissingAreErrors verifies empty values fail for every type.
func TestParamEmptyAndMissingAreErrors(t *testing.T) {
	t.Parallel()

	if _, err := request.Param[string](pathReq(""), "v"); !errors.As(err, new(problem.Details)) {
		t.Fatalf("string empty: %v", err)
	}

	if _, err := request.Param[int](pathReq(""), "v"); !errors.As(err, new(problem.Details)) {
		t.Fatalf("int empty: %v", err)
	}

	if _, err := request.Param[int](pathReq("1"), "other"); !errors.As(err, new(problem.Details)) {
		t.Fatalf("missing: %v", err)
	}
}

// TestParamReturnsInvalidPathParameter verifies the problem metadata.
func TestParamReturnsInvalidPathParameter(t *testing.T) {
	t.Parallel()

	_, err := request.Param[int8](pathReq("300"), "v")
	details := requireParameterProblemDetails(t, err)

	assertInvalidParameterProblem(t, details, "Invalid path parameter", "The path parameter has an invalid value.")
	assertProblemParameter(t, details, "v")

	if details.Extensions()["expected_type"] != "int8" {
		t.Fatalf("got %#v", details)
	}

	requireParameterErrorCause(t, details.Unwrap())
}

// TestParamNamedTypeReportsTypeString verifies the type attribute of named types.
func TestParamNamedTypeReportsTypeString(t *testing.T) {
	t.Parallel()

	_, err := request.Param[UserID](pathReq("x"), "v")
	details := requireParameterProblemDetails(t, err)

	if details.Extensions()["expected_type"] != "request_test.UserID" {
		t.Fatalf("got %#v", details)
	}
}

// TestParamOrFallsBack verifies the fallback for missing and unparseable values.
func TestParamOrFallsBack(t *testing.T) {
	t.Parallel()

	if got := request.ParamOr(pathReq("9"), "v", 1); got != 9 {
		t.Fatalf("got %d", got)
	}

	if got := request.ParamOr(pathReq("nope"), "v", 1); got != 1 {
		t.Fatalf("got %d", got)
	}

	if got := request.ParamOr(pathReq("1"), "other", "dflt"); got != "dflt" {
		t.Fatalf("got %q", got)
	}

	if got := request.ParamOr(pathReq("300"), "v", int8(5)); got != 5 {
		t.Fatalf("got %d", got)
	}
}

// TestQueryParsesTypes verifies query parsing across kinds, including named types.
func TestQueryParsesTypes(t *testing.T) {
	t.Parallel()

	if v, err := request.Query[string](queryReq("hi"), "v"); err != nil || v != "hi" {
		t.Fatalf("string: %v %v", v, err)
	}

	if v, err := request.Query[bool](queryReq("1"), "v"); err != nil || !v {
		t.Fatalf("bool: %v %v", v, err)
	}

	if v, err := request.Query[uint16](queryReq("65535"), "v"); err != nil || v != 65535 {
		t.Fatalf("uint16: %v %v", v, err)
	}

	if v, err := request.Query[float32](queryReq("0.5"), "v"); err != nil || v != 0.5 {
		t.Fatalf("float32: %v %v", v, err)
	}

	if v, err := request.Query[UserID](queryReq("7"), "v"); err != nil || v != 7 {
		t.Fatalf("named: %v %v", v, err)
	}
}

// TestQueryRejectsInvalidValues verifies inputs that must all be rejected.
func TestQueryRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	if _, err := request.Query[int8](queryReq("300"), "v"); err == nil {
		t.Fatal("int8 overflow accepted")
	}

	if _, err := request.Query[uint](queryReq("-1"), "v"); err == nil {
		t.Fatal("negative uint accepted")
	}

	if _, err := request.Query[float64](queryReq("x"), "v"); err == nil {
		t.Fatal("bad float accepted")
	}

	if _, err := request.Query[bool](queryReq("maybe"), "v"); err == nil {
		t.Fatal("bad bool accepted")
	}
}

// TestQueryRendersInvalidQueryParameter verifies invalid query rendering.
func TestQueryRendersInvalidQueryParameter(t *testing.T) {
	t.Parallel()

	handler := router.Handler(func(_ http.ResponseWriter, req *http.Request) error {
		_, err := request.Query[int](req, "page")

		return err
	})

	req := httptest.NewRequest(http.MethodGet, "/?page=nope", nil)
	req.Header.Set("Accept", "application/problem+json")

	response := handler.Record(req)
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest || response.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	var details problem.Details

	if err := json.UnmarshalRead(response.Body, &details); err != nil {
		t.Fatal(err)
	}

	assertInvalidParameterProblem(t, details, "Invalid query parameter", "The query parameter has an invalid value.")
}

// TestQueryEmptyAndAbsentAreErrors verifies empty and absent values fail, even for string.
func TestQueryEmptyAndAbsentAreErrors(t *testing.T) {
	t.Parallel()

	_, err := request.Query[int](httptest.NewRequest(http.MethodGet, "/?page=", nil), "page")
	details := requireParameterProblemDetails(t, err)

	assertInvalidParameterProblem(t, details, "Invalid query parameter", "The query parameter has an invalid value.")
	assertProblemParameter(t, details, "page")
	requireNoParameterErrorCause(t, details.Unwrap())

	if _, err := request.Query[string](queryReq(""), "v"); !errors.As(err, new(problem.Details)) {
		t.Fatalf("string empty: %v", err)
	}

	if _, err := request.Query[string](queryReq("a"), "other"); !errors.As(err, new(problem.Details)) {
		t.Fatalf("string absent: %v", err)
	}
}

// TestQueryOrFallsBack verifies the fallback for absent, empty, and unparseable values.
func TestQueryOrFallsBack(t *testing.T) {
	t.Parallel()

	if got := request.QueryOr(queryReq("9"), "v", 1); got != 9 {
		t.Fatalf("got %d", got)
	}

	if got := request.QueryOr(queryReq("nope"), "v", 1); got != 1 {
		t.Fatalf("got %d", got)
	}

	if got := request.QueryOr(queryReq("a"), "other", "dflt"); got != "dflt" {
		t.Fatalf("got %q", got)
	}

	if got := request.QueryOr(queryReq(""), "v", "dflt"); got != "dflt" {
		t.Fatalf("got %q", got)
	}

	if got := request.QueryOr(queryReq("x"), "v", ""); got != "x" {
		t.Fatalf("got %q", got)
	}
}

// requireParameterProblemDetails returns problem details or fails the test.
func requireParameterProblemDetails(t *testing.T, err error) problem.Details {
	t.Helper()

	var details problem.Details

	if !errors.As(err, &details) {
		t.Fatalf("expected problem details, got %T", err)
	}

	return details
}

// assertInvalidParameterProblem verifies invalid parameter problem metadata.
func assertInvalidParameterProblem(t *testing.T, details problem.Details, title, detail string) {
	t.Helper()

	if details.Status() != http.StatusBadRequest {
		t.Fatalf("got %#v", details)
	}

	if details.Title() != title {
		t.Fatalf("got %#v", details)
	}

	if details.Detail() != detail {
		t.Fatalf("got %#v", details)
	}
}

// assertProblemParameter verifies that a problem identifies the invalid parameter.
func assertProblemParameter(t *testing.T, details problem.Details, parameter string) {
	t.Helper()

	if details.Extensions()["parameter"] != parameter {
		t.Fatalf("got %#v", details)
	}
}

// requireParameterErrorCause fails the test when a cause is missing.
func requireParameterErrorCause(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected decode error to be retained")
	}
}

// requireNoParameterErrorCause fails the test when an unexpected cause is present.
func requireNoParameterErrorCause(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("got %v", err)
	}
}
