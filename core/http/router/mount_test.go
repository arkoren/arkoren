package router_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
)

// seen records what a mounted handler received.
type seen struct {
	// method, path, rawPath, and query capture the mounted request after prefix
	// rewriting; query keeps its original encoding.
	method, path, rawPath, query string
}

// recorder returns a handler that stores the request it served in *got and
// answers 200 with body.
func recorder(got *seen, body string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		*got = seen{
			request.Method,
			request.URL.Path,
			request.URL.RawPath,
			request.URL.RawQuery,
		}
		_, _ = writer.Write([]byte(body))
	})
}

// serve dispatches a bodiless request through handler and returns the response.
func serve(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))

	return response
}

// TestMountStripsThePrefix checks mount-boundary path rewriting while
// preserving the original query string passed to the mounted handler.
func TestMountStripsThePrefix(t *testing.T) {
	t.Parallel()

	var got seen
	r := router.New()
	r.Mount("/admin", recorder(&got, "mounted"))

	for target, wantPath := range map[string]string{
		"/admin":              "/",
		"/admin/":             "/",
		"/admin/users":        "/users",
		"/admin/users/7/edit": "/users/7/edit",
		"/admin/users/":       "/users/",
	} {
		response := serve(r, http.MethodGet, target)

		if response.Code != http.StatusOK || response.Body.String() != "mounted" || got.path != wantPath {
			t.Errorf("%s: status %d body %q path %q, want path %q", target, response.Code, response.Body, got.path, wantPath)
		}
	}

	serve(r, http.MethodGet, "/admin/users?q=a%20b")

	if got.path != "/users" || got.query != "q=a%20b" {
		t.Errorf("path %q query %q", got.path, got.query)
	}
}

// TestMountKeepsTheEscapedPathConsistent checks that prefix stripping updates
// decoded and escaped paths consistently for an encoded slash.
func TestMountKeepsTheEscapedPathConsistent(t *testing.T) {
	t.Parallel()

	var got seen
	r := router.New()
	r.Mount("/m", recorder(&got, ""))
	serve(r, http.MethodGet, "/m/a%2Fb")

	if got.path != "/a/b" || got.rawPath != "/a%2Fb" {
		t.Fatalf("path %q raw %q", got.path, got.rawPath)
	}
}

// TestMountPreservesEscapedSegmentsAfterEncodedPrefix verifies equivalent
// encodings of a literal prefix do not turn an escaped slash into a separator.
func TestMountPreservesEscapedSegmentsAfterEncodedPrefix(t *testing.T) {
	t.Parallel()

	inner := http.NewServeMux()
	inner.HandleFunc("GET /{name}", func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(request.PathValue("name")))
	})

	routes := router.New()
	routes.Mount("/m", inner)

	original := httptest.NewRequest(http.MethodGet, "/%6D/a%2fb?q=a%20b", nil)
	response := httptest.NewRecorder()

	routes.ServeHTTP(response, original)

	if response.Code != http.StatusOK || response.Body.String() != "a/b" {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}

	if original.URL.Path != "/m/a/b" || original.URL.RawPath != "/%6D/a%2fb" || original.URL.RawQuery != "q=a%20b" {
		t.Fatalf("original URL changed: %+v", original.URL)
	}
}

// TestMountUnescapesRegisteredPrefix verifies encoded literals in registration
// follow ServeMux's per-segment matching rules when the mounted path is stripped.
func TestMountUnescapesRegisteredPrefix(t *testing.T) {
	t.Parallel()

	var got seen
	routes := router.New()
	routes.Mount("/team%20one", recorder(&got, "mounted"))

	response := serve(routes, http.MethodGet, "/team%20one/member%2Fname")

	if response.Code != http.StatusOK || got.path != "/member/name" || got.rawPath != "/member%2Fname" {
		t.Fatalf("status = %d, mounted = %+v", response.Code, got)
	}
}

// TestMountStripsEscapedSlashPrefixBySegment verifies a slash within one
// registered segment is not counted as a path separator while stripping.
func TestMountStripsEscapedSlashPrefixBySegment(t *testing.T) {
	t.Parallel()

	var got seen
	routes := router.New()
	routes.Group("/ops", func(ops *router.Router) {
		ops.Mount("/m%2Fn", recorder(&got, "mounted"))
	})

	response := serve(routes, http.MethodGet, "/ops/m%2fn/a%2Fb")

	if response.Code != http.StatusOK || got.path != "/a/b" || got.rawPath != "/a%2Fb" {
		t.Fatalf("status = %d, mounted = %+v", response.Code, got)
	}
}

// TestMountStripsWildcardGroupPrefix verifies inherited wildcard segments are
// removed by their matched segment boundary while their values remain available
// to the mounted handler, including slashes encoded within a wildcard value.
func TestMountStripsWildcardGroupPrefix(t *testing.T) {
	t.Parallel()

	var got seen
	var team string
	routes := router.New()
	routes.Group("/teams/{team}", func(teams *router.Router) {
		teams.Mount("/files", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			team = request.PathValue("team")

			recorder(&got, "mounted").ServeHTTP(writer, request)
		}))
	})

	response := serve(routes, http.MethodGet, "/teams/a%2Fb/files/x%2fy")

	if response.Code != http.StatusOK || team != "a/b" {
		t.Fatalf("status = %d, team = %q", response.Code, team)
	}

	if got.path != "/x/y" || got.rawPath != "/x%2fy" {
		t.Fatalf("mounted = %+v", got)
	}
}

// TestMountServesEveryStandardMethod checks supported mount methods and
// confirms TRACE remains outside the default method set.
func TestMountServesEveryStandardMethod(t *testing.T) {
	t.Parallel()

	var got seen
	r := router.New()
	r.Mount("/m", recorder(&got, "ok"))

	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "QUERY"} {
		if code := serve(r, method, "/m/x").Code; code != http.StatusOK || got.method != method {
			t.Errorf("%s: status %d, handler saw %q", method, code, got.method)
		}
	}

	if code := serve(r, http.MethodTrace, "/m/x").Code; code != http.StatusMethodNotAllowed {
		t.Errorf("TRACE: status %d, want 405", code)
	}
}

// TestMountedHandlerThatWritesNothingAnswersOK checks that a mounted no-op
// standard handler finishes successfully without writing an explicit status.
func TestMountedHandlerThatWritesNothingAnswersOK(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Mount("/m", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
	}))

	if code := serve(r, http.MethodGet, "/m/x").Code; code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
}

// TestMountStripsTheWholeGroupPattern checks that nested group prefixes are
// removed together with the mount prefix and do not register an ungrouped
// route.
func TestMountStripsTheWholeGroupPattern(t *testing.T) {
	t.Parallel()

	var got seen
	r := router.New()
	r.Group("/ops", func(ops *router.Router) {
		ops.Group("/tools", func(tools *router.Router) {
			tools.Mount("/inspect", recorder(&got, ""))
		})
	})

	if code := serve(r, http.MethodGet, "/ops/tools/inspect/a/b").Code; code != http.StatusOK || got.path != "/a/b" {
		t.Fatalf("status %d path %q", code, got.path)
	}

	if code := serve(r, http.MethodGet, "/inspect/a").Code; code != http.StatusNotFound {
		t.Fatalf("ungrouped path status = %d", code)
	}
}

// TestMountAtTheRootKeepsTheWholePath checks that mounting at the root
// preserves the complete path seen by the mounted handler.
func TestMountAtTheRootKeepsTheWholePath(t *testing.T) {
	t.Parallel()

	var got seen
	r := router.New()
	r.Mount("/", recorder(&got, ""))

	for target, wantPath := range map[string]string{
		"/":    "/",
		"/a/b": "/a/b",
	} {
		if code := serve(r, http.MethodGet, target).Code; code != http.StatusOK || got.path != wantPath {
			t.Errorf("%s: status %d path %q", target, code, got.path)
		}
	}
}

// TestMountedHandlersRunInsideTheRoutersMiddleware checks that router
// middleware wraps mounted handlers in the same registration order as ordinary
// routes.
func TestMountedHandlersRunInsideTheRoutersMiddleware(t *testing.T) {
	t.Parallel()

	var order []string
	trace := func(name string) router.Middleware {
		return func(next router.Handler) router.Handler {
			return func(writer http.ResponseWriter, request *http.Request) error {
				order = append(order, name)

				return next(writer, request)
			}
		}
	}

	var got seen
	r := router.New()
	r.Use(trace("outer"), trace("inner"))
	r.Mount("/m", recorder(&got, "ok"))

	if code := serve(r, http.MethodGet, "/m/x").Code; code != http.StatusOK || strings.Join(order, ",") != "outer,inner" {
		t.Fatalf("status %d, middleware order %v", code, order)
	}
}

// TestMountedHandlerIsNotReachedWhenMiddlewareRejects checks that a middleware
// error stops a mounted standard handler before it runs.
func TestMountedHandlerIsNotReachedWhenMiddlewareRejects(t *testing.T) {
	t.Parallel()

	reached := false
	r := router.New()
	r.Use(func(router.Handler) router.Handler {
		return func(http.ResponseWriter, *http.Request) error {
			return errors.New("stop")
		}
	})
	r.Mount("/m", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))

	if code := serve(r, http.MethodGet, "/m/x").Code; code != http.StatusInternalServerError || reached {
		t.Fatalf("status %d, reached %v", code, reached)
	}
}

// TestMountIsListedOncePerMethodWithTheHandlersOwnName checks that mounted
// routes expose the original handler identity and one listing per supported
// method.
func TestMountIsListedOncePerMethodWithTheHandlersOwnName(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Group("/ops", func(ops *router.Router) {
		ops.Mount("/files", http.FileServerFS(nil))
	})
	r.Mount("/fn", http.HandlerFunc(namedHandler))

	byPattern := map[string][]router.Route{}

	for _, route := range r.Routes() {
		byPattern[route.Pattern] = append(byPattern[route.Pattern], route)
	}

	files := byPattern["/ops/files/{path...}"]

	if len(files) != 8 || files[0].Method != http.MethodGet || files[7].Method != "QUERY" {
		t.Fatalf("files routes = %+v", files)
	}

	if files[0].Handler != "http.fileHandler" && files[0].Handler != "*http.fileHandler" {
		t.Fatalf("handler = %q, want the file server's own type", files[0].Handler)
	}

	if fn := byPattern["/fn/{path...}"]; len(fn) != 8 || !strings.HasSuffix(fn[0].Handler, "namedHandler") || !strings.Contains(fn[0].Source, "mount_test.go:") {
		t.Fatalf("fn routes = %+v", fn)
	}
}

// namedHandler is a handler with a stable name for the Routes test.
func namedHandler(http.ResponseWriter, *http.Request) {
}

// TestMountRejectsWildcardPrefixes checks that wildcard mount prefixes panic
// at registration rather than creating ambiguous prefix rewrites.
func TestMountRejectsWildcardPrefixes(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("a wildcard prefix did not panic")
		}
	}()
	router.New().Mount("/users/{id}", http.NotFoundHandler())
}

// TestMountSealsTheRouterLikeAnyRoute checks that mount registration seals
// middleware configuration just like ordinary route registration.
func TestMountSealsTheRouterLikeAnyRoute(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Mount("/m", http.NotFoundHandler())

	defer func() {
		if recover() == nil {
			t.Fatal("Use after Mount did not panic")
		}
	}()
	r.Use(func(next router.Handler) router.Handler {
		return next
	})
}
