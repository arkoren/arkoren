package router_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/mcp"
)

// ExampleRouter_MCPTools checks the documented native tool call without exposing
// source locations or requiring a network endpoint.
func ExampleRouter_MCPTools() {
	tools := router.New().MCPTools("http")

	if err := tools[0].Err(); err != nil {
		fmt.Println(err)

		return
	}

	result, err := tools[0].Call(context.Background(), []byte(`{"path":"/api"}`))

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(string(result))
	// Output: {"items":[],"truncated":false}
}

// routeView mirrors an item of the routes operation.
type routeView struct {
	// Method is the registered HTTP verb returned by route inspection.
	Method string `json:"method"`
	// Pattern is the registered route pattern, including group prefixes.
	Pattern string `json:"pattern"`
	// Handler is the original application handler identity reported by the router.
	Handler string `json:"handler"`
	// Source is the handler source location reported with that identity.
	Source string `json:"source"`
}

// routesResult mirrors the object-shaped listing returned by a routes tool.
type routesResult struct {
	// Items contains matching route registrations.
	Items []routeView `json:"items"`
	// Truncated reports whether matching items were omitted.
	Truncated bool `json:"truncated"`
}

// routesOf calls the tool directly and decodes its structured object result.
// Declaration or execution failures stop the calling test immediately.
func routesOf(t *testing.T, tool mcp.Tool, input string) routesResult {
	t.Helper()

	body, err := tool.Call(context.Background(), []byte(input))

	if err != nil {
		t.Fatal(err)
	}

	var result routesResult

	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	return result
}

// TestMCPListsRoutesAndFilters checks live route inspection and its
// independent method and path-prefix filters against one shared router.
func TestMCPListsRoutesAndFilters(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Get("/users/{id}", showUser)
	r.Post("/users", showUser)
	r.Get("/ping", showUser)

	tool := r.MCPTools("public")[0]

	all := routesOf(t, tool, `{}`)

	if len(all.Items) != 3 || all.Truncated {
		t.Fatalf("routes = %+v", all)
	}

	first := all.Items[0]

	if first.Method != "GET" || first.Pattern != "/users/{id}" || !strings.HasSuffix(first.Handler, "showUser") || !strings.Contains(first.Source, "routes_test.go:") {
		t.Fatalf("first = %+v", first)
	}

	byMethod := routesOf(t, tool, `{"method":"post"}`)

	if len(byMethod.Items) != 1 || byMethod.Items[0].Method != "POST" {
		t.Fatalf("method filter = %+v", byMethod)
	}

	byPath := routesOf(t, tool, `{"path":"/users"}`)

	if len(byPath.Items) != 2 {
		t.Fatalf("path filter = %+v", byPath)
	}
}

// TestMCPOfAGroupReportsTheWholeTree checks that inspection attached
// through a group still lists routes from the entire router tree.
func TestMCPOfAGroupReportsTheWholeTree(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Group("/api", func(api *router.Router) {
		api.Get("/a", showUser)
	})
	r.Get("/b", showUser)

	var inGroup mcp.Tool
	r.Group("/api", func(api *router.Router) {
		inGroup = api.MCPTools("public")[0]
	})

	if got := routesOf(t, inGroup, `{}`); len(got.Items) != 2 {
		t.Fatalf("routes = %+v", got)
	}
}

// TestMCPSeesRoutesRegisteredAfterConstruction checks that
// route inspection reads the current registry rather than a construction-time
// snapshot.
func TestMCPSeesRoutesRegisteredAfterConstruction(t *testing.T) {
	t.Parallel()

	r := router.New()
	tool := r.MCPTools("public")[0]
	r.Get("/late", showUser)

	if got := routesOf(t, tool, `{}`); len(got.Items) != 1 {
		t.Fatalf("routes = %+v", got)
	}
}

// TestMCPListsTwoRoutersUnderTheirOwnNames checks that separately named
// inspection extensions keep independent routers and their route listings
// isolated.
func TestMCPListsTwoRoutersUnderTheirOwnNames(t *testing.T) {
	t.Parallel()

	public, admin := router.New(), router.New()
	public.Get("/users", showUser)
	admin.Get("/jobs", showUser)

	got := routesOf(t, public.MCPTools("public")[0], `{}`)

	if len(got.Items) != 1 || got.Items[0].Pattern != "/users" {
		t.Fatalf("public: %+v", got)
	}

	got = routesOf(t, admin.MCPTools("admin")[0], `{}`)

	if len(got.Items) != 1 || got.Items[0].Pattern != "/jobs" {
		t.Fatalf("admin: %+v", got)
	}
}

// TestMCPListsMountedHandlers checks that mounted standard handlers
// appear in inspection with their registered catch-all pattern.
func TestMCPListsMountedHandlers(t *testing.T) {
	t.Parallel()

	r := router.New()
	r.Mount("/inspect", http.NotFoundHandler())

	got := routesOf(t, r.MCPTools("public")[0], `{"method":"GET"}`)

	if len(got.Items) != 1 || got.Items[0].Pattern != "/inspect/{path...}" {
		t.Fatalf("routes = %+v", got)
	}
}

// TestMCPRejectsInvalidName checks that invalid native tool names are reported
// by the tool declaration without requiring a transport or registration.
func TestMCPRejectsInvalidName(t *testing.T) {
	t.Parallel()

	err := router.New().MCPTools("Bad Name")[0].Err()

	if !errors.Is(err, mcp.ErrInvalidTool) {
		t.Fatalf("err = %v", err)
	}
}

// TestMCPAllowsCLICommandName checks that the router reserves no CLI words.
// Native tool names remain independent of local CLI command resolution.
func TestMCPAllowsCLICommandName(t *testing.T) {
	t.Parallel()

	err := router.New().MCPTools("help")[0].Err()

	if err != nil {
		t.Fatal(err)
	}
}

// TestMCPToolsNamesAndMetadata checks native names independently of CLI
// grouping, including the routes shortcut role and closed-world read hints.
func TestMCPToolsNamesAndMetadata(t *testing.T) {
	t.Parallel()

	info := router.New().MCPTools("http")[0].Describe()

	if info.Name != "http.routes" || info.CLI.Group != "http" || info.CLI.Role != "routes" {
		t.Fatalf("info = %+v", info)
	}

	if !info.Annotations.ReadOnly || info.Annotations.OpenWorld {
		t.Fatalf("annotations = %+v", info.Annotations)
	}

	info = router.New().MCPTools("")[0].Describe()

	if info.Name != "routes" || info.CLI.Group != "router" {
		t.Fatalf("defaults = %+v", info)
	}
}

// TestMCPToolsAvailabilityDenies checks consumer policy before input decoding.
// The router supplies no built-in application authorization rules.
func TestMCPToolsAvailabilityDenies(t *testing.T) {
	t.Parallel()

	tool := router.New().MCPTools("")[0].WithAvailability(func(context.Context) (bool, error) {
		return false, nil
	})

	_, err := tool.Call(context.Background(), []byte(`not json`))

	if !errors.Is(err, mcp.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// TestMCPEmptyRoutesReturnsArray checks the object envelope and non-null empty
// items required for schema-valid structured output.
func TestMCPEmptyRoutesReturnsArray(t *testing.T) {
	t.Parallel()

	result := routesOf(t, router.New().MCPTools("")[0], `{}`)

	if result.Items == nil || len(result.Items) != 0 || result.Truncated {
		t.Fatalf("result = %+v", result)
	}
}
