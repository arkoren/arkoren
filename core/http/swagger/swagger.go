package swagger

import (
	"embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// assets holds the unmodified upstream distribution and its legal notices.
// Only explicitly named resources below are exposed over HTTP.
//
//go:embed assets/*
var assets embed.FS

// basePattern limits public paths to unreserved ASCII segments. Escaped
// separators, router wildcards, queries, and browser URL ambiguities are excluded.
var basePattern = regexp.MustCompile(`^/([A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*)?$`)

// resource is a completed immutable HTTP representation. Strings prevent
// request handling from modifying shared response bytes.
type resource struct {
	// body contains the exact bytes sent for GET, never for HEAD.
	body string
	// contentType is explicit so browsers do not need MIME sniffing.
	contentType string
	// cacheControl distinguishes configuration from pinned static assets.
	cacheControl string
}

// Handler constructs an immutable Swagger UI endpoint without registering routes
// or fetching the specification. basePath is an absolute, clean URL path without
// a trailing slash (except "/"); its segments use unreserved ASCII characters.
// specificationURL must be nonempty: HTTP(S), root-relative, and relative URLs
// are accepted, but credentials, fragments, scheme-relative URLs, whitespace,
// and backslashes are rejected. Relative URLs resolve against basePath + "/",
// consistently for both page paths; request host and forwarding headers are ignored.
// Invalid configuration returns an error before a handler is published.
//
// The page is served at basePath and basePath + "/". Only GET and HEAD are
// accepted. Static assets are pinned locally; the initializer disables the remote
// validator and query-string configuration. Application middleware still owns
// access control and CSP. Allow same-origin scripts, inline styles used by Swagger
// UI, and connections to the specification/API origins. No inline script is needed.
// Callbacks run synchronously once in order, including for invalid inputs; nil
// callbacks panic. The completed handler supports concurrent requests.
//
// Example:
//
//	endpoint, err := swagger.Handler("/docs", "/openapi.json")
//	if err != nil {
//		log.Fatal(err)
//	}
//	routes := router.New()
//	routes.Get("/docs", endpoint, swagger.Operation)
//	routes.Get("/docs/{path...}", endpoint, swagger.AssetsOperation)
func Handler(basePath, specificationURL string, configure ...BuilderFunc) (router.Handler, error) {
	page := &Builder{title: "API documentation"}

	for _, configurePage := range configure {
		configurePage(page)
	}

	if !basePattern.MatchString(basePath) || path.Clean(basePath) != basePath {
		return nil, fmt.Errorf("swagger: basePath must be a clean absolute path with unreserved segments")
	}

	prefix := strings.TrimSuffix(basePath, "/")

	specification, err := resolveSpecification(prefix, specificationURL)

	if err != nil {
		return nil, err
	}

	resources, err := representations(prefix, specification, page.title)

	if err != nil {
		return nil, err
	}

	return func(writer http.ResponseWriter, request *http.Request) error {
		return serve(resources, writer, request)
	}, nil
}

// Operation describes the documentation page that [Handler] serves at its base
// path. It documents the uncached HTML page and its security headers, and leaves
// the embedded assets to [AssetsOperation]. The annotation changes no handler
// behavior and registers no route; the application owns the path, middleware
// and access control.
//
// Example:
//
//	routes.Get("/docs", endpoint, swagger.Operation)
func Operation(operation *router.OperationBuilder) {
	operation.Summary("API documentation")
	operation.Description("Serves the Swagger UI page for this application's OpenAPI document. " +
		"The page is also served at the base path with a trailing slash. HEAD answers headers only.")
	operation.MediaResponse(http.StatusOK, "text/html; charset=utf-8", router.RawSchema(`{"type":"string"}`))
	operation.ResponseHeader(http.StatusOK, "Cache-Control", router.RawSchema(`{"type":"string","const":"no-store"}`))
	operation.ResponseHeader(http.StatusOK, "X-Content-Type-Options", router.RawSchema(`{"type":"string","const":"nosniff"}`))
}

// AssetsOperation is the metadata of the wildcard route that serves the pinned
// Swagger UI assets and its initializer beneath the base path. The wildcard is
// an opaque catch-all that the router's OpenAPI export cannot describe as one
// path, so it hides the registration instead of documenting it: the route stays
// callable and listed by Routes, and export neither reports nor includes it.
//
// Example:
//
//	routes.Get("/docs/{path...}", endpoint, swagger.AssetsOperation)
func AssetsOperation(operation *router.OperationBuilder) {
	operation.Hidden(true)
}

// resolveSpecification validates browser-facing URL input and resolves relative
// references using only the explicitly supplied documentation prefix.
func resolveSpecification(prefix, specificationURL string) (string, error) {
	unsafe := strings.IndexFunc(specificationURL, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) >= 0

	if specificationURL == "" || unsafe || strings.Contains(specificationURL, `\`) {
		return "", fmt.Errorf("swagger: specificationURL must be nonempty without whitespace or backslashes")
	}

	reference, err := url.Parse(specificationURL)

	if err != nil {
		return "", fmt.Errorf("swagger: specificationURL: %w", err)
	}

	if reference.User != nil || reference.Fragment != "" || strings.Contains(specificationURL, "#") {
		return "", fmt.Errorf("swagger: specificationURL must not contain credentials or a fragment")
	}

	if err := validateOrigin(reference); err != nil {
		return "", err
	}

	base := &url.URL{Path: prefix + "/"}

	return base.ResolveReference(reference).String(), nil
}

// validateOrigin limits absolute URLs to HTTP(S), rejecting network-path and
// opaque references that browsers could interpret differently from net/url.
func validateOrigin(reference *url.URL) error {
	if reference.Scheme == "" {
		if reference.Host != "" || strings.HasPrefix(reference.Path, "//") {
			return fmt.Errorf("swagger: specificationURL must not be scheme-relative")
		}

		return nil
	}

	if reference.Scheme != "http" && reference.Scheme != "https" {
		return fmt.Errorf("swagger: specificationURL scheme must be http or https")
	}

	if reference.Hostname() == "" || reference.Opaque != "" {
		return fmt.Errorf("swagger: specificationURL requires an HTTP(S) host")
	}

	return nil
}

// representations renders configuration once, and loads only the two vendored
// distribution assets. All returned entries are read-only after publication.
func representations(prefix, specificationURL, title string) (map[string]resource, error) {
	encoded, err := json.Marshal(specificationURL, jsontext.EscapeForHTML(true))

	if err != nil {
		return nil, fmt.Errorf("swagger: encode specificationURL: %w", err)
	}

	page := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` +
		html.EscapeString(title) + `</title><link rel="stylesheet" href="` + prefix +
		`/swagger-ui.css"></head><body><div id="swagger-ui"></div><script src="` + prefix +
		`/swagger-ui-bundle.js"></script><script src="` + prefix + `/initializer.js"></script></body></html>`
	document := resource{body: page, contentType: "text/html; charset=utf-8", cacheControl: "no-store"}
	resources := map[string]resource{
		prefix:       document,
		prefix + "/": document,
		prefix + "/initializer.js": {
			body: "window.ui = SwaggerUIBundle({url:" + string(encoded) +
				",dom_id:'#swagger-ui',validatorUrl:null,queryConfigEnabled:false});\n",
			contentType: "text/javascript; charset=utf-8", cacheControl: "no-store",
		},
	}

	for _, name := range []string{"swagger-ui.css", "swagger-ui-bundle.js"} {
		body, err := assets.ReadFile("assets/" + name)

		if err != nil {
			return nil, fmt.Errorf("swagger: embedded asset: %w", err)
		}

		contentType := "text/javascript; charset=utf-8"

		if strings.HasSuffix(name, ".css") {
			contentType = "text/css; charset=utf-8"
		}

		resources[prefix+"/"+name] = resource{
			body: string(body), contentType: contentType, cacheControl: "public, max-age=3600",
		}
	}

	return resources, nil
}

// ErrMethodNotAllowed is the 405 problem the endpoint returns for a method other
// than GET or HEAD, with an Allow header. Router registration with Get never
// reaches it; it protects an endpoint registered with Any or mounted directly.
// Paths the endpoint does not serve are the standard 404 problem. Both are
// rendered by the router like any other problem, so documentation routes never
// answer with an empty or plain-text error body.
//
// Example:
//
//	endpoint, _ := swagger.Handler("/docs", "/openapi.json")
//	err := endpoint(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/docs", nil))
//	fmt.Println(errors.Is(err, swagger.ErrMethodNotAllowed)) // true
var ErrMethodNotAllowed = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:core:http/swagger:method-not-allowed")
	details.Title("Method not allowed")
	details.Detail("The documentation endpoint serves GET and HEAD only.")
	details.Status(http.StatusMethodNotAllowed)
	details.Header("Allow", "GET, HEAD")
})

// serve uses exact paths, not a filesystem handler, so directories, traversal,
// and unspecified files can never expose embedded package contents.
func serve(resources map[string]resource, writer http.ResponseWriter, request *http.Request) error {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return ErrMethodNotAllowed
	}

	entry, ok := resources[request.URL.Path]

	if !ok {
		return problem.NotFound()
	}

	writer.Header().Set("Content-Type", entry.contentType)
	writer.Header().Set("Cache-Control", entry.cacheControl)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Length", strconv.Itoa(len(entry.body)))

	if request.Method == http.MethodHead {
		writer.WriteHeader(http.StatusOK)

		return nil
	}

	_, err := io.WriteString(writer, entry.body)

	return err
}
