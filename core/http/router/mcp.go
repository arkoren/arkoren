package router

import (
	"context"
	"strings"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

// routeView exposes registration metadata, never request data or handler state.
type routeView struct {
	// Method is the HTTP method.
	Method string `json:"method"`

	// Pattern is the registered pattern with group prefixes applied.
	Pattern string `json:"pattern"`

	// Handler is the qualified name of the handler function.
	Handler string `json:"handler"`

	// Source is the "dir/file.go:line" of the handler; may be empty.
	Source string `json:"source,omitzero"`
}

// routesInput is the input of the routes operation.
type routesInput struct {
	// Method restricts results to one HTTP method; empty accepts every method.
	Method string `json:"method,omitzero"`
	// Path restricts results by registered-pattern prefix; empty accepts every
	// pattern.
	Path string `json:"path,omitzero"`
}

// routesView keeps the list response an object, including for an empty tree.
// Items follow registration order; the complete registry is returned.
type routesView struct {
	// Items contains matching registrations and is never null.
	Items []routeView `json:"items"`
	// Truncated is false because route listings return every match.
	Truncated bool `json:"truncated"`
}

// MCPTools returns a live, read-only route listing for explicit registration.
// A nonempty prefix produces prefix.routes and uses prefix as its CLI group;
// an empty prefix produces routes in the router CLI group. Invalid names are
// reported by each tool's Err method. Calls list the entire router tree with
// optional case-insensitive method and case-sensitive path-prefix filters.
//
// Handler identities and source paths can disclose application structure.
// The caller owns authentication and capability policy; nothing is registered
// implicitly. Routes added after construction appear on subsequent calls.
//
// Example:
//
//	tools := router.New().MCPTools("http")
//	if err := tools[0].Err(); err != nil {
//		return err
//	}
//	result, err := tools[0].Call(context.Background(), []byte(`{"path":"/api"}`))
//	if err != nil {
//		return err
//	}
//	fmt.Println(string(result))
func (router *Router) MCPTools(prefix string) []mcp.Tool {
	group, name := "router", "routes"

	if prefix != "" {
		group, name = prefix, prefix+".routes"
	}

	routes := mcp.NewTool(name, func(_ context.Context, input routesInput) (routesView, error) {
		items := []routeView{}

		for _, route := range router.Routes() {
			if input.Method != "" && !strings.EqualFold(input.Method, route.Method) {
				continue
			}

			if !strings.HasPrefix(route.Pattern, input.Path) {
				continue
			}

			items = append(items, routeView(route))
		}

		return routesView{
			Items: items,
		}, nil
	}, func(read *mcp.ToolBuilder[routesInput, routesView]) {
		read.Description("Registered HTTP routes with their handlers and source locations")
		read.CLI(group, "routes").GroupSummary("HTTP routes").Role("routes")
		read.ReadOnly(true).Idempotent(true).OpenWorld(false)
		read.Input(validate.Struct(func(object *validate.StructBuilder[routesInput]) {
			object.Field("Method", func(method *validate.StringBuilder[string]) {
				method.Describe("Only this HTTP method, such as GET")
			})
			object.Field("Path", func(path *validate.StringBuilder[string]) {
				path.Describe("Only patterns starting with this prefix")
			})
		}))
		read.Positional("path")
		read.Example("Everything under /api", `{"path":"/api"}`)
	})

	return []mcp.Tool{routes}
}
