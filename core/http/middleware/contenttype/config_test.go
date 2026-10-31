package contenttype

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// TestMiddlewareReturnsProblemDetails verifies the rejection body is RFC 9457
// problem details and honors a custom error response.
func TestMiddlewareReturnsProblemDetails(t *testing.T) {
	t.Parallel()

	handler := Middleware()(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("a=b"))
	request.Header.Set("Content-Type", "text/plain")

	response := router.Handler(handler).Record(request)

	if response.StatusCode != http.StatusUnsupportedMediaType ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("status = %d, content-type = %q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	custom := newSettings(func(content *Builder) {
		content.ErrorResponse((problem.Details{}).Derive(func(details *problem.Builder) {
			details.Status(http.StatusTeapot)
		}))
	})

	if custom.ErrorResponse.Status() != http.StatusTeapot {
		t.Fatalf("config = %#v", custom)
	}
}

// TestCompletedSettingsDefaults verifies defaults, callback order, and list isolation.
func TestCompletedSettingsDefaults(t *testing.T) {
	t.Parallel()

	config := newSettings(func(content *Builder) {
		content.settings = settings{}
	})

	if len(config.Allowed) != 2 || config.ErrorResponse.Status() != http.StatusUnsupportedMediaType {
		t.Fatalf("config = %#v", config)
	}

	types := []string{"Text/Plain"}
	config = newSettings(func(content *Builder) {
		content.Allowed(types...)
	})

	types[0] = "changed"

	if config.Allowed[0] != "text/plain" {
		t.Fatalf("allowed = %v, want a lowercased copy", config.Allowed)
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	Middleware(func(content *Builder) {
		retained = content
		order++

		if content.Allowed("application/json") != content {
			t.Fatal("setting returned another draft")
		}
	}, func(content *Builder) {
		if order != 1 || content != retained {
			t.Fatal("callbacks were reordered or used different drafts")
		}

		order++
	})

	if order != 2 {
		t.Fatalf("callback count = %d, want 2", order)
	}
}

// TestConstructionOmissionAndEmptyExpansions supply zero callbacks. A supplied nil
// is instead misuse and must panic before a later callback can execute.
func TestConstructionOmissionAndEmptyExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)
	Middleware(omitted...)
	Middleware([]func(*Builder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	Middleware(nil, func(content *Builder) {
		reached = true
	})
}

// TestConstructionApplicationPanicPropagates keeps callback misuse separate from
// ordinary constructor validation and prevents later policies from executing.
func TestConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	reached := false
	defer func() {
		if value := recover(); value != "construction sentinel" || reached {
			t.Fatalf("panic = %v, later callback = %v", value, reached)
		}
	}()
	Middleware(func(content *Builder) {
		panic("construction sentinel")
	}, func(content *Builder) {
		reached = true
	})
}

// TestAllowedDetachesInputAndDraft checks capture and publication copies.
// Later caller or retained-root edits cannot alter the fixed policy.
func TestAllowedDetachesInputAndDraft(t *testing.T) {
	t.Parallel()

	values := []string{"text/plain"}
	var retained *Builder
	config := newSettings(func(content *Builder) {
		retained = content
		content.Allowed(values...)

		values[0] = "changed before publication"
	})

	retained.settings.Allowed[0] = "changed after publication"

	if config.Allowed[0] != "text/plain" {
		t.Fatal("published settings alias caller or retained draft")
	}
}
