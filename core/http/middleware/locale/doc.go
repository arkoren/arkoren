// Package locale provides middleware that picks the response language from
// the request and hands it to [validate] to translate validation messages.
//
// The locale comes from, in order, an optional query parameter, an optional
// cookie, and the Accept-Language header with its q-values. Each candidate
// is matched against the locales of the catalog, first exactly and then by
// base language ("es" for "es-MX"). Without a match the catalog default is
// used.
//
// Catalog tags have ASCII language-tag shape. Case, surrounding whitespace, and
// underscores are normalized for matching; context keeps the original catalog
// key, while Content-Language trims whitespace and replaces underscores with
// hyphens. Invalid tags, ambiguous normalized keys, or invalid nonempty cookie
// names panic after all callbacks. An empty catalog falls back to English, and
// empty query/cookie names disable those selectors. The middleware only reads
// cookies; it does not issue cookies or configure their lifetime.
//
// HTTP quality values must be zero or one with at most three decimal digits;
// only zeros may follow one. Malformed, nonfinite, duplicate, or q=0 preferences
// are ignored. Tag checks cover the basic ASCII shape, not the full BCP 47
// grammar or a registry lookup.
//
// Example:
//
//	routes := router.New()
//	routes.Use(locale.Middleware(func(language *locale.Builder) {
//		language.QueryParam("lang")
//		language.Cookie("language")
//	}))
package locale
