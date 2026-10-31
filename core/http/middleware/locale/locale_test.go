package locale_test

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"arkoren.dev/core/http/middleware/locale"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"
)

// testCatalog supports en, es and pt-BR.
func testCatalog() validate.Catalog {
	return validate.Catalog{
		Default: "en",
		Locales: map[string]validate.Messages{
			"en": validate.English(),
			"es": {
				"required": "es obligatorio",
			},
			"pt-BR": {
				"required": "obrigatório",
			},
		},
	}
}

// resolved runs the middleware and returns the locale seen downstream.
func resolved(t *testing.T, httpRequest *http.Request, opts ...func(*locale.Builder)) string {
	t.Helper()

	var got string

	handler := locale.Middleware(opts...)(func(writer http.ResponseWriter, httpRequest *http.Request) error {
		got = locale.FromContext(httpRequest.Context())

		return nil
	})

	handler.ServeHTTP(httptest.NewRecorder(), httpRequest)

	return got
}

// withAccept returns a request with an Accept-Language header.
func withAccept(value string) *http.Request {
	httpRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	httpRequest.Header.Set("Accept-Language", value)

	return httpRequest
}

// requireLocale fails unless got equals want.
func requireLocale(t *testing.T, got, want string) {
	t.Helper()

	if got != want {
		t.Fatalf("expected locale %q, got %q", want, got)
	}
}

// TestMiddlewareDefaultsToCatalogDefault verifies the fallback.
func TestMiddlewareDefaultsToCatalogDefault(t *testing.T) {
	t.Parallel()

	requireLocale(t, resolved(t, httptest.NewRequest(http.MethodGet, "/", nil)), "en")
	requireLocale(t, resolved(t, withAccept("fr"), func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}), "en")
}

// TestMiddlewareExactMatch verifies an exact tag wins over its base language.
func TestMiddlewareExactMatch(t *testing.T) {
	t.Parallel()

	requireLocale(t, resolved(t, withAccept("pt-br"), func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}), "pt-BR")
}

// TestMiddlewareBaseLanguage verifies region tags fall back to the base language.
func TestMiddlewareBaseLanguage(t *testing.T) {
	t.Parallel()

	requireLocale(t, resolved(t, withAccept("es-MX"), func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}), "es")
}

// TestMiddlewareQValues verifies preference order and q=0 exclusion.
func TestMiddlewareQValues(t *testing.T) {
	t.Parallel()

	catalog := func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}

	requireLocale(t, resolved(t, withAccept("en;q=0.5, es;q=0.9"), catalog), "es")
	requireLocale(t, resolved(t, withAccept("es;q=0, pt-BR;q=0.1"), catalog), "pt-BR")
	requireLocale(t, resolved(t, withAccept("fr, *;q=0.1, es;q=0.2"), catalog), "es")
	requireLocale(t, resolved(t, withAccept("es;q=bad, pt-BR"), catalog), "pt-BR")
}

// TestMiddlewareQueryParam verifies the query parameter wins over the header.
func TestMiddlewareQueryParam(t *testing.T) {
	t.Parallel()

	httpRequest := httptest.NewRequest(http.MethodGet, "/?lang=es", nil)
	httpRequest.Header.Set("Accept-Language", "pt-BR")

	requireLocale(t, resolved(t, httpRequest, func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}, func(localePolicy *locale.Builder) {
		localePolicy.QueryParam("lang")
	}), "es")
	requireLocale(t, resolved(t, httpRequest, func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}), "pt-BR")
}

// TestMiddlewareUnsupportedQueryFallsThrough verifies an unsupported
// query value defers to the next source.
func TestMiddlewareUnsupportedQueryFallsThrough(t *testing.T) {
	t.Parallel()

	httpRequest := httptest.NewRequest(http.MethodGet, "/?lang=fr", nil)
	httpRequest.Header.Set("Accept-Language", "es")

	requireLocale(t, resolved(t, httpRequest, func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}, func(localePolicy *locale.Builder) {
		localePolicy.QueryParam("lang")
	}), "es")
}

// TestMiddlewareCookie verifies the cookie wins over the header.
func TestMiddlewareCookie(t *testing.T) {
	t.Parallel()

	httpRequest := withAccept("pt-BR")
	httpRequest.AddCookie(&http.Cookie{
		Name:  "lang",
		Value: "es",
	})

	requireLocale(t, resolved(t, httpRequest, func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}, func(localePolicy *locale.Builder) {
		localePolicy.Cookie("lang")
	}), "es")
	requireLocale(t, resolved(t, httpRequest, func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}), "pt-BR")
}

// TestMiddlewareSetsValidateContext verifies validate sees catalog and locale.
func TestMiddlewareSetsValidateContext(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[struct {
		// Name is intentionally empty so the required-field rule produces a
		// translatable violation.
		Name string
	}]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Required()
		})
	})

	var err error

	handler := locale.Middleware(func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	})(func(writer http.ResponseWriter, httpRequest *http.Request) error {
		err = schema.Validate(httpRequest.Context(), struct {
			// Name is intentionally empty so the required-field rule produces a
			// translatable violation.
			Name string
		}{})

		return nil
	})

	handler.ServeHTTP(httptest.NewRecorder(), withAccept("es-MX"))

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok || errs["/Name"][0].Message != "es obligatorio" {
		t.Fatalf("unexpected error %v", err)
	}
}

// TestMiddlewareSetsHeaders verifies headers on a normal response.
func TestMiddlewareSetsHeaders(t *testing.T) {
	t.Parallel()

	handler := locale.Middleware(func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	}, func(localePolicy *locale.Builder) {
		localePolicy.Cookie("lang")
	})(
		func(writer http.ResponseWriter, httpRequest *http.Request) error {
			writer.WriteHeader(http.StatusOK)

			return nil
		},
	)

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, withAccept("es"))

	if got := recorder.Header().Get("Content-Language"); got != "es" {
		t.Fatalf("unexpected Content-Language %q", got)
	}

	if got := recorder.Header().Values("Vary"); !slices.Equal(got, []string{"Accept-Language", "Cookie"}) {
		t.Fatalf("unexpected Vary %v", got)
	}
}

// TestMiddlewareSetsHeadersOnProblem verifies headers on an error response
// and that an existing Vary value is kept.
func TestMiddlewareSetsHeadersOnProblem(t *testing.T) {
	t.Parallel()

	handler := router.Middleware(func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, httpRequest *http.Request) error {
			writer.Header().Add("Vary", "Origin")

			return next(writer, httpRequest)
		}
	})(locale.Middleware(func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	})(func(http.ResponseWriter, *http.Request) error {
		return problem.BadRequest()
	}))

	httpRequest := withAccept("es")
	httpRequest.Header.Set("Accept", "application/problem+json")

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httpRequest)

	if got := recorder.Header().Get("Content-Language"); got != "es" {
		t.Fatalf("unexpected Content-Language %q", got)
	}

	// Problem negotiation may also add Accept; locale owns these two entries,
	// not the complete variation policy of the downstream renderer.
	vary := recorder.Header().Values("Vary")

	for _, required := range []string{"Origin", "Accept-Language"} {
		if !slices.Contains(vary, required) {
			t.Fatalf("Vary = %v, missing %s", vary, required)
		}
	}
}

// TestMiddlewareLocalizesValidationProblem verifies the full 422 rendering.
func TestMiddlewareLocalizesValidationProblem(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[struct {
		// Name is intentionally empty so the required-field rule produces a
		// translatable violation.
		Name string
	}]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Required()
		})
	})

	handler := locale.Middleware(func(localePolicy *locale.Builder) {
		localePolicy.Catalog(testCatalog())
	})(
		func(writer http.ResponseWriter, httpRequest *http.Request) error {
			return request.ValidationProblem(schema.Validate(httpRequest.Context(), struct {
				// Name is intentionally empty so the required-field rule produces a
				// translatable violation.
				Name string
			}{}))
		},
	)

	httpRequest := withAccept("pt-BR")
	httpRequest.Header.Set("Accept", "application/problem+json")

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httpRequest)

	var body struct {
		// Errors holds the localized violations decoded from the HTTP problem
		// response.
		Errors map[string][]validate.Violation `json:"errors"`
	}

	if err := json.UnmarshalRead(recorder.Body, &body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}

	if recorder.Code != http.StatusUnprocessableEntity || body.Errors["/Name"][0].Message != "obrigatório" {
		t.Fatalf("unexpected response %d %+v", recorder.Code, body)
	}

	if got := recorder.Header().Get("Content-Language"); got != "pt-BR" {
		t.Fatalf("unexpected Content-Language %q", got)
	}
}

// TestFromWithoutMiddleware verifies From returns an empty string.
func TestFromWithoutMiddleware(t *testing.T) {
	t.Parallel()

	requireLocale(t, locale.FromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context()), "")
}
