package validate

import (
	"context"
	"maps"
	"time"
)

// contextKey is the type of every context key this package defines, so
// values cannot collide with keys from other packages.
type contextKey int

const (
	// catalogKey stores the [Catalog] used to render messages.
	catalogKey contextKey = iota

	// localeKey stores the requested locale tag.
	localeKey

	// clockKey stores the function that returns the current time.
	clockKey
)

// NewCatalogContext returns a context that renders violation messages from
// catalog. Locales missing from the catalog fall back to the built-in
// English messages. It snapshots the locale registry and each message map;
// callers may edit their original catalog after the call returns. Nil maps
// remain nil and retain the normal English fallback.
//
// Example:
//
//	catalog := validate.Catalog{
//		Default: "en",
//		Locales: map[string]validate.Messages{"en": {"required": "please enter a value"}},
//	}
//	ctx := validate.NewCatalogContext(context.Background(), catalog)
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Required()
//	})
//	if _, err := schema.Parse(ctx, ""); err != nil {
//		fmt.Println(err)
//	}
func NewCatalogContext(ctx context.Context, catalog Catalog) context.Context {
	catalog.Locales = maps.Clone(catalog.Locales)

	for locale, messages := range catalog.Locales {
		catalog.Locales[locale] = maps.Clone(messages)
	}

	return context.WithValue(ctx, catalogKey, catalog)
}

// NewLocaleContext returns a context that requests messages in locale, a
// language tag such as "es" or "es-MX".
func NewLocaleContext(ctx context.Context, locale string) context.Context {
	return context.WithValue(ctx, localeKey, locale)
}

// LocaleFromContext returns the locale stored by [NewLocaleContext]. The boolean is
// false when the context carries no locale.
func LocaleFromContext(ctx context.Context) (string, bool) {
	locale, ok := ctx.Value(localeKey).(string)

	return locale, ok
}

// NewClockContext returns a context whose Future and Past rules read the
// current time from now instead of [time.Now]. Tests use it to pin time.
func NewClockContext(ctx context.Context, now func() time.Time) context.Context {
	return context.WithValue(ctx, clockKey, now)
}

// currentTime returns the time reported by the context clock, or the
// system time when the context has none.
func currentTime(ctx context.Context) time.Time {
	if now, ok := ctx.Value(clockKey).(func() time.Time); ok && now != nil {
		return now()
	}

	return time.Now()
}
