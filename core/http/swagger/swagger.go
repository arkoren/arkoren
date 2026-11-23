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

// Viewer is an immutable Swagger UI page together with its pinned assets and
// rendered initializer. [New] builds it without fetching the specification;
// [Viewer.Register] attaches it to a router group. It is safe for concurrent
// use because every representation is rendered once at construction.
//
// A Viewer deliberately has no per-route handler methods, unlike packages that
// offer XHandler for custom paths. The page links its assets by fixed relative
// names ("./swagger-ui.css", "./swagger-ui-bundle.js", "./initializer.js"), so
// the four routes only work together under one directory; moving one of them
// would break the page.
//
// Example:
//
//	viewer, err := swagger.New("/openapi.json")
//	if err != nil {
//		log.Fatal(err)
//	}
//	routes := router.New()
//	routes.Group("/docs", viewer.Register)
type Viewer struct {
	// page is the HTML document served at the group root.
	page resource
	// initializer is the script that starts Swagger UI with the specification URL.
	initializer resource
	// stylesheet is the vendored Swagger UI CSS.
	stylesheet resource
	// bundle is the vendored Swagger UI JavaScript bundle.
	bundle resource
}

// New constructs an immutable Swagger UI viewer without registering routes or
// fetching the specification. specificationURL must be nonempty: HTTP(S),
// root-relative, and relative URLs are accepted, but credentials, fragments,
// scheme-relative URLs, whitespace, and backslashes are rejected. A relative
// URL is left as written for the browser to resolve against the page URL, so
// "openapi.json" under a group at "/docs" resolves to "/docs/openapi.json"; use
// a root-relative URL for a specification outside the group. Request host and
// forwarding headers are never consulted. Invalid configuration returns an
// error before a viewer is published.
//
// Only GET and HEAD are served, with pinned local assets; the initializer
// disables the remote validator and query-string configuration. Application
// middleware still owns access control and CSP. Allow same-origin scripts,
// inline styles used by Swagger UI, and connections to the specification and
// API origins. No inline script is needed. Callbacks run synchronously once in
// order, including for invalid inputs; nil callbacks panic.
//
// Example:
//
//	viewer, err := swagger.New("/openapi.json", func(page *swagger.Builder) {
//		page.Title("Billing API")
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//	routes := router.New()
//	routes.Group("/docs", viewer.Register)
func New(specificationURL string, configure ...BuilderFunc) (*Viewer, error) {
	page := &Builder{title: "API documentation"}

	for _, configurePage := range configure {
		configurePage(page)
	}

	if err := validateSpecification(specificationURL); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(specificationURL, jsontext.EscapeForHTML(true))

	if err != nil {
		return nil, fmt.Errorf("swagger: encode specificationURL: %w", err)
	}

	stylesheet, err := asset("swagger-ui.css", "text/css; charset=utf-8")

	if err != nil {
		return nil, err
	}

	bundle, err := asset("swagger-ui-bundle.js", "text/javascript; charset=utf-8")

	if err != nil {
		return nil, err
	}

	document := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` +
		html.EscapeString(page.title) + `</title><link rel="stylesheet" href="./swagger-ui.css"></head>` +
		`<body><div id="swagger-ui"></div><script src="./swagger-ui-bundle.js"></script>` +
		`<script src="./initializer.js"></script></body></html>`

	return &Viewer{
		page: resource{body: document, contentType: "text/html; charset=utf-8", cacheControl: "no-store"},
		initializer: resource{
			body: "window.ui = SwaggerUIBundle({url:" + string(encoded) +
				",dom_id:'#swagger-ui',validatorUrl:null,queryConfigEnabled:false});\n",
			contentType: "text/javascript; charset=utf-8", cacheControl: "no-store",
		},
		stylesheet: stylesheet,
		bundle:     bundle,
	}, nil
}

// Register adds the four exact GET routes of the viewer to r: "/" (the page),
// "/swagger-ui.css", "/swagger-ui-bundle.js" and "/initializer.js". It applies
// no middleware and no prefix of its own; attach it with a group, usually
// routes.Group("/docs", viewer.Register), after any access-control middleware.
// There is no catch-all, so every other path below the group is a router 404.
// Each route carries its OpenAPI operation (summary, media type, caching
// headers and, for the page, the redirect and 404 problem), so the router's
// export lists the documentation routes without further annotation.
//
// The page addresses its assets relative to its own URL, so it must be served
// with a trailing slash. A request for the group path without one ("/docs") is
// answered with a 308 redirect to the relative Location "docs/", built from
// the last path segment of the request only and never from the Host, any
// forwarding header or the query string, so it stays correct behind a reverse
// proxy that adds a prefix. A last segment with characters other than
// letters, digits and "._~-" answers 404 instead. Registered at the router root, the page is "/" and never redirects.
//
// Registering a second viewer on the same group panics, as any duplicate
// pattern does.
//
// Example:
//
//	routes := router.New()
//	routes.With(auth.Middleware(authenticator), auth.Require()).Group("/docs", viewer.Register)
func (viewer *Viewer) Register(r *router.Router) {
	r.Get("/", viewer.servePage, pageOperation)
	r.Get("/swagger-ui.css", viewer.stylesheet.serve, assetOperation("Swagger UI stylesheet", "text/css; charset=utf-8", "public, max-age=3600"))
	r.Get("/swagger-ui-bundle.js", viewer.bundle.serve, assetOperation("Swagger UI script", "text/javascript; charset=utf-8", "public, max-age=3600"))
	r.Get("/initializer.js", viewer.initializer.serve, assetOperation("Swagger UI initializer", "text/javascript; charset=utf-8", "no-store"))
}

// pageOperation describes the documentation page route of [Viewer.Register]:
// the uncached HTML page with its security headers, the 308 relative redirect
// that a request without the trailing slash receives, and the 404 problem for
// a last path segment the redirect refuses to echo. It changes no handler
// behavior.
func pageOperation(operation *router.OperationBuilder) {
	operation.Summary("API documentation")
	operation.Description("Serves the Swagger UI page for this application's OpenAPI document. The page links its assets " +
		"relative to its own URL, so it is served with a trailing slash; a request without one is redirected to the " +
		"relative Location. HEAD answers headers only.")
	operation.Tags("documentation")
	operation.MediaResponse(http.StatusOK, "text/html; charset=utf-8", router.RawSchema(`{"type":"string"}`))
	operation.ResponseHeader(http.StatusOK, "Cache-Control", router.RawSchema(`{"type":"string","const":"no-store"}`))
	operation.ResponseHeader(http.StatusOK, "X-Content-Type-Options", router.RawSchema(`{"type":"string","const":"nosniff"}`))
	operation.Response(http.StatusPermanentRedirect, "Redirected to the page with a trailing slash")
	operation.ResponseHeader(http.StatusPermanentRedirect, "Location", router.RawSchema(`{"type":"string","description":"The last path segment plus a slash, relative to the request."}`))
	operation.Problem(problem.NotFound())
}

// assetOperation describes one pinned static resource of [Viewer.Register]:
// its media type and the caching of its 200 answer. summary names the
// resource and cacheControl is the exact Cache-Control value it is served with.
func assetOperation(summary, media, cacheControl string) router.OperationBuilderFunc {
	return func(operation *router.OperationBuilder) {
		operation.Summary(summary)
		operation.Description("Serves a resource of the documentation page. HEAD answers headers only.")
		operation.Tags("documentation")
		operation.MediaResponse(http.StatusOK, media, router.RawSchema(`{"type":"string"}`))
		operation.ResponseHeader(http.StatusOK, "Cache-Control", router.RawSchema(`{"type":"string","const":"`+cacheControl+`"}`))
		operation.ResponseHeader(http.StatusOK, "X-Content-Type-Options", router.RawSchema(`{"type":"string","const":"nosniff"}`))
	}
}

// redirectSegment matches the only last path segments the trailing-slash
// redirect will echo back: RFC 3986 unreserved characters. Anything else, such
// as an encoded slash or backslash, a colon, or an encoded dot sequence, could
// be read by a browser as a different target, so it is never put in Location.
var redirectSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// servePage serves the document, first redirecting a request without a
// trailing slash so the page's relative asset URLs resolve inside the group.
//
// The redirect Location is the last request path segment plus "/", so it
// stays relative and keeps any proxy prefix. No query string is copied, since
// Swagger UI query-string configuration is disabled. A last segment that is
// not purely unreserved characters, or is a dot segment, answers 404 rather
// than reaching the Location header.
func (viewer *Viewer) servePage(writer http.ResponseWriter, request *http.Request) error {
	escaped := request.URL.EscapedPath()

	if strings.HasSuffix(escaped, "/") {
		return viewer.page.serve(writer, request)
	}

	segment := escaped[strings.LastIndex(escaped, "/")+1:]

	if !redirectSegment.MatchString(segment) || segment == "." || segment == ".." {
		return problem.NotFound()
	}

	// http.Redirect would rewrite the relative Location into an absolute path.
	writer.Header().Set("Location", segment+"/")
	writer.WriteHeader(http.StatusPermanentRedirect)

	return nil
}

// asset reads one vendored distribution file into a long-lived resource.
func asset(name, contentType string) (resource, error) {
	body, err := assets.ReadFile("assets/" + name)

	if err != nil {
		return resource{}, fmt.Errorf("swagger: embedded asset: %w", err)
	}

	return resource{body: string(body), contentType: contentType, cacheControl: "public, max-age=3600"}, nil
}

// validateSpecification rejects an empty URL, whitespace, control characters
// and backslashes, credentials and fragments, and anything that is neither
// HTTP(S) nor a path-style reference.
func validateSpecification(specificationURL string) error {
	unsafe := strings.IndexFunc(specificationURL, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) >= 0

	if specificationURL == "" || unsafe || strings.Contains(specificationURL, `\`) {
		return fmt.Errorf("swagger: specificationURL must be nonempty without whitespace or backslashes")
	}

	reference, err := url.Parse(specificationURL)

	if err != nil {
		return fmt.Errorf("swagger: specificationURL: %w", err)
	}

	if reference.User != nil || reference.Fragment != "" || strings.Contains(specificationURL, "#") {
		return fmt.Errorf("swagger: specificationURL must not contain credentials or a fragment")
	}

	return validateOrigin(reference)
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

// serve writes the resource for GET and HEAD. The router has already
// restricted the method and matched the exact path, so there is no filesystem
// lookup through which directories, traversal or unspecified embedded files
// could be exposed.
func (entry resource) serve(writer http.ResponseWriter, request *http.Request) error {
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
