package swagger_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/swagger"
)

// TestHandlerServesBothPagePaths checks explicit router wiring and local URLs.
func TestHandlerServesBothPagePaths(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/prefix/docs", "/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	routes := router.New()
	routes.Get("/prefix/docs", endpoint)
	routes.Get("/prefix/docs/{path...}", endpoint)

	for _, address := range []string{"/prefix/docs", "/prefix/docs/"} {
		response := httptest.NewRecorder()

		routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, address, nil))

		if response.Code != http.StatusOK {
			t.Fatalf("%s: status %d", address, response.Code)
		}

		if strings.Count(response.Body.String(), `"/prefix/docs/`) != 3 {
			t.Fatal("page must load exactly three local assets")
		}
	}
}

// TestHandlerEscapesConfiguration ensures hostile configuration remains data.
func TestHandlerEscapesConfiguration(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/docs", `/spec?name="</script><script>alert(1)</script>&x=1`,
		func(page *swagger.Builder) {
			page.Title(`<img src=x onerror=alert(1)>`)
		})

	if err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRecorder()

	endpoint.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/docs", nil))

	if strings.Contains(page.Body.String(), "<img") || !strings.Contains(page.Body.String(), "&lt;img") {
		t.Fatal("title was not HTML-escaped")
	}

	script := httptest.NewRecorder()

	endpoint.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/docs/initializer.js", nil))

	if strings.Contains(script.Body.String(), "</script>") {
		t.Fatal("initializer contains unescaped HTML")
	}

	for _, required := range []string{`\u003c`, `\"`, "validatorUrl:null", "queryConfigEnabled:false"} {
		if !strings.Contains(script.Body.String(), required) {
			t.Fatalf("initializer missing %q: %s", required, script.Body.String())
		}
	}
}

// TestHandlerResolvesRelativeSpecification independently of slash and host checks
// that forwarded request metadata cannot change configuration.
func TestHandlerResolvesRelativeSpecification(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/prefix/docs", "../openapi.json?version=3.2")

	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "https://untrusted.test/prefix/docs/initializer.js", nil)
	request.Header.Set("Forwarded", "host=attacker.test;proto=http")

	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if !strings.Contains(response.Body.String(), `url:"/prefix/openapi.json?version=3.2"`) {
		t.Fatal(response.Body.String())
	}
}

// TestHandlerRejectsInvalidBasePaths checks one invalid-path contract.
func TestHandlerRejectsInvalidBasePaths(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"", "docs", "/docs/", "//docs", "/../docs", "/a/./b", "/a%2fb", "/a?b", "/a#b", "/{path...}", "/a\\b", "/a b"} {
		if _, err := swagger.Handler(base, "/openapi.json"); err == nil {
			t.Fatalf("accepted base %q", base)
		}
	}
}

// TestHandlerRejectsUnsafeSpecificationURLs checks one browser-URL safety policy.
func TestHandlerRejectsUnsafeSpecificationURLs(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"", "//evil.test/a", "javascript:alert(1)", "data:text/plain,a", "https:spec", "https:///a", "https://user:secret@example.test/spec", "/spec#fragment", "/spec#", "/spec\\x", " /spec", "/spec\n", "/%zz"} {
		if _, err := swagger.Handler("/docs", address); err == nil {
			t.Fatalf("accepted specification %q", address)
		}
	}
}

// TestHandlerAcceptsHTTPSSpecification verifies that cross-origin specs are an
// explicit caller choice, not inferred from the incoming request host.
func TestHandlerAcceptsHTTPSSpecification(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/docs", "https://api.example.test/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/docs/initializer.js", nil))

	if !strings.Contains(response.Body.String(), `url:"https://api.example.test/openapi.json"`) {
		t.Fatal(response.Body.String())
	}
}

// TestHandlerUnknownPathsReturnNotFound ensures no listing or traversal fallback.
func TestHandlerUnknownPathsReturnNotFound(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/docs", "/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	for _, address := range []string{"/other", "/docsx/swagger-ui.css", "/docs/assets/", "/docs/LICENSE", "/docs/../swagger-ui.css", "/docs/a/../swagger-ui.css", "/docs/swagger-ui.css/", "/docs/swagger-ui-bundle.js.map"} {
		response := httptest.NewRecorder()

		endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, address, nil))

		if response.Code != http.StatusNotFound || response.Body.Len() != 0 {
			t.Fatalf("%s: status %d, body %s", address, response.Code, response.Body.String())
		}
	}
}

// TestHandlerHeadAssetsPreservesHeaders ensures correct GET/HEAD parity for all
// public resources, including the generated page and initializer.
func TestHandlerHeadAssetsPreservesHeaders(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/docs", "/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	for _, address := range []string{"/docs", "/docs/initializer.js", "/docs/swagger-ui.css", "/docs/swagger-ui-bundle.js"} {
		get := httptest.NewRecorder()

		endpoint.ServeHTTP(get, httptest.NewRequest(http.MethodGet, address, nil))

		head := httptest.NewRecorder()

		endpoint.ServeHTTP(head, httptest.NewRequest(http.MethodHead, address, nil))

		if head.Code != http.StatusOK || head.Body.Len() != 0 || get.Body.Len() == 0 {
			t.Fatalf("%s: invalid GET/HEAD response", address)
		}

		if !reflect.DeepEqual(head.Header(), get.Header()) {
			t.Fatalf("%s: mismatched GET/HEAD headers", address)
		}
	}
}

// TestHandlerDoesNotOverrideCSP checks compatibility with application policy and
// ensures that configuration-sensitive representations are never cached.
func TestHandlerDoesNotOverrideCSP(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/docs", "/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	for _, address := range []string{"/docs", "/docs/initializer.js"} {
		response := httptest.NewRecorder()
		response.Header().Set("Content-Security-Policy", "script-src 'self'; style-src 'self' 'unsafe-inline'")
		endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, address, nil))

		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("configuration can be cached")
		}

		if response.Header().Get("Content-Security-Policy") != "script-src 'self'; style-src 'self' 'unsafe-inline'" {
			t.Fatal("application CSP changed")
		}
	}
}

// TestHandlerRejectsOtherMethods checks the endpoint's defensive method boundary.
func TestHandlerRejectsOtherMethods(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/docs", "/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/docs", nil))

	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("missing method rejection")
	}
}

// TestHandlerRootBaseServesPage checks the special root-prefix boundary.
func TestHandlerRootBaseServesPage(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/", "openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `src="/initializer.js"`) {
		t.Fatal("root base failed")
	}
}

// TestHandlerPublishesImmutableConfiguration races retained-draft mutation with
// requests; the construction draft must never remain reachable by the handler.
func TestHandlerPublishesImmutableConfiguration(t *testing.T) {
	t.Parallel()

	var retained *swagger.Builder

	endpoint, err := swagger.Handler("/docs", "/openapi.json", func(page *swagger.Builder) {
		retained = page
		page.Title("Original")
	})

	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	workers.Go(func() {
		for range 100 {
			retained.Title("Changed")
		}
	})

	for range 4 {
		workers.Go(func() {
			for range 25 {
				response := httptest.NewRecorder()

				endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/docs", nil))

				if !strings.Contains(response.Body.String(), "<title>Original</title>") {
					t.Error("published handler changed")
				}
			}
		})
	}

	workers.Wait()
}

// TestHandlerReturnsWriteErrors ensures the router can observe failed writes.
func TestHandlerReturnsWriteErrors(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/docs", "/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	writer := &failedWriter{ResponseRecorder: httptest.NewRecorder()}

	if err := endpoint(writer, httptest.NewRequest(http.MethodGet, "/docs", nil)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error = %v", err)
	}
}

// TestHandlerPageUsesOnlyExternalLocalResources verifies the HTML can run with
// script-src 'self', without allowing inline script or a third-party CDN.
func TestHandlerPageUsesOnlyExternalLocalResources(t *testing.T) {
	t.Parallel()

	endpoint, err := swagger.Handler("/docs", "/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/docs", nil))

	body := response.Body.String()

	for _, forbidden := range []string{"<script>", "https://", "http://", "style=", "onload="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("page contains %q", forbidden)
		}
	}

	if strings.Count(body, `<script src="/docs/`) != 2 {
		t.Fatal("page must load exactly the local bundle and initializer")
	}
}

// TestHandlerCallbacksRunOnceInOrder checks draft semantics independently of
// input validation; even invalid configuration must invoke supplied callbacks.
func TestHandlerCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var calls []string

	_, err := swagger.Handler("invalid", "/openapi.json", func(page *swagger.Builder) {
		calls = append(calls, "first")
		page.Title("First")
	}, func(page *swagger.Builder) {
		calls = append(calls, "second")
		page.Title("Second")
	})

	if err == nil || strings.Join(calls, ",") != "first,second" {
		t.Fatalf("error %v, calls %v", err, calls)
	}
}

// failedWriter simulates a disconnected client without sockets or timing.
// Both byte and string writes fail so io.WriteString cannot bypass the failure
// through the embedded recorder's successful StringWriter implementation.
type failedWriter struct {
	// ResponseRecorder captures headers; both write methods are overridden below.
	*httptest.ResponseRecorder
}

// Write reports the deterministic transport error without consuming bytes.
func (writer *failedWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

// WriteString reports the same transport failure as Write without consuming
// bytes, including when io.WriteString selects the StringWriter fast path.
func (writer *failedWriter) WriteString(string) (int, error) {
	return 0, io.ErrClosedPipe
}
