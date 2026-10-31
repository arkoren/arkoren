package router

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"sync"

	"arkoren.dev/core/http/router/internal/openapi"
	"arkoren.dev/core/http/router/internal/snapshot"
)

// OpenAPIBuilderFunc configures one document synchronously in order. Nil
// entries panic; an expanded nil slice leaves the defaults unchanged.
//
// Example:
//
//	var configure router.OpenAPIBuilderFunc = func(document *router.OpenAPIBuilder) {
//		document.Description("Public API")
//	}
type OpenAPIBuilderFunc = func(*OpenAPIBuilder)

// OpenAPIBuilder configures document-level metadata, not route behavior.
// Owned settings detach after callbacks; schemes are explicit and never
// inferred from authentication middleware.
//
// Example:
//
//	data, err := routes.OpenAPI("Users", "1", func(document *router.OpenAPIBuilder) {
//		document.SecurityScheme("bearer", router.RawSchema(`{"type":"http","scheme":"bearer"}`))
//	})
//	if err != nil {
//		return err
//	}
//	_ = data
type OpenAPIBuilder struct {
	// description is the optional Info description.
	description string
	// schemes contains captured security scheme JSON objects.
	schemes map[string]any
	// err preserves the first configuration error.
	err error
}

// Description replaces the document's Info description. It accepts CommonMark
// without interpreting it, and does not alter any operation description.
//
// Example:
//
//	document.Description("API for the public user directory.")
func (document *OpenAPIBuilder) Description(text string) *OpenAPIBuilder {
	document.description = text

	return document
}

// SecurityScheme declares a named HTTP, API-key, or mutual-TLS scheme as a
// JSON object. This bounded API rejects other scheme kinds rather than
// partially validating OAuth flows. No middleware is installed or inferred.
//
// Example:
//
//	document.SecurityScheme("bearer", router.RawSchema(`{"type":"http","scheme":"bearer"}`))
func (document *OpenAPIBuilder) SecurityScheme(name string, definition RawSchema) *OpenAPIBuilder {
	var scheme map[string]any

	err := json.Unmarshal(definition, &scheme)

	if err == nil {
		err = openapi.ValidateSecurityScheme(name, scheme)
	}

	if document.err == nil && err != nil {
		document.err = fmt.Errorf("security scheme %q: %w", name, err)
	}

	document.schemes[name] = scheme

	return document
}

// newOpenAPI captures callbacks at construction, not at handler request time.
func newOpenAPI(configure []OpenAPIBuilderFunc) OpenAPIBuilder {
	document := &OpenAPIBuilder{schemes: map[string]any{}}

	for _, configure := range configure {
		configure(document)
	}

	published := *document
	published.schemes = snapshot.Clone(document.schemes).(map[string]any)

	return published
}

// OpenAPI exports only annotated, nonhidden routes as deterministic OpenAPI
// 3.2.1 JSON. It reads the entire router tree, using final group paths and
// synthesizing required string parameters for otherwise undescribed wildcards.
// Catch-alls and mounts are opaque and must be omitted or hidden. Declaration
// errors, duplicate IDs and equivalent path templates return contextual errors.
// Schemas remain inline and references are neither rewritten nor fetched.
// Registration must finish before exporting concurrently.
//
// Example:
//
//	data, err := routes.OpenAPI("Users", "1.0")
//	if err != nil {
//		return err
//	}
//	_ = data
func (router *Router) OpenAPI(title, version string, configure ...OpenAPIBuilderFunc) ([]byte, error) {
	return router.openAPI(title, version, newOpenAPI(configure))
}

// OpenAPIHandler returns an ordinary error-returning handler. Configuration
// callbacks run once now; document JSON or its error is cached once on the
// first request with sync.OnceValues. Routes added before that request are
// included. Later changes do not invalidate the cache. No route is registered,
// compiled, or frozen by construction; finish registration before serving.
//
// Example:
//
//	routes.Get("/openapi.json", routes.OpenAPIHandler("Users", "1.0"))
func (router *Router) OpenAPIHandler(title, version string, configure ...OpenAPIBuilderFunc) Handler {
	document := newOpenAPI(configure)
	load := sync.OnceValues(func() ([]byte, error) {
		return router.openAPI(title, version, document)
	})

	return func(writer http.ResponseWriter, _ *http.Request) error {
		data, err := load()

		if err != nil {
			return err
		}

		writer.Header().Set("Content-Type", "application/json")

		_, err = writer.Write(data)

		return err
	}
}

// openAPI renders a completed configuration against the current route tree.
func (router *Router) openAPI(title, version string, document OpenAPIBuilder) ([]byte, error) {
	if document.err != nil {
		return nil, fmt.Errorf("openapi: %w", document.err)
	}

	if title == "" || version == "" {
		return nil, fmt.Errorf("openapi: title and version must not be empty")
	}

	return openapi.Export(snapshot.Document{
		Title: title, Version: version, Description: document.description, Schemes: document.schemes,
	}, router.openAPIRoutes())
}

// openAPIRoutes projects only documentation metadata, never executable handlers
// or the public diagnostic wire records. Published metadata remains read-only;
// the exporter detaches its working copies before inferring any declarations.
func (router *Router) openAPIRoutes() []snapshot.Route {
	records := router.root().routes
	routes := make([]snapshot.Route, 0, len(records))

	for _, record := range records {
		route := snapshot.Route{Method: record.Method, Pattern: record.Pattern}

		if record.operation != nil {
			route.Operation = &snapshot.Operation{
				Value: record.operation.value, Hidden: record.operation.hidden, Err: record.operation.err,
			}
		}

		routes = append(routes, route)
	}

	return routes
}
