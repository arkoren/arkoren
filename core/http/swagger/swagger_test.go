package swagger_test

import (
	"encoding/json/v2"
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

// docsRouter returns a router with a viewer for specificationURL registered as
// a group at prefix, the way applications use it.
func docsRouter(t *testing.T, prefix, specificationURL string, configure ...swagger.BuilderFunc) *router.Router {
	t.Helper()

	viewer, err := swagger.New(specificationURL, configure...)

	if err != nil {
		t.Fatal(err)
	}

	routes := router.New()
	routes.Group(prefix, viewer.Register)

	return routes
}

// request dispatches a bodiless request through handler and returns the response.
func request(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))

	return response
}

// TestViewerServesPageUnderGroup checks that a group at /prefix/docs serves the
// page and that it links its three assets with relative URLs.
func TestViewerServesPageUnderGroup(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/prefix/docs", "/openapi.json")
	response := request(routes, http.MethodGet, "/prefix/docs/")

	if response.Code != http.StatusOK {
		t.Fatalf("status %d", response.Code)
	}

	for _, link := range []string{`href="./swagger-ui.css"`, `src="./swagger-ui-bundle.js"`, `src="./initializer.js"`} {
		if !strings.Contains(response.Body.String(), link) {
			t.Fatalf("page missing %s: %s", link, response.Body.String())
		}
	}

	if strings.Contains(response.Body.String(), "/prefix") {
		t.Fatal("page must not embed the mount path")
	}
}

// TestViewerRedirectsToTrailingSlash checks the relative 308 for the bare path, dropping the query.
func TestViewerRedirectsToTrailingSlash(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/prefix/docs", "/openapi.json")
	response := request(routes, http.MethodGet, "/prefix/docs?a=1")

	if response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != "docs/" {
		t.Fatalf("status %d, location %q", response.Code, response.Header().Get("Location"))
	}
}

// TestViewerRedirectIgnoresForwardingHeaders ensures the Location never depends
// on the request's Host or forwarding headers.
func TestViewerRedirectIgnoresForwardingHeaders(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/docs", "/openapi.json")
	incoming := httptest.NewRequest(http.MethodGet, "https://untrusted.test/docs", nil)
	incoming.Header.Set("Forwarded", "host=attacker.test;proto=http")
	incoming.Header.Set("X-Forwarded-Host", "attacker.test")
	incoming.Header.Set("X-Forwarded-Prefix", "/evil")

	response := httptest.NewRecorder()

	routes.ServeHTTP(response, incoming)

	if response.Header().Get("Location") != "docs/" {
		t.Fatalf("location %q", response.Header().Get("Location"))
	}
}

// TestViewerRedirectRejectsUnsafeSegments checks that a last segment outside
// the unreserved set is a 404 and never reaches the Location header.
func TestViewerRedirectRejectsUnsafeSegments(t *testing.T) {
	t.Parallel()

	for _, segment := range []string{"a:b", "a%2Fb", "a%5Cb", "%2F%2Fevil.test", "%2e%2e", "a%20b", "a%3Ab"} {
		routes := docsRouter(t, "/"+segment, "/openapi.json")
		response := request(routes, http.MethodGet, "/"+segment)

		if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
			t.Errorf("%q: status %d, location %q", segment, response.Code, response.Header().Get("Location"))
		}
	}
}

// TestViewerRootGroupServesWithoutRedirect checks the router root: the page is
// "/" and the assets sit beside it.
func TestViewerRootGroupServesWithoutRedirect(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/", "openapi.json")
	page := request(routes, http.MethodGet, "/")

	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `src="./initializer.js"`) {
		t.Fatalf("page: %d %s", page.Code, page.Body.String())
	}

	if request(routes, http.MethodGet, "/initializer.js").Code != http.StatusOK {
		t.Fatal("initializer missing at root")
	}
}

// TestViewerEscapesConfiguration ensures hostile configuration remains data.
func TestViewerEscapesConfiguration(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/docs", `/spec?name="</script><script>alert(1)</script>&x=1`,
		func(page *swagger.Builder) {
			page.Title(`<img src=x onerror=alert(1)>`)
		})
	page := request(routes, http.MethodGet, "/docs/")

	if strings.Contains(page.Body.String(), "<img") || !strings.Contains(page.Body.String(), "&lt;img") {
		t.Fatal("title was not HTML-escaped")
	}

	script := request(routes, http.MethodGet, "/docs/initializer.js")

	if strings.Contains(script.Body.String(), "</script>") {
		t.Fatal("initializer contains unescaped HTML")
	}

	for _, required := range []string{`\u003c`, `\"`, "validatorUrl:null", "queryConfigEnabled:false"} {
		if !strings.Contains(script.Body.String(), required) {
			t.Fatalf("initializer missing %q: %s", required, script.Body.String())
		}
	}
}

// TestViewerLeavesRelativeSpecificationToTheBrowser checks that a relative URL
// is emitted as written and request metadata cannot change it.
func TestViewerLeavesRelativeSpecificationToTheBrowser(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/prefix/docs", "../openapi.json?version=3.2")
	incoming := httptest.NewRequest(http.MethodGet, "https://untrusted.test/prefix/docs/initializer.js", nil)
	incoming.Header.Set("Forwarded", "host=attacker.test;proto=http")

	response := httptest.NewRecorder()

	routes.ServeHTTP(response, incoming)

	if !strings.Contains(response.Body.String(), `url:"../openapi.json?version=3.2"`) {
		t.Fatal(response.Body.String())
	}
}

// TestViewerRejectsUnsafeSpecificationURLs checks one browser-URL safety policy.
func TestViewerRejectsUnsafeSpecificationURLs(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"", "//evil.test/a", "javascript:alert(1)", "data:text/plain,a", "https:spec", "https:///a", "https://user:secret@example.test/spec", "/spec#fragment", "/spec#", "/spec\\x", " /spec", "/spec\n", "/%zz"} {
		if _, err := swagger.New(address); err == nil {
			t.Fatalf("accepted specification %q", address)
		}
	}
}

// TestViewerAcceptsHTTPSSpecification verifies that cross-origin specs are an
// explicit caller choice, not inferred from the incoming request host.
func TestViewerAcceptsHTTPSSpecification(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/docs", "https://api.example.test/openapi.json")
	response := request(routes, http.MethodGet, "/docs/initializer.js")

	if !strings.Contains(response.Body.String(), `url:"https://api.example.test/openapi.json"`) {
		t.Fatal(response.Body.String())
	}
}

// TestViewerRoutesAreExact checks the route listing and that nothing else is
// reachable: no listing, traversal or unspecified embedded file.
func TestViewerRoutesAreExact(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/docs", "/openapi.json")

	var patterns []string

	for _, route := range routes.Routes() {
		if route.Method != http.MethodGet {
			t.Fatalf("method %q", route.Method)
		}

		patterns = append(patterns, route.Pattern)
	}

	want := []string{"/docs", "/docs/swagger-ui.css", "/docs/swagger-ui-bundle.js", "/docs/initializer.js"}

	if !reflect.DeepEqual(patterns, want) {
		t.Fatalf("patterns = %v, want %v", patterns, want)
	}

	for _, address := range []string{"/other", "/docsx/swagger-ui.css", "/docs/assets/", "/docs/LICENSE", "/docs/swagger-ui-bundle.js.map", "/docs/x/swagger-ui.css"} {
		if response := request(routes, http.MethodGet, address); response.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d", address, response.Code)
		}
	}
}

// TestViewerHeadMatchesGet ensures GET/HEAD parity for all public resources,
// including the generated page and initializer.
func TestViewerHeadMatchesGet(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/docs", "/openapi.json")

	for _, address := range []string{"/docs/", "/docs/initializer.js", "/docs/swagger-ui.css", "/docs/swagger-ui-bundle.js"} {
		get := request(routes, http.MethodGet, address)
		head := request(routes, http.MethodHead, address)

		if head.Code != http.StatusOK || head.Body.Len() != 0 || get.Body.Len() == 0 {
			t.Fatalf("%s: invalid GET/HEAD response", address)
		}

		if !reflect.DeepEqual(head.Header(), get.Header()) {
			t.Fatalf("%s: mismatched GET/HEAD headers", address)
		}
	}
}

// TestViewerDoesNotOverrideCSP checks compatibility with application policy and
// ensures that configuration-sensitive representations are never cached.
func TestViewerDoesNotOverrideCSP(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/docs", "/openapi.json")

	for _, address := range []string{"/docs/", "/docs/initializer.js"} {
		response := httptest.NewRecorder()
		response.Header().Set("Content-Security-Policy", "script-src 'self'; style-src 'self' 'unsafe-inline'")
		routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, address, nil))

		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("configuration can be cached")
		}

		if response.Header().Get("Content-Security-Policy") != "script-src 'self'; style-src 'self' 'unsafe-inline'" {
			t.Fatal("application CSP changed")
		}
	}
}

// TestViewerRejectsOtherMethods checks that the router refuses non-GET methods.
func TestViewerRejectsOtherMethods(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/docs", "/openapi.json")
	response := request(routes, http.MethodPost, "/docs/")

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", response.Code)
	}
}

// TestViewerPublishesImmutableConfiguration races retained-draft mutation with
// requests; the construction draft must never remain reachable by the viewer.
func TestViewerPublishesImmutableConfiguration(t *testing.T) {
	t.Parallel()

	var retained *swagger.Builder

	routes := docsRouter(t, "/docs", "/openapi.json", func(page *swagger.Builder) {
		retained = page
		page.Title("Original")
	})

	var workers sync.WaitGroup
	workers.Go(func() {
		for range 100 {
			retained.Title("Changed")
		}
	})

	for range 4 {
		workers.Go(func() {
			for range 25 {
				if !strings.Contains(request(routes, http.MethodGet, "/docs/").Body.String(), "<title>Original</title>") {
					t.Error("published viewer changed")
				}
			}
		})
	}

	workers.Wait()
}

// TestViewerReturnsWriteErrors ensures the router can observe failed writes.
func TestViewerReturnsWriteErrors(t *testing.T) {
	t.Parallel()

	viewer, err := swagger.New("/openapi.json")

	if err != nil {
		t.Fatal(err)
	}

	var got error

	routes := router.New()
	routes.Use(func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, incoming *http.Request) error {
			got = next(writer, incoming)

			return got
		}
	})
	routes.Group("/docs", viewer.Register)

	routes.ServeHTTP(&failedWriter{ResponseRecorder: httptest.NewRecorder()}, httptest.NewRequest(http.MethodGet, "/docs/", nil))

	if !errors.Is(got, io.ErrClosedPipe) {
		t.Fatalf("write error = %v", got)
	}
}

// TestViewerPageUsesOnlyExternalLocalResources verifies the HTML can run with
// script-src 'self', without allowing inline script or a third-party CDN.
func TestViewerPageUsesOnlyExternalLocalResources(t *testing.T) {
	t.Parallel()

	routes := docsRouter(t, "/docs", "/openapi.json")
	body := request(routes, http.MethodGet, "/docs/").Body.String()

	for _, forbidden := range []string{"<script>", "https://", "http://", "style=", "onload="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("page contains %q", forbidden)
		}
	}

	if strings.Count(body, `<script src="./`) != 2 {
		t.Fatal("page must load exactly the local bundle and initializer")
	}
}

// TestNewCallbacksRunOnceInOrder checks draft semantics independently of
// input validation; even invalid configuration must invoke supplied callbacks.
func TestNewCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var calls []string

	_, err := swagger.New("", func(page *swagger.Builder) {
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

// TestRegisterExportsOperations verifies Register annotates every route, so the
// router exports the four documentation paths without errors, each with a
// summary and a 200 response, and the page also documents its redirect.
func TestRegisterExportsOperations(t *testing.T) {
	t.Parallel()

	viewer, err := swagger.New("/openapi.json")
	if err != nil {
		t.Fatal(err)
	}

	routes := router.New()
	routes.Group("/docs", viewer.Register)

	data, err := routes.OpenAPI("Docs", "1")
	if err != nil {
		t.Fatalf("OpenAPI() error = %v", err)
	}

	var document struct {
		Paths map[string]map[string]struct {
			Summary   string                    `json:"summary"`
			Responses map[string]map[string]any `json:"responses"`
		} `json:"paths"`
	}

	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	if len(document.Paths) != 4 {
		t.Fatalf("paths = %d, want 4", len(document.Paths))
	}

	for path, methods := range document.Paths {
		operation := methods["get"]

		if operation.Summary == "" || operation.Responses["200"] == nil {
			t.Errorf("%s operation = %+v, want summary and 200", path, operation)
		}
	}

	if document.Paths["/docs"]["get"].Responses["308"] == nil {
		t.Error("page does not document its redirect")
	}
}
