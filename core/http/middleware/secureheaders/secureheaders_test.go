package secureheaders_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/middleware/secureheaders"
	"arkoren.dev/core/http/router"
)

// ExampleBuilderFunc demonstrates that explicit empty settings stay omitted and
// do not turn off other recommended defaults or opt into persistent HSTS.
func ExampleBuilderFunc() {
	configure := secureheaders.BuilderFunc(func(headers *secureheaders.Builder) {
		headers.FrameOptions("")
	})

	routes := router.New()
	routes.Use(secureheaders.Middleware(configure))

	recorder := httptest.NewRecorder()

	routes.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://service.example/", nil))
	fmt.Printf("%q %q %q\n", recorder.Header().Get("X-Frame-Options"),
		recorder.Header().Get("Strict-Transport-Security"), recorder.Header().Get("X-Content-Type-Options"))
	// Output: "" "" "nosniff"
}

// TestRecommendedDefaultsDoNotOverrideExplicitEmpty checks the distinction
// between initial recommendations and post-callback normalization. Retaining
// the draft cannot restore a header on already published middleware.
func TestRecommendedDefaultsDoNotOverrideExplicitEmpty(t *testing.T) {
	t.Parallel()

	var retained *secureheaders.Builder
	handler := secureheaders.Middleware(func(headers *secureheaders.Builder) {
		retained = headers
		headers.FrameOptions("")
		headers.ContentSecurityPolicy("")
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})
	retained.FrameOptions("DENY")

	response := router.Handler(handler).Record(httptest.NewRequest("GET", "/", nil))

	if response.Header.Get("X-Frame-Options") != "" || response.Header.Get("Content-Security-Policy") != "" {
		t.Fatal("explicit empty was replaced or draft remained live")
	}

	if response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("initial recommended defaults were lost")
	}
}

// TestSecureHeadersSetsDefaultsOnHTTPS verifies recommended headers remain
// enabled on TLS requests without imposing a persistent HTTPS policy.
func TestSecureHeadersSetsDefaultsOnHTTPS(t *testing.T) {
	t.Parallel()

	handler := secureheaders.Middleware()(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "https://example.com/", nil))

	if response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("X-XSS-Protection") != "0" {
		t.Fatal("default headers missing")
	}

	if got := response.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("frame options = %q", got)
	}

	if got := response.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("referrer policy = %q", got)
	}

	if got := response.Header.Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Fatalf("csp = %q", got)
	}

	if _, present := response.Header["Strict-Transport-Security"]; present {
		t.Fatal("default HSTS header present on HTTPS")
	}

	if got := response.Header.Get("Permissions-Policy"); got != "" {
		t.Fatalf("permissions policy = %q", got)
	}
}

// TestSecureHeadersPreservesConfiguredValues verifies configured security headers.
func TestSecureHeadersPreservesConfiguredValues(t *testing.T) {
	t.Parallel()

	handler := secureheaders.Middleware(
		func(headers *secureheaders.Builder) {
			headers.FrameOptions("SAMEORIGIN")
		},
		func(headers *secureheaders.Builder) {
			headers.ContentSecurityPolicy("default-src 'self'")
			headers.StrictTransportSecurity("max-age=86400")
		},
	)(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "https://example.com/", nil))

	if response.Header.Get("X-Frame-Options") != "SAMEORIGIN" || response.Header.Get("Content-Security-Policy") != "default-src 'self'" {
		t.Fatal("configured headers missing")
	}

	if got := response.Header.Get("Strict-Transport-Security"); got != "max-age=86400" {
		t.Fatalf("hsts = %q", got)
	}
}

// TestSecureHeadersPreservesExplicitEmptyValue verifies disabled headers remain absent.
func TestSecureHeadersPreservesExplicitEmptyValue(t *testing.T) {
	t.Parallel()

	handler := secureheaders.Middleware(func(headers *secureheaders.Builder) {
		headers.FrameOptions("")
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Header.Get("X-Frame-Options") != "" {
		t.Fatal("explicitly disabled header was restored")
	}
}

// TestSecureHeadersOmitsHSTSOnPlainHTTP checks that plain HTTP omits HSTS
// while retaining the default Content-Security-Policy header.
func TestSecureHeadersOmitsHSTSOnPlainHTTP(t *testing.T) {
	t.Parallel()

	handler := secureheaders.Middleware(func(headers *secureheaders.Builder) {
		headers.StrictTransportSecurity("max-age=86400")
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))

	if got := response.Header.Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("hsts = %q on plain HTTP", got)
	}

	if got := response.Header.Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Fatalf("csp = %q", got)
	}
}

// TestSecureHeadersSendsHSTSBehindTLSProxy checks that a request marked HTTPS
// by its forwarding proxy receives the configured HSTS policy.
func TestSecureHeadersSendsHSTSBehindTLSProxy(t *testing.T) {
	t.Parallel()

	handler := secureheaders.Middleware(func(headers *secureheaders.Builder) {
		headers.StrictTransportSecurity("max-age=86400")
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")

	response := router.Handler(handler).Record(request)

	if got := response.Header.Get("Strict-Transport-Security"); got != "max-age=86400" {
		t.Fatalf("hsts = %q", got)
	}
}

// TestSecureHeadersDefaultsOmitHSTSBehindTLSProxy verifies forwarded HTTPS
// detection alone does not opt a deployment into persistent browser policy.
func TestSecureHeadersDefaultsOmitHSTSBehindTLSProxy(t *testing.T) {
	t.Parallel()

	handler := secureheaders.Middleware()(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")

	response := router.Handler(handler).Record(request)

	if _, present := response.Header["Strict-Transport-Security"]; present {
		t.Fatal("default HSTS header present behind TLS proxy")
	}
}

// TestSecureHeadersExplicitEmptyOmitsHSTS verifies a later empty setting
// overrides an opt-in policy even on HTTPS, rather than restoring a default.
func TestSecureHeadersExplicitEmptyOmitsHSTS(t *testing.T) {
	t.Parallel()

	handler := secureheaders.Middleware(func(headers *secureheaders.Builder) {
		headers.StrictTransportSecurity("max-age=86400")
		headers.StrictTransportSecurity("")
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "https://example.com/", nil))

	if _, present := response.Header["Strict-Transport-Security"]; present {
		t.Fatal("explicitly omitted HSTS header restored")
	}
}
