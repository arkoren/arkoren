package router_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/http/router"
)

// TestRouterHas reports registrations for both trailing-slash forms.
func TestRouterHas(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/foo/", func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusOK)

		return nil
	})

	for _, pattern := range []string{
		"/foo",
		"/foo/",
	} {
		if !r.Has(http.MethodGet, pattern) {
			t.Fatalf("router should have %s", pattern)
		}
	}
}

// TestRouterRendersNotFoundAsProblem renders unmatched routes as Problem Details.
func TestRouterRendersNotFoundAsProblem(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/missing", nil)
	request.Header.Set("Accept", "application/problem+json")

	response := router.New().Record(request)

	if response.StatusCode != http.StatusNotFound || response.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
}

// TestRouterRendersMethodNotAllowedAsProblem renders unsupported methods as Problem Details.
func TestRouterRendersMethodNotAllowedAsProblem(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/users", func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/users", nil)
	request.Header.Set("Accept", "application/problem+json")

	response := r.Record(request)

	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	if response.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("expected Allow header %q, got %q", "GET, HEAD", response.Header.Get("Allow"))
	}
}

// TestRouterUnmatchedRunsMiddleware checks that middleware still runs for
// unmatched paths and unsupported methods before the router renders its
// problem.
func TestRouterUnmatchedRunsMiddleware(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Use(func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			writer.Header().Set("X-Middleware", "ran")

			return next(writer, request)
		}
	})
	r.Post("/items", func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/missing", nil),
		httptest.NewRequest(http.MethodOptions, "/items", nil),
	} {
		response := r.Record(request)

		if response.Header.Get("X-Middleware") != "ran" {
			t.Fatalf("%s %s: middleware did not run (status %d)", request.Method, request.URL.Path, response.StatusCode)
		}
	}
}

// TestRouterCatchAllAfterParentRoute checks that registering a later catch-all
// route does not replace an existing more-specific parent route.
func TestRouterCatchAllAfterParentRoute(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/users/{id}", func(writer http.ResponseWriter, _ *http.Request) error {
		_, err := writer.Write([]byte("user"))

		return err
	})
	r.Get("/users/{id}/{rest...}", func(writer http.ResponseWriter, _ *http.Request) error {
		_, err := writer.Write([]byte("files"))

		return err
	})

	recorder := httptest.NewRecorder()

	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/users/1", nil))

	if recorder.Body.String() != "user" {
		t.Fatalf("body = %q, want user", recorder.Body.String())
	}
}

// TestRouterWrapsMiddlewareOncePerRoute checks that registering one handler
// for several methods constructs its middleware chain only once.
func TestRouterWrapsMiddlewareOncePerRoute(t *testing.T) {
	t.Parallel()

	wraps := 0
	r := router.New()
	r.Use(func(next router.Handler) router.Handler {
		wraps++

		return next
	})
	r.Any("/items", func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	if wraps != 1 {
		t.Fatalf("wraps = %d, want 1", wraps)
	}
}

// TestRouterUnmatchedSharesOneMiddlewareChain verifies unmatched paths and
// methods share middleware state without rebuilding it on every request.
func TestRouterUnmatchedSharesOneMiddlewareChain(t *testing.T) {
	t.Parallel()

	builds := 0
	routes := router.New()
	routes.Use(func(next router.Handler) router.Handler {
		builds++
		requests := 0

		return func(writer http.ResponseWriter, request *http.Request) error {
			requests++
			writer.Header().Set("X-Requests", fmt.Sprint(requests))

			return next(writer, request)
		}
	})
	routes.Get("/items", func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	missing := serve(routes, http.MethodGet, "/missing")
	method := serve(routes, http.MethodPost, "/items")

	if builds != 2 || missing.Header().Get("X-Requests") != "1" || method.Header().Get("X-Requests") != "2" {
		t.Fatalf("constructors = %d, missing = %q, method = %q", builds,
			missing.Header().Get("X-Requests"), method.Header().Get("X-Requests"))
	}

	if missing.Code != http.StatusNotFound || method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("statuses = %d, %d", missing.Code, method.Code)
	}

	if method.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("Allow = %q", method.Header().Get("Allow"))
	}
}

// TestRouterConcurrentUnmatchedBuildsOneChain verifies the lazy fallback is
// published once when a router's first requests arrive concurrently.
func TestRouterConcurrentUnmatchedBuildsOneChain(t *testing.T) {
	t.Parallel()

	var builds atomic.Int32
	routes := router.New()
	routes.Use(func(next router.Handler) router.Handler {
		builds.Add(1)

		return next
	})
	var workers sync.WaitGroup

	for range 8 {
		workers.Go(func() {
			response := httptest.NewRecorder()

			routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

			if response.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", response.Code)
			}
		})
	}

	workers.Wait()

	if builds.Load() != 1 {
		t.Fatalf("constructors = %d, want 1", builds.Load())
	}
}

// noop is a middleware that passes through, used to call [router.Router.Use].
func noop(next router.Handler) router.Handler {
	return next
}

// ok is a handler used to register routes.
func ok(http.ResponseWriter, *http.Request) error {
	return nil
}

// mustPanic fails the test when fn does not panic.
func mustPanic(t *testing.T, fn func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	fn()
}

// TestUseAfterRouteRegistrationPanics verifies that Use after Get panics.
func TestUseAfterRouteRegistrationPanics(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/a", ok)

	mustPanic(t, func() {
		r.Use(noop)
	})
}

// TestUseOnParentAfterChildRegistrationPanics verifies that a route registered
// in a group seals the parent router.
func TestUseOnParentAfterChildRegistrationPanics(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Group("/api", func(g *router.Router) {
		g.Get("/a", ok)
	})

	mustPanic(t, func() {
		r.Use(noop)
	})
}

// TestUseOnChildWithoutRoutesIsAllowed verifies that a child that registered
// nothing may still use middleware.
func TestUseOnChildWithoutRoutesIsAllowed(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Use(noop)
	r.Group("/api", func(g *router.Router) {
		g.Use(noop)
		g.Get("/a", ok)
	})
	r.With(noop).Use(noop)
}
