package cors_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/http/middleware/cors"
	"arkoren.dev/core/http/router"
)

// requireOriginPanic checks construction rejection through the public API,
// without depending on the private origin parser or a particular panic string.
func requireOriginPanic(t *testing.T, origin string) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Errorf("configured origin %q was accepted", origin)
		}
	}()
	cors.Middleware(func(policy *cors.Builder) {
		policy.AllowedOrigins(origin)
	})
}

// TestCORSRejectsNonOriginURLs covers URL components and host patterns that
// cannot appear in the middleware's documented bare, serialized origins.
func TestCORSRejectsNonOriginURLs(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{
		"https://user:password@app.example",
		"https://app.example#fragment",
		"https://app.example#",
		"https://app.example?",
		"https://*.example",
		"https://:443",
		"https://[not-ipv6]",
		"https://app.example:65536",
		"https://app.example:",
	} {
		requireOriginPanic(t, origin)
	}
}

// TestCORSCredentialValidationRunsAfterCallbacks keeps validation at publication:
// later callbacks may replace an intermediate wildcard before credentials apply.
func TestCORSCredentialValidationRunsAfterCallbacks(t *testing.T) {
	t.Parallel()

	order := 0
	handler := cors.Middleware(func(policy *cors.Builder) {
		order++
		policy.AllowCredentials(true)
	}, func(policy *cors.Builder) {
		if order != 1 {
			t.Fatal("callbacks did not run once in order")
		}

		order++
		policy.AllowedOrigins("https://app.example")
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", "https://app.example")

	response := router.Handler(handler).Record(request)

	requireHeader(t, response, "Access-Control-Allow-Credentials", "true")

	if order != 2 {
		t.Fatalf("callbacks = %d, want two", order)
	}
}

// TestCORSRejectsWildcardCredentialsAfterCallbacks confirms that a wildcard in
// any allow-list position cannot authorize credentialed browser requests.
func TestCORSRejectsWildcardCredentialsAfterCallbacks(t *testing.T) {
	t.Parallel()

	order := 0
	defer func() {
		if recover() == nil || order != 2 {
			t.Fatal("expected credential policy rejection after both callbacks")
		}
	}()
	cors.Middleware(func(policy *cors.Builder) {
		order++
		policy.AllowedOrigins("https://app.example", "*")
	}, func(policy *cors.Builder) {
		order++
		policy.AllowCredentials(true)
	})
}

// TestCORSMaxAgeRoundsDownWithoutFloatLoss checks a valid long duration just
// below a whole second, where float conversion can round the header upward.
func TestCORSMaxAgeRoundsDownWithoutFloatLoss(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(policy *cors.Builder) {
		policy.MaxAge((1<<30)*time.Second - time.Nanosecond)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodOptions, "/", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)

	response := router.Handler(handler).Record(request)

	requireHeader(t, response, "Access-Control-Max-Age", "1073741823")
}

// TestCORSExplicitEmptyOriginsStayClosed prevents security validation from
// turning an intentional nonnil empty list back into the permissive default.
func TestCORSExplicitEmptyOriginsStayClosed(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(policy *cors.Builder) {
		policy.AllowedOrigins([]string{}...)
		policy.AllowCredentials(true)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", "https://app.example")

	response := router.Handler(handler).Record(request)

	requireHeader(t, response, "Access-Control-Allow-Origin", "")
	requireHeader(t, response, "Access-Control-Allow-Credentials", "")
}

// TestCORSExplicitEmptyPreflightListsStayEmpty preserves independent method,
// header, and cache-header omissions without resetting them to defaults.
func TestCORSExplicitEmptyPreflightListsStayEmpty(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(policy *cors.Builder) {
		policy.AllowedMethods([]string{}...)
		policy.AllowedHeaders([]string{}...)
		policy.MaxAge(0)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodOptions, "/", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)

	response := router.Handler(handler).Record(request)

	requireHeader(t, response, "Access-Control-Allow-Origin", "*")
	requireHeader(t, response, "Access-Control-Allow-Methods", "")
	requireHeader(t, response, "Access-Control-Allow-Headers", "")
	requireHeader(t, response, "Access-Control-Max-Age", "")
}

// TestCORSNilOriginListRestoresDefault keeps nil distinct from a nonnil empty
// list even when callbacks explicitly replace one with the other.
func TestCORSNilOriginListRestoresDefault(t *testing.T) {
	t.Parallel()

	handler := cors.Middleware(func(policy *cors.Builder) {
		policy.AllowedOrigins([]string{}...)
	}, func(policy *cors.Builder) {
		policy.AllowedOrigins(nil...)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", "https://app.example")

	response := router.Handler(handler).Record(request)

	requireHeader(t, response, "Access-Control-Allow-Origin", "*")
}
