package request_test

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
)

// TestQueryFirstValueAndEmptyDefault preserves first-value parsing.
func TestQueryFirstValueAndEmptyDefault(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/?name=&name=later", nil)

	if !request.HasQuery(r, "name") || request.QueryOr(r, "name", "fallback") != "fallback" {
		t.Fatal("empty first query value did not retain presence and fallback semantics")
	}
}

// TestQueryDecodesURLValueOnce checks native URL decoding without repetition.
func TestQueryDecodesURLValueOnce(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/?name=a+b%252Fc", nil)

	value, err := request.Query[string](r, "name")

	if err != nil || value != "a b%2Fc" {
		t.Fatalf("value = %q, error = %v, want one URL decode", value, err)
	}
}

// TestQuerySkipsMalformedURLPairs preserves net/url's parsing policy.
func TestQuerySkipsMalformedURLPairs(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/?value=%ZZ&value=2", nil)

	value, err := request.Query[int](r, "value")

	if err != nil || value != 2 {
		t.Fatalf("value = %v, error = %v, want first successfully parsed URL value", value, err)
	}
}

// TestQueryFloatAllowsNaN records native Go scalar parsing, not a JSON schema.
func TestQueryFloatAllowsNaN(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/?value=NaN", nil)

	value, err := request.Query[float64](r, "value")

	if err != nil || !math.IsNaN(value) {
		t.Fatalf("value = %v, error = %v, want native floating-point parsing", value, err)
	}
}

// TestQuerySchemaMetadataDoesNotValidateValues checks descriptive metadata.
func TestQuerySchemaMetadataDoesNotValidateValues(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Get("/", func(writer http.ResponseWriter, r *http.Request) error {
		value, err := request.Query[int](r, "limit")

		if err != nil {
			return err
		}

		return response.JSON(writer, http.StatusOK, value)
	}, func(operation *router.OperationBuilder) {
		operation.Parameter("limit", "query", router.RawSchema(`{"type":"integer","minimum":10}`))
		operation.JSONResponse(http.StatusOK, router.RawSchema(`{"type":"integer"}`))
	})

	if _, err := routes.OpenAPI("Queries", "1"); err != nil {
		t.Fatal(err)
	}

	writer := httptest.NewRecorder()

	routes.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/?limit=1", nil))

	if writer.Code != http.StatusOK || writer.Body.String() != "1\n" {
		t.Fatalf("response = %d %q, want handler-owned query validation", writer.Code, writer.Body.String())
	}
}
