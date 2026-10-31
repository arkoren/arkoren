package router

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Mount serves handler for every standard method under prefix: the exact path
// and everything below it. The prefix is stripped from the request before
// handler sees it, so a handler written to serve "/" and "/{x}/{y}" can be
// mounted anywhere, as chi's Mount does. A request for exactly prefix, with or
// without a trailing slash, reaches the handler with the path "/".
//
// prefix is joined to the router's own group prefix, so a Mount inside
// [Router.Group] strips the whole pattern. prefix must contain literal segments:
// a wildcard such as "{id}" panics. A parent group may contain named wildcards;
// their values remain available through request.PathValue after stripping.
// Like [Router.Any] it covers every method except TRACE and CONNECT; a request
// with one of those gets the router's 405.
// Literal segments follow [http.ServeMux] pattern escaping. Stripping preserves
// escaped segment boundaries in URL.RawPath, including encoded slashes, and
// does not modify the original request's URL or query.
//
// Mounted handlers are ordinary routes: they run inside the router's
// middleware, in registration order, exactly as a handler registered with
// [Router.Get] would, so authentication, recovery, and telemetry middleware
// apply. The handler renders its own errors, because an [http.Handler] has no
// error to return; the wrapper only answers 200 when it wrote nothing, as
// [HTTP] does. Routes are registered through [Router.Methods], so [Router.Use]
// panics afterwards and a route that overlaps one already registered panics
// as it does for any pattern.
//
// [Router.Routes] lists the mount once per method with the pattern
// prefix+"/{path...}", the pattern that was registered, and names the
// handler's own function or type (for an [http.HandlerFunc] its function,
// otherwise the dynamic type such as "*http.ServeMux"), not the wrapper
// that strips the prefix.
//
// Metadata callbacks follow [Router.Methods] and run once for the public mount,
// not once per method. Mounts are opaque catch-alls, so omit metadata or set
// [OperationBuilder.Hidden] to exclude them; annotated visible mounts return
// a contextual [Router.OpenAPI] error rather than a fabricated single path.
//
// Example:
//
//	admin := router.New()
//	admin.Use(auth.Middleware(authenticator), auth.Require[User]())
//	admin.Mount("/tools", tools) // tools sees paths relative to /tools
func (router *Router) Mount(prefix string, handler http.Handler, configure ...OperationBuilderFunc) {
	if strings.ContainsAny(prefix, "{}") {
		panic("router: mount prefix must not contain wildcards")
	}

	// full is the whole prefix to strip; it is empty for a mount at the root.
	full := strings.TrimSuffix(path.Join("/", router.pattern, prefix), "/")
	strip := HTTP(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, withoutPrefix(request, full))
	}))

	// Describe the user's handler, not the stripping closure.
	name, source := describe(HTTP(handler))
	record := routeRecord{Route: Route{Handler: name, Source: source}, operation: newOperation(configure)}
	router.methods(allMethods, path.Join("/", prefix, "{path...}"), strip, record)
}

// withoutPrefix copies the request and URL, removing the matched prefix's
// segment count from the escaped path. ServeMux already matched the literal
// segments, whose encodings can differ from the registration. Working in the
// escaped form preserves slashes encoded inside a segment. An empty remainder
// becomes "/"; query, context, and other request data stay unchanged.
func withoutPrefix(request *http.Request, prefix string) *http.Request {
	escaped := strings.TrimPrefix(request.URL.EscapedPath(), "/")

	for range strings.Count(prefix, "/") {
		_, escaped, _ = strings.Cut(escaped, "/")
	}

	escaped = "/" + escaped

	stripped := new(http.Request)
	*stripped = *request
	stripped.URL = new(url.URL)
	*stripped.URL = *request.URL
	// EscapedPath returns valid escaping, and cutting at literal slashes cannot
	// split a percent escape, so PathUnescape cannot fail here.
	stripped.URL.Path, _ = url.PathUnescape(escaped)
	stripped.URL.RawPath = ""

	if request.URL.RawPath != "" {
		stripped.URL.RawPath = escaped
	}

	return stripped
}
