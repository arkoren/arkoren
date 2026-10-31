package csrf_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/middleware/csrf"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ExampleBuilderFunc deliberately adds a trusted partner origin while keeping
// the remaining native cross-origin policy unchanged.
func ExampleBuilderFunc() {
	configure := csrf.BuilderFunc(func(protection *csrf.Builder) {
		protection.TrustedOrigins("https://partner.example")
	})

	routes := router.New()
	routes.Use(csrf.Middleware(configure))
	routes.Post("/", func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "https://service.example/", nil)
	request.Header.Set("Origin", "https://partner.example")
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	recorder := httptest.NewRecorder()

	routes.ServeHTTP(recorder, request)
	fmt.Println(recorder.Code)
	// Output: 204
}

// TestMiddlewareBlocksCrossSitePost verifies cross-site POST rejection.
func TestMiddlewareBlocksCrossSitePost(t *testing.T) {
	t.Parallel()

	handler := csrf.Middleware()(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next handler called")

		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	if response := router.Handler(handler).Record(request); response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

// TestMiddlewareRejectsMalformedTrustedOrigin checks that CSRF construction
// rejects a malformed trusted origin before any request is processed.
func TestMiddlewareRejectsMalformedTrustedOrigin(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("malformed origin accepted")
		}
	}()
	csrf.Middleware(func(protection *csrf.Builder) {
		protection.TrustedOrigins("example.com/path")
	})
}

// TestMiddlewareUsesProvidedProtection verifies a custom protection applies.
func TestMiddlewareUsesProvidedProtection(t *testing.T) {
	t.Parallel()

	engine := http.NewCrossOriginProtection()

	if err := engine.AddTrustedOrigin("https://ok.example"); err != nil {
		t.Fatal(err)
	}

	handler := csrf.Middleware(func(protection *csrf.Builder) {
		protection.Protection(engine)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Origin", "https://ok.example")

	request.Host = "app.example"

	if err := handler(httptest.NewRecorder(), request); err != nil {
		t.Fatalf("trusted origin blocked: %v", err)
	}
}

// TestMiddlewareAppliesTrustedOriginsToProvidedProtection verifies both
// options combine: origins are added to the provided protection.
func TestMiddlewareAppliesTrustedOriginsToProvidedProtection(t *testing.T) {
	t.Parallel()

	engine := http.NewCrossOriginProtection()
	handler := csrf.Middleware(
		func(protection *csrf.Builder) {
			protection.Protection(engine)
		},
		func(protection *csrf.Builder) {
			protection.TrustedOrigins("https://ok.example")
		},
	)(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Origin", "https://ok.example")

	request.Host = "app.example"

	if err := handler(httptest.NewRecorder(), request); err != nil {
		t.Fatalf("trusted origin blocked: %v", err)
	}

	if err := engine.Check(request); err != nil {
		t.Fatalf("provided protection not updated: %v", err)
	}
}

// TestMiddlewareUsesCustomErrorResponse verifies Builder.ErrorResponse.
func TestMiddlewareUsesCustomErrorResponse(t *testing.T) {
	t.Parallel()

	custom := csrf.ErrBlocked.Derive(func(details *problem.Builder) {
		details.Detail("custom detail")
	})

	handler := csrf.Middleware(func(protection *csrf.Builder) {
		protection.ErrorResponse(custom)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	response := router.Handler(handler).Record(request)
	body := new(strings.Builder)

	if _, err := io.Copy(body, response.Body); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(body.String(), "custom detail") {
		t.Fatalf("body = %s", body.String())
	}
}
