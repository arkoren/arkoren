// Package router provides an HTTP router with error-returning handlers,
// middleware, and centrally rendered Problem Details errors.
//
// [New] creates a [Router]. [Handler] errors are rendered in one place, and
// standard [http.Handler] values can be adapted with [HTTP].
// Middleware chains are shared across requests: registered routes capture one
// chain per registration, and unmatched requests share a separate chain built
// on first use. [HTTPMiddleware] constructs native middleware at chain creation,
// not once per request; its handlers must support concurrent requests.
// Forwarded request contexts must retain the original context's values, which
// carry lifecycle hooks and middleware state; derive them from request.Context.
//
// [Router.Mount] serves a standard [http.Handler] under a prefix, which is
// stripped, inside the router's middleware. [Router.MCPTools] returns a live
// route listing for explicit MCP registration; callers own access policy.
//
// [Router.Routes] lists the registered routes with their handler functions,
// for route listings and inspection tools. It returns an eager collection
// snapshot in registration order, not a live query. Registration must not run
// concurrently with inspection; the returned slice is detached from the tree.
// Optional trailing [OperationBuilderFunc] callbacks describe operations for
// [Router.OpenAPI] without changing request execution. Export emits OpenAPI
// 3.2.1 JSON from annotated, nonhidden registrations only; schemas are rendered
// at declaration time, never evaluated or remotely fetched. Opaque catch-all
// routes and mounts must be omitted or hidden. [Router.OpenAPIHandler] caches
// export on its first request; applications own route setup and access policy.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/health", func(writer http.ResponseWriter, request *http.Request) error {
//		writer.WriteHeader(http.StatusNoContent)
//		return nil
//	}, func(operation *router.OperationBuilder) {
//		operation.ID("health").Response(http.StatusNoContent, "Healthy")
//	})
//	routes.Get("/openapi.json", routes.OpenAPIHandler("Example", "1"))
//
// With [RecoverBuilder.Events], [Recover] publishes [PanicRecovered]
// ("http.panic.recovered") with the converted error and the stack for every
// recovered panic. These are sensitive server-side diagnostics, not client-safe
// payloads: restrict listener output and sanitize it before forwarding. The
// event stack and each Stack method result are detached from the error's stack.
package router
