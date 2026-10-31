package request_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/request"
)

// TestCookieEmptyFirstValueUsesFallback preserves first parsed cookie selection.
func TestCookieEmptyFirstValueUsesFallback(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Cookie", "theme=; theme=dark")

	if value := request.CookieValueOr(r, "theme", "light"); value != "light" {
		t.Fatalf("theme = %q, want fallback for the first empty cookie", value)
	}
}
