package locale

import (
	"maps"

	"arkoren.dev/foundation/validate"
)

// BuilderFunc edits one locale construction draft. Policies run once,
// synchronously in order, before final tag and cookie-name validation. A supplied
// nil policy panics; an expanded nil slice supplies no policies. Catalog maps are
// detached at publication, including maps kept through a retained draft.
//
// Example:
//
//	var selectLanguage locale.BuilderFunc = func(language *locale.Builder) {
//		language.QueryParam("lang")
//	}
//	routes.Use(locale.Middleware(selectLanguage))
type BuilderFunc = func(*Builder)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	language := &Builder{
		settings: settings{
			Catalog: validate.Catalog{
				Default: fallbackLocale,
				Locales: map[string]validate.Messages{
					fallbackLocale: validate.English(),
				},
			},
		},
	}

	for _, configure := range configure {
		configure(language)
	}

	config := language.settings

	config.Catalog = cloneCatalog(config.Catalog)

	return config
}

// cloneCatalog detaches the locale index and each message map. Message strings
// are immutable; copying both map layers fixes the middleware's translations
// without taking ownership of the caller's mutable catalog.
func cloneCatalog(catalog validate.Catalog) validate.Catalog {
	catalog.Locales = maps.Clone(catalog.Locales)

	for locale, messages := range catalog.Locales {
		catalog.Locales[locale] = maps.Clone(messages)
	}

	return catalog
}
