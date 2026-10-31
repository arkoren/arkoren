package locale

import (
	"maps"
	"net/http"

	"arkoren.dev/core/http/middleware/locale/internal/negotiation"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/validate"
)

// Middleware returns middleware that resolves the request locale and stores
// it, with the catalog, in the request context for [validate].
//
// It sets Content-Language to the resolved locale and adds Accept-Language
// (and Cookie, when a cookie is configured) to Vary. The headers are also
// attached to the problem response context, so error responses carry them.
//
// Retrieve the locale downstream with [FromContext].
// Invalid catalog tags, ambiguous normalized keys, and invalid nonempty cookie
// names panic after all callbacks. The empty catalog and empty selector names
// retain their fallback/disabled meanings. Malformed HTTP quality values,
// including nonfinite numbers or duplicate q parameters, are ignored.
//
// Example:
//
//	routes.Use(locale.Middleware(func(language *locale.Builder) {
//		language.QueryParam("lang")
//		language.Cookie("language")
//	}))
func Middleware(configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	resolver := negotiation.New(config.Catalog.Default, maps.Keys(config.Catalog.Locales), config.QueryParam, config.Cookie)

	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			tag := resolver.Resolve(request)

			ctx := validate.NewCatalogContext(request.Context(), config.Catalog)
			ctx = validate.NewLocaleContext(ctx, tag)
			ctx = setHeaders(ctx, writer, tag, config.Cookie)

			return next(writer, request.WithContext(ctx))
		}
	}
}
