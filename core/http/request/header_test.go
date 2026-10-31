package request_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/request"
)

// TestHeaderEmptyFirstValueUsesFallback checks native first-value semantics.
func TestHeaderEmptyFirstValueUsesFallback(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Add("X-Mode", "")
	r.Header.Add("X-Mode", "later")

	if request.HasHeader(r, "X-Mode") || request.HeaderOr(r, "X-Mode", "fallback") != "fallback" {
		t.Fatal("empty first header value did not retain fallback semantics")
	}
}
