package locale

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"
)

const (
	// fallbackLocale is the default locale when the catalog names none.
	fallbackLocale = "en"

	// headerVary lists the request headers a response depends on.
	headerVary = "Vary"

	// headerAcceptLanguage carries the client language preferences.
	headerAcceptLanguage = "Accept-Language"

	// headerContentLanguage reports the language of the response.
	headerContentLanguage = "Content-Language"

	// headerCookie is added to Vary when a cookie selects the locale.
	headerCookie = "Cookie"
)

// settings configures the locale middleware.
type settings struct {
	// Catalog holds the supported locales, which are its Locales keys, and
	// the default locale. [Middleware] uses the built-in English messages.
	// An empty Default falls back to "en".
	Catalog validate.Catalog

	// QueryParam names a query parameter that selects the locale and wins
	// over every other source. Empty disables it.
	QueryParam string

	// Cookie names a cookie that selects the locale when the query
	// parameter is absent or unsupported. Empty disables it.
	Cookie string
}

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(locale.Middleware(func(language *locale.Builder) {
//		language.QueryParam("lang")
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// Catalog holds the supported locales, which are its Locales keys, and
// the default locale. [Middleware] uses the built-in English messages.
// An empty Default falls back to "en"; an empty catalog remains valid. Keys and
// a nonempty Default must have language-tag shape: one to eight ASCII letters,
// followed by optional one-to-eight-character alphanumeric subtags. Matching
// ignores case, surrounding whitespace, and the distinction between "_" and "-".
// Two keys with the same normalized spelling are ambiguous and panic at
// publication, as do invalid tags. Message maps remain detached from the caller.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	catalog := validate.Catalog{
//		Default: "en",
//		Locales: map[string]validate.Messages{"en": validate.English()},
//	}
//	r.Use(locale.Middleware(func(language *locale.Builder) {
//		language.Catalog(catalog)
//	}))
func (language *Builder) Catalog(catalog validate.Catalog) *Builder {
	language.settings.Catalog = cloneCatalog(catalog)

	return language
}

// QueryParam names a query parameter that selects the locale and wins
// over every other source. Empty disables it.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(locale.Middleware(func(language *locale.Builder) {
//		language.QueryParam("lang")
//	}))
func (language *Builder) QueryParam(name string) *Builder {
	language.settings.QueryParam = name

	return language
}

// Cookie names a cookie that selects the locale when the query
// parameter is absent or unsupported. Empty disables it. A nonempty name must
// pass net/http.Cookie.Valid; Middleware panics for an invalid final name after
// running every construction callback. This middleware never writes cookies
// and has no cookie lifetime or duration setting.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(locale.Middleware(func(language *locale.Builder) {
//		language.Cookie("language")
//	}))
func (language *Builder) Cookie(name string) *Builder {
	language.settings.Cookie = name

	return language
}

// FromContext returns the locale stored by [Middleware], or an empty string when
// the context has none. It preserves the catalog key for translation lookup;
// Content-Language instead trims whitespace and replaces underscores with "-".
//
// Example:
//
//	tag := locale.FromContext(request.Context())
//	fmt.Println(tag)
func FromContext(ctx context.Context) string {
	tag, _ := validate.LocaleFromContext(ctx)

	return tag
}

// setHeaders writes Content-Language and Vary to the response and to the
// problem response context, creating the context when absent.
func setHeaders(ctx context.Context, writer http.ResponseWriter, tag, cookie string) context.Context {
	tag = strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")
	header := writer.Header()
	header.Set(headerContentLanguage, tag)
	addVary(header, headerAcceptLanguage)

	if cookie != "" {
		addVary(header, headerCookie)
	}

	responseContext, ok := problem.FromContext(ctx)

	if !ok {
		responseContext = problem.NewResponseContext()
		ctx = problem.NewContext(ctx, responseContext)
	}

	responseContext.SetHeader(headerContentLanguage, tag)

	known := responseContext.Headers().Values(headerVary)

	for _, value := range header.Values(headerVary) {
		if !slices.Contains(known, value) {
			responseContext.AddHeader(headerVary, value)
		}
	}

	return ctx
}

// addVary adds value to the Vary header unless it is already listed.
func addVary(header http.Header, value string) {
	for _, existing := range header.Values(headerVary) {
		for part := range strings.SplitSeq(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}

	header.Add(headerVary, value)
}
