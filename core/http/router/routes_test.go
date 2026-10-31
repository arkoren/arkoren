package router_test

import (
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
)

// TestRoutesSnapshotOperations checks native collection operations on a
// detached registration-ordered inventory, including an initially nil result.
func TestRoutesSnapshotOperations(t *testing.T) {
	t.Parallel()

	routes := router.New()

	if routes.Routes() != nil {
		t.Fatal("empty inventory must remain nil")
	}

	routes.Get("/first", listUsers)
	routes.Post("/second", listUsers)

	snapshot := routes.Routes()
	routes.Get("/later", listUsers)

	gets := snapshot.Filter(func(_ int, route router.Route) bool {
		return route.Method == http.MethodGet
	})

	if gets.Len() != 1 || gets[0].Pattern != "/first" {
		t.Fatalf("GET snapshot = %+v", gets)
	}

	if snapshot.Len() != 2 || snapshot[1].Pattern != "/second" {
		t.Fatalf("registration order = %+v", snapshot)
	}

	gets[0].Pattern = "/changed"

	if routes.Routes()[0].Pattern != "/first" {
		t.Fatal("filtered snapshot changed registration")
	}
}

// listUsers is a named successful handler used to verify that route listings
// retain the application function identity.
func listUsers(http.ResponseWriter, *http.Request) error {
	return nil
}

// showUser is a second named successful handler used to distinguish route
// identities without adding response-writing behavior.
func showUser(http.ResponseWriter, *http.Request) error {
	return nil
}

// plainHandler is a standard no-op HTTP function used to test identity
// discovery through the router HTTP adapter.
func plainHandler(http.ResponseWriter, *http.Request) {
}

// fileServer is a no-op standard HTTP handler used to distinguish concrete-
// type identity from function identity in route listings.
type fileServer struct{}

// ServeHTTP satisfies http.Handler without writing a response, allowing route
// tests to inspect the concrete handler type.
func (*fileServer) ServeHTTP(http.ResponseWriter, *http.Request) {
}

// controller supplies a method-value route handler so identity tests cover
// methods as well as standalone functions.
type controller struct{}

// Show is a successful method-value handler used to test route identity
// reporting separately from top-level functions.
func (controller) Show(http.ResponseWriter, *http.Request) error {
	return nil
}

// testPackage is the qualified test-package prefix expected in discovered
// handler names, keeping identity assertions independent of local aliases.
const testPackage = "arkoren.dev/core/http/router_test."

// tag returns middleware that appends label to X-Tag before calling the next
// handler, making group ordering visible in responses.
func tag(label string) router.Middleware {
	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Add("X-Tag", label)

			return next(w, r)
		}
	}
}

// TestRoutesGroupsApplyPrefixInOrder lists routes in registration order with
// group prefixes applied and the handler's qualified name and source.
func TestRoutesGroupsApplyPrefixInOrder(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/", listUsers)
	r.Group("/users", func(users *router.Router) {
		users.Get("/", listUsers)
		users.Group("/{id}", func(user *router.Router) {
			user.Delete("/", showUser)
		})
	})

	routes := r.Routes()
	want := []struct {
		// method, pattern, and handler describe one expected route in registration
		// order.
		method, pattern, handler string
	}{
		{"GET", "/", testPackage + "listUsers"},
		{"GET", "/users", testPackage + "listUsers"},
		{"DELETE", "/users/{id}", testPackage + "showUser"},
	}

	if len(routes) != len(want) {
		t.Fatalf("got %d routes, want %d: %+v", len(routes), len(want), routes)
	}

	for i, w := range want {
		got := routes[i]

		if got.Method != w.method || got.Pattern != w.pattern || got.Handler != w.handler {
			t.Fatalf("route %d = %+v, want %+v", i, got, w)
		}

		if !strings.HasPrefix(got.Source, "router/routes_test.go:") {
			t.Fatalf("route %d source = %q", i, got.Source)
		}
	}
}

// TestRoutesFromSubrouterAnswersForTree returns the whole tree from any router.
func TestRoutesFromSubrouterAnswersForTree(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/a", listUsers)
	r.Group("/g", func(g *router.Router) {
		g.Get("/b", showUser)

		if len(g.Routes()) != 2 {
			t.Fatalf("sub-router sees %d routes, want 2", len(g.Routes()))
		}
	})
}

// TestRoutesWithMiddlewareReportsHandler keeps the user's handler, not the
// middleware wrapper.
func TestRoutesWithMiddlewareReportsHandler(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Use(tag("root"))
	r.With(tag("with")).Get("/x", showUser)
	r.Grouped(func(g *router.Router) {
		g.Use(tag("grouped"))
		g.Get("/y", listUsers)
	})

	routes := r.Routes()

	if len(routes) != 2 || routes[0].Handler != testPackage+"showUser" || routes[1].Handler != testPackage+"listUsers" {
		t.Fatalf("routes = %+v", routes)
	}
}

// TestRoutesAnyListsOneEntryPerMethod hides the internal trailing-slash
// registrations.
func TestRoutesAnyListsOneEntryPerMethod(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Any("/things/{id}", showUser)
	r.Get("/files/{path...}", listUsers)

	routes := r.Routes()

	if len(routes) != 9 {
		t.Fatalf("got %d routes, want 8 for Any plus 1: %+v", len(routes), routes)
	}

	if routes[0].Method != "GET" || routes[1].Method != "HEAD" || routes[7].Method != "QUERY" {
		t.Fatalf("methods out of order: %+v", routes[:8])
	}

	if routes[8].Pattern != "/files/{path...}" {
		t.Fatalf("catch-all pattern = %q", routes[8].Pattern)
	}
}

// TestRoutesHTTPFuncReportsFunction names the adapted http.HandlerFunc.
func TestRoutesHTTPFuncReportsFunction(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/plain", router.HTTP(http.HandlerFunc(plainHandler)))

	route := r.Routes()[0]

	if route.Handler != testPackage+"plainHandler" || !strings.HasPrefix(route.Source, "router/routes_test.go:") {
		t.Fatalf("route = %+v", route)
	}
}

// TestRoutesHTTPTypeReportsTypeName names the dynamic type of other handlers.
func TestRoutesHTTPTypeReportsTypeName(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/files", router.HTTP(&fileServer{}))

	route := r.Routes()[0]

	if route.Handler != "*router_test.fileServer" {
		t.Fatalf("handler = %q", route.Handler)
	}

	if !strings.HasPrefix(route.Source, "router/routes_test.go:") {
		t.Fatalf("source = %q", route.Source)
	}
}

// TestRoutesClosureAndMethodValue report compiler names without suffixes.
func TestRoutesClosureAndMethodValue(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/closure", func(http.ResponseWriter, *http.Request) error {
		return nil
	})
	r.Get("/method", controller{}.Show)

	routes := r.Routes()

	if !strings.HasPrefix(routes[0].Handler, testPackage+"TestRoutesClosureAndMethodValue.func") {
		t.Fatalf("closure handler = %q", routes[0].Handler)
	}

	if routes[1].Handler != testPackage+"controller.Show" {
		t.Fatalf("method handler = %q", routes[1].Handler)
	}
}

// TestRoutesReturnsCopy protects the router's own list.
func TestRoutesReturnsCopy(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/a", listUsers)

	r.Routes()[0].Pattern = "/changed"

	if r.Routes()[0].Pattern != "/a" {
		t.Fatal("Routes exposed internal state")
	}
}
