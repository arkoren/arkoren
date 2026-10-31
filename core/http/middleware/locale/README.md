# Locale

`arkoren.dev/core/http/middleware/locale` negotiates the request language and passes it, with a message catalog, to `arkoren.dev/foundation/validate` so validation errors render in the client's language.

Given your `spanish` and `brazilian` message sets (`validate.Messages`), add
language negotiation to an existing `r` router:

```go
catalog := validate.Catalog{
	Default: "en",
	Locales: map[string]validate.Messages{
		"en":    validate.English(),
		"es":    spanish,
		"pt-BR": brazilian,
	},
}

r.Use(locale.Middleware(func(language *locale.Builder) {
	language.Catalog(catalog)
	language.QueryParam("lang")
	language.Cookie("lang")
}))
```

The locale is the first match of the query parameter, the cookie, then the `Accept-Language` header ordered by q-value. A tag matches a catalog locale exactly (`pt-BR`), then by base language (`es-MX` matches `es`); matching ignores case. Otherwise the catalog default applies, or `en` when it names none. Supported locales are the keys of `Catalog.Locales`.

Catalog tags must have ASCII language-tag shape: one to eight letters followed
by optional alphanumeric subtags of one to eight characters. Matching also trims
whitespace and accepts underscores in place of hyphens. Duplicate normalized keys
such as `pt-BR` and `pt_BR`, invalid tags, or an invalid nonempty cookie name panic
after all callbacks. Later callbacks may replace intermediate invalid settings.
An explicitly empty catalog remains valid and falls back to `en`.

The middleware sets `Content-Language` and adds `Accept-Language` to `Vary` (plus `Cookie` when a cookie is configured). The headers are also added to the problem response context, so error responses carry them. Read the result with `locale.FromContext(ctx)`.
The context keeps the catalog key used for translations; the header trims that
key and replaces catalog-only underscores with HTTP language-tag hyphens.

HTTP q-values are in `[0, 1]` with no more than three decimal digits; digits after
`1` must be zero. Zero preferences, nonfinite or malformed values, and duplicate
q parameters are ignored. Tag checks cover the basic ASCII shape, not the full
BCP 47 grammar or a language-registry lookup.

Without `Builder.QueryParam` and `Builder.Cookie` those sources are off.
`Middleware()` starts with the built-in English catalog.
The cookie setting only names a cookie to read. This middleware does not write
cookies and has no cookie duration or lifetime setting.

The [validation overview](../../../../cmd/arkoren/skills/arkoren/references/guides/validation/overview.md)
introduces the schemas that use these messages.
