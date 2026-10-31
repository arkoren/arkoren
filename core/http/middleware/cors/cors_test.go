package cors_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/http/middleware/cors"
	"arkoren.dev/core/http/router"
)

// requireHeader fails when response does not contain the expected header value.
func requireHeader(t *testing.T, response *http.Response, name string, want string) {
	t.Helper()

	if got := response.Header.Get(name); got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

// TestMiddlewareHandlesPreflight verifies preflight handling.
func TestMiddlewareHandlesPreflight(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(corsPolicy *cors.Builder) {
		corsPolicy.AllowedOrigins(
			"https://example.com",
		)
		corsPolicy.AllowedMethods(
			"GET",
			"POST",
		)
		corsPolicy.AllowCredentials(true)
		corsPolicy.MaxAge(10 * time.Minute)
	})(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next handler called")

		return nil
	})

	request := httptest.NewRequest(http.MethodOptions, "/", nil)
	request.Header.Set("Origin", "https://example.com")
	request.Header.Set("Access-Control-Request-Method", "POST")

	response := router.Handler(handler).Record(request)

	if response.StatusCode != http.StatusNoContent {
		t.Fatal("preflight response did not preserve CORS behavior")
	}

	requireHeader(t, response, "Access-Control-Allow-Origin", "https://example.com")
	requireHeader(t, response, "Access-Control-Max-Age", "600")
}

// TestCORSUsesDefaults verifies default CORS response headers.
func TestCORSUsesDefaults(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware()(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodOptions, "/", nil)
	request.Header.Set("Origin", "https://example.com")
	request.Header.Set("Access-Control-Request-Method", "POST")

	response := router.Handler(handler).Record(request)

	requireHeader(t, response, "Access-Control-Allow-Origin", "*")
	requireHeader(t, response, "Access-Control-Allow-Methods", "GET, POST, HEAD")
	requireHeader(t, response, "Access-Control-Max-Age", "300")
}

// TestCORSPreservesConfiguredValues verifies configured CORS response headers.
func TestCORSPreservesConfiguredValues(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(corsPolicy *cors.Builder) {
		corsPolicy.AllowedOrigins("https://example.com")
		corsPolicy.AllowedMethods(http.MethodPut)
		corsPolicy.AllowedHeaders("X-Request-ID")
		corsPolicy.ExposedHeaders("X-Response-ID")
		corsPolicy.MaxAge(time.Minute)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodOptions, "/", nil)
	request.Header.Set("Origin", "https://example.com")
	request.Header.Set("Access-Control-Request-Method", "PUT")

	response := router.Handler(handler).Record(request)

	requireHeader(t, response, "Access-Control-Allow-Methods", "PUT")
	requireHeader(t, response, "Access-Control-Allow-Headers", "X-Request-ID")
	requireHeader(t, response, "Access-Control-Expose-Headers", "")
	requireHeader(t, response, "Access-Control-Max-Age", "60")

	actual := httptest.NewRequest(http.MethodPut, "/", nil)
	actual.Header.Set("Origin", "https://example.com")

	response = router.Handler(handler).Record(actual)

	requireHeader(t, response, "Access-Control-Expose-Headers", "X-Response-ID")
	requireHeader(t, response, "Access-Control-Allow-Methods", "")
}

// TestCORSPreservesExplicitZeroMaxAge verifies zero max-age omission.
func TestCORSPreservesExplicitZeroMaxAge(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(corsPolicy *cors.Builder) {
		corsPolicy.MaxAge(0)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodOptions, "/", nil)
	request.Header.Set("Origin", "https://example.com")
	request.Header.Set("Access-Control-Request-Method", "POST")

	response := router.Handler(handler).Record(request)

	if response.Header.Get("Access-Control-Max-Age") != "" {
		t.Fatal("explicit zero max age was defaulted")
	}
}

// TestCORSVariesOnOriginWithoutOrigin checks that responses still vary by
// Origin when the current request has none, protecting shared-cache
// correctness.
func TestCORSVariesOnOriginWithoutOrigin(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(corsPolicy *cors.Builder) {
		corsPolicy.AllowedOrigins("https://example.com")
		corsPolicy.MaxAge(0)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))

	requireHeader(t, response, "Vary", "Origin")
}

// TestCORSNormalizesConfiguredOrigin checks that host casing in configured
// origins does not prevent an equivalent request origin from being allowed.
func TestCORSNormalizesConfiguredOrigin(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(corsPolicy *cors.Builder) {
		corsPolicy.AllowedOrigins("https://Example.com")
		corsPolicy.MaxAge(0)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", "https://example.com")

	response := router.Handler(handler).Record(request)

	requireHeader(t, response, "Access-Control-Allow-Origin", "https://example.com")
}

// TestCORSRejectsInvalidOrigin checks that a configured origin containing a
// path is rejected at construction rather than treated as a valid origin.
func TestCORSRejectsInvalidOrigin(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("invalid origin accepted")
		}
	}()
	cors.Middleware(func(corsPolicy *cors.Builder) {
		corsPolicy.AllowedOrigins("https://example.com/")
		corsPolicy.MaxAge(0)
	})
}
