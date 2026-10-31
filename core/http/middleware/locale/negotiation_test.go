package locale_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/middleware/locale"
	"arkoren.dev/foundation/validate"
)

// TestLocaleIgnoresMalformedQualityValues checks HTTP qvalue grammar rather
// than the much wider collection of numbers accepted by strconv.ParseFloat.
func TestLocaleIgnoresMalformedQualityValues(t *testing.T) {
	t.Parallel()

	for _, quality := range []string{"NaN", "+0.5", "0x1p-1", "1e-1", "0.1234", "1.001", "1.0000", "-0.1"} {
		got := resolved(t, withAccept("es;q="+quality), func(policy *locale.Builder) {
			policy.Catalog(testCatalog())
		})

		if got != "en" {
			t.Errorf("q=%q selected %q, want fallback en", quality, got)
		}
	}
}

// TestLocaleAcceptsValidQualityBounds retains the complete supported decimal
// qvalue range, including the smallest nonzero preference and padded one.
func TestLocaleAcceptsValidQualityBounds(t *testing.T) {
	t.Parallel()

	for _, quality := range []string{"1", "1.000", "0.999", "0.001"} {
		got := resolved(t, withAccept("es;q="+quality), func(policy *locale.Builder) {
			policy.Catalog(testCatalog())
		})

		requireLocale(t, got, "es")
	}
}

// TestLocaleDefaultWithoutCatalogEntryRemainsValid keeps fallback selection
// independent of whether an application supplied messages for that language.
func TestLocaleDefaultWithoutCatalogEntryRemainsValid(t *testing.T) {
	t.Parallel()

	got := resolved(t, withAccept("fr"), func(policy *locale.Builder) {
		policy.Catalog(validate.Catalog{Default: "de"})
	})

	requireLocale(t, got, "de")
}

// TestLocaleIgnoresAmbiguousQualityParameters skips missing and duplicate q
// parameters instead of treating them as an implicit or first-wins preference.
func TestLocaleIgnoresAmbiguousQualityParameters(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"es;q", "es;q=0.9;q=0"} {
		got := resolved(t, withAccept(header), func(policy *locale.Builder) {
			policy.Catalog(testCatalog())
		})

		requireLocale(t, got, "en")
	}
}

// TestLocaleIgnoresMalformedLanguageRanges prevents a malformed client range
// from matching a supported base language just because it begins with its name.
func TestLocaleIgnoresMalformedLanguageRanges(t *testing.T) {
	t.Parallel()

	for _, tag := range []string{"es-*", "es--MX", "es-123456789", "es-!"} {
		got := resolved(t, withAccept(tag), func(policy *locale.Builder) {
			policy.Catalog(testCatalog())
		})

		requireLocale(t, got, "en")
	}
}

// TestLocaleUnderscoreCatalogKeysKeepValidHTTPHeaders preserves the catalog's
// translation key in context while serializing a hyphenated Content-Language.
func TestLocaleUnderscoreCatalogKeysKeepValidHTTPHeaders(t *testing.T) {
	t.Parallel()

	var selected string
	handler := locale.Middleware(func(policy *locale.Builder) {
		policy.Catalog(validate.Catalog{
			Default: "pt_BR",
			Locales: map[string]validate.Messages{"pt_BR": {}},
		})
	})(func(_ http.ResponseWriter, request *http.Request) error {
		selected = locale.FromContext(request.Context())

		return nil
	})

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, withAccept("pt-BR"))

	if selected != "pt_BR" || recorder.Header().Get("Content-Language") != "pt-BR" {
		t.Fatalf("catalog key = %q, header = %q", selected, recorder.Header().Get("Content-Language"))
	}
}

// requireLocalePolicyPanic verifies invalid final configuration at the public
// constructor, after later callbacks have had the opportunity to replace it.
func requireLocalePolicyPanic(t *testing.T, configure locale.BuilderFunc) {
	t.Helper()

	called := false
	defer func() {
		if recover() == nil || !called {
			t.Error("invalid locale policy was not rejected after all callbacks")
		}
	}()
	locale.Middleware(configure, func(*locale.Builder) {
		called = true
	})
}

// TestLocaleRejectsInvalidCatalogTags keeps configured header values within
// language-tag syntax instead of copying arbitrary catalog keys into HTTP.
func TestLocaleRejectsInvalidCatalogTags(t *testing.T) {
	t.Parallel()

	requireLocalePolicyPanic(t, func(policy *locale.Builder) {
		policy.Catalog(validate.Catalog{
			Default: "en",
			Locales: map[string]validate.Messages{"en\r\nX-Injected: yes": {}},
		})
	})
}

// TestLocaleRejectsUnicodeCaseFoldedTag keeps Unicode characters from becoming
// ASCII only during matching while retaining a non-ASCII response header key.
func TestLocaleRejectsUnicodeCaseFoldedTag(t *testing.T) {
	t.Parallel()

	requireLocalePolicyPanic(t, func(policy *locale.Builder) {
		policy.Catalog(validate.Catalog{Locales: map[string]validate.Messages{"K": {}}})
	})
}

// TestLocaleRejectsInvalidDefaultTag also validates the fallback when it has
// no catalog entry; the supported-locale map must not be the only boundary.
func TestLocaleRejectsInvalidDefaultTag(t *testing.T) {
	t.Parallel()

	requireLocalePolicyPanic(t, func(policy *locale.Builder) {
		policy.Catalog(validate.Catalog{Default: "en, es"})
	})
}

// TestLocaleRejectsAmbiguousNormalizedTags prevents map iteration from deciding
// which translation wins when catalog keys differ only by case or separator.
func TestLocaleRejectsAmbiguousNormalizedTags(t *testing.T) {
	t.Parallel()

	requireLocalePolicyPanic(t, func(policy *locale.Builder) {
		policy.Catalog(validate.Catalog{Locales: map[string]validate.Messages{
			"pt-BR": {},
			"pt_BR": {},
		}})
	})
}

// TestLocaleRejectsMalformedCookieName catches configuration that net/http
// cannot read as a cookie name, rather than silently disabling that selector.
func TestLocaleRejectsMalformedCookieName(t *testing.T) {
	t.Parallel()

	requireLocalePolicyPanic(t, func(policy *locale.Builder) {
		policy.Cookie("lang;other")
	})
}

// TestLocaleEmptyCatalogAndSelectorsKeepFallback preserves explicit empty
// selectors and the documented built-in English fallback for an empty catalog.
func TestLocaleEmptyCatalogAndSelectorsKeepFallback(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/?lang=es", nil)
	request.Header.Set("Cookie", "lang=es")

	got := resolved(t, request, func(policy *locale.Builder) {
		policy.Catalog(validate.Catalog{})
		policy.QueryParam("")
		policy.Cookie("")
	})

	requireLocale(t, got, "en")
}

// TestLocaleConfigurationValidationUsesFinalSettings lets the final callback
// repair an intermediate invalid catalog and cookie, without replay or mutation.
func TestLocaleConfigurationValidationUsesFinalSettings(t *testing.T) {
	t.Parallel()

	order := 0
	got := resolved(t, withAccept("es"), func(policy *locale.Builder) {
		order++
		policy.Catalog(validate.Catalog{Default: "invalid,tag"})
		policy.Cookie("bad;name")
	}, func(policy *locale.Builder) {
		if order != 1 {
			t.Fatal("callbacks were replayed or reordered")
		}

		order++
		policy.Catalog(testCatalog())
		policy.Cookie("")
	})

	if got != "es" || order != 2 {
		t.Fatalf("locale = %q, callbacks = %d", got, order)
	}
}
