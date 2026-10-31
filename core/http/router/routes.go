package router

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"arkoren.dev/foundation/collection"
)

// Route describes one route registered on a [Router] tree, as the application
// wrote it. Inspection tools such as route listings and debugging endpoints
// read it through [Router.Routes]. It carries no request data or executable
// handler state, but names and source locations can disclose application layout.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/users/{id}", router.HTTP(http.NotFoundHandler()))
//	for _, route := range routes.Routes() {
//		fmt.Println(route.Method, route.Pattern)
//	}
type Route struct {
	// Method is the HTTP method, such as "GET".
	// Any registrations yield one Route per explicit method, rather than an
	// empty method standing for all methods.
	//
	// Example:
	//
	//	route := router.Route{Method: http.MethodGet}
	//	fmt.Println(route.Method) // GET
	Method string

	// Pattern is the route pattern with every group prefix applied, such as
	// "/users/{id}". It is the pattern the application registered: the
	// trailing-slash and parent-path variants the router adds internally are
	// not reported.
	//
	// Example:
	//
	//	route := router.Route{Pattern: "/users/{id}"}
	//	fmt.Println(route.Pattern) // /users/{id}
	Pattern string

	// Handler is the fully qualified name of the application's handler
	// function, such as "shop/users.Show", taken before any middleware wraps
	// it. Closures carry the compiler's name (for example "shop/users.New.func1")
	// and method values lose their "-fm" suffix. For a handler adapted with
	// [HTTP], it names the adapted [http.Handler]: the function for an
	// [http.HandlerFunc], otherwise the dynamic type, such as "*pkg.Files".
	//
	// Example:
	//
	//	route := router.Route{Handler: "shop/users.Show"}
	//	fmt.Println(route.Handler) // shop/users.Show
	Handler string

	// Source is the "dir/file.go:line" where the handler function is
	// declared: the last directory and the file name only, because the module
	// root is not knowable at run time and full build paths leak the build
	// machine's layout. It is empty when the location is unknown.
	//
	// Example:
	//
	//	route := router.Route{Source: "users/handlers.go:42"}
	//	fmt.Println(route.Source) // users/handlers.go:42
	Source string
}

// Routes returns every route registered through this router or any router in
// its tree ([Router.Group], [Router.Grouped], [Router.Clone], [Router.With]),
// in registration order. It answers for the whole tree whichever router it is
// called on, and returns a copy the caller may modify. [Router.Any] yields one
// entry per method. The eager collection is a detached snapshot, not a live
// query. Its wrapper adds operations, not ownership or additional copying.
//
// Example:
//
//	r := router.New()
//	r.Group("/users", func(users *router.Router) {
//		users.Get("/{id}", show)
//	})
//	gets := r.Routes().Filter(func(_ int, route router.Route) bool {
//		return route.Method == http.MethodGet
//	})
//	for _, route := range gets {
//		fmt.Println(route.Method, route.Pattern, route.Handler)
//	}
func (router *Router) Routes() collection.Slice[Route] {
	var routes []Route

	for _, record := range router.root().routes {
		routes = append(routes, record.Route)
	}

	return collection.NewSlice(routes)
}

// adapterPC is the code pointer shared by every closure [HTTP] returns, used
// to recognize an adapted handler.
var adapterPC = reflect.ValueOf(HTTP(nil)).Pointer()

// probeKey is the request context key under which [describe] asks an [HTTP]
// closure for the handler it adapts.
type probeKey struct{}

// describe returns the handler's name and source location for a [Route].
// It never calls the handler: an [HTTP] adapter is asked for its inner
// handler through a request that carries a probeKey and is answered before
// the adapted handler runs.
func describe(handler Handler) (name, source string) {
	function := reflect.ValueOf(handler)

	if function.Pointer() != adapterPC {
		return funcInfo(function.Pointer())
	}

	var inner http.Handler
	probe := (&http.Request{}).WithContext(contextWithProbe(&inner))
	_ = handler(nil, probe) // the adapter answers the probe and returns nil

	if inner == nil {
		return funcInfo(function.Pointer())
	}

	if fn, ok := inner.(http.HandlerFunc); ok {
		return funcInfo(reflect.ValueOf(fn).Pointer())
	}

	name = fmt.Sprintf("%T", inner)
	method, ok := reflect.TypeOf(inner).MethodByName("ServeHTTP")

	if !ok {
		return name, ""
	}

	_, source = funcInfo(method.Func.Pointer())

	return name, source
}

// funcInfo resolves a code pointer to its function name and "dir/file.go:line".
// Compiler-generated wrappers have no real file, so their source is empty.
func funcInfo(pc uintptr) (name, source string) {
	function := runtime.FuncForPC(pc)

	if function == nil {
		return "", ""
	}

	name = strings.TrimSuffix(function.Name(), "-fm")
	file, line := function.FileLine(function.Entry())

	if file == "" || strings.HasPrefix(file, "<") {
		return name, ""
	}

	return name, filepath.Base(filepath.Dir(file)) + "/" + filepath.Base(file) + ":" + strconv.Itoa(line)
}

// contextWithProbe returns a context that makes an [HTTP] adapter store its
// adapted handler in sink instead of serving.
func contextWithProbe(sink *http.Handler) context.Context {
	return context.WithValue(context.Background(), probeKey{}, sink)
}
