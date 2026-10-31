package cors

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

// TestCompletedSettingsAppliesDefaults verifies the default CORS configuration.
func TestCompletedSettingsAppliesDefaults(t *testing.T) {
	t.Parallel()

	config := newSettings()

	if !reflect.DeepEqual(config.AllowedOrigins, []string{"*"}) || config.MaxAge != 5*time.Minute ||
		!reflect.DeepEqual(config.AllowedMethods, []string{http.MethodGet, http.MethodPost, http.MethodHead}) {
		t.Fatalf("config = %#v", config)
	}
}

// TestCompletedSettingsAppliesCallbacks verifies each setting and their order.
func TestCompletedSettingsAppliesCallbacks(t *testing.T) {
	t.Parallel()

	config := newSettings(
		func(corsPolicy *Builder) {
			corsPolicy.settings = settings{}
		},
		func(corsPolicy *Builder) {
			corsPolicy.AllowedOrigins("https://a.example")
		},
		func(corsPolicy *Builder) {
			corsPolicy.AllowedMethods(http.MethodPut)
		},
		func(corsPolicy *Builder) {
			corsPolicy.AllowedHeaders("X-A")
		},
		func(corsPolicy *Builder) {
			corsPolicy.ExposedHeaders("X-B")
		},
		func(corsPolicy *Builder) {
			corsPolicy.AllowCredentials(true)
		},
		func(corsPolicy *Builder) {
			corsPolicy.MaxAge(time.Minute)
		},
		func(corsPolicy *Builder) {
			corsPolicy.MaxAge(90 * time.Second)
		},
	)

	want := settings{
		AllowedOrigins:   []string{"https://a.example"},
		AllowedMethods:   []string{http.MethodPut},
		AllowedHeaders:   []string{"X-A"},
		ExposedHeaders:   []string{"X-B"},
		AllowCredentials: true,
		MaxAge:           90 * time.Second,
	}

	if !reflect.DeepEqual(config, want) {
		t.Fatalf("config = %#v, want %#v", config, want)
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	Middleware(func(corsPolicy *Builder) {
		retained = corsPolicy
		order++

		if corsPolicy.AllowedOrigins("https://app.example.com") != corsPolicy {
			t.Fatal("setting returned another draft")
		}
	}, func(corsPolicy *Builder) {
		if order != 1 || corsPolicy != retained {
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
	Middleware(nil, func(corsPolicy *Builder) {
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
	Middleware(func(corsPolicy *Builder) {
		panic("construction sentinel")
	}, func(corsPolicy *Builder) {
		reached = true
	})
}

// TestAllowedOriginsDetachesInputAndDraft checks capture and publication copies.
// Later caller or retained-root edits cannot alter the fixed policy.
func TestAllowedOriginsDetachesInputAndDraft(t *testing.T) {
	t.Parallel()

	values := []string{"https://app.example.com"}
	var retained *Builder
	config := newSettings(func(corsPolicy *Builder) {
		retained = corsPolicy
		corsPolicy.AllowedOrigins(values...)

		values[0] = "changed before publication"
	})

	retained.settings.AllowedOrigins[0] = "changed after publication"

	if config.AllowedOrigins[0] != "https://app.example.com" {
		t.Fatal("published settings alias caller or retained draft")
	}
}

// TestAllowedMethodsDetachesInputAndDraft checks capture and publication copies.
// Later caller or retained-root edits cannot alter the fixed policy.
func TestAllowedMethodsDetachesInputAndDraft(t *testing.T) {
	t.Parallel()

	values := []string{"GET"}
	var retained *Builder
	config := newSettings(func(corsPolicy *Builder) {
		retained = corsPolicy
		corsPolicy.AllowedMethods(values...)

		values[0] = "changed before publication"
	})

	retained.settings.AllowedMethods[0] = "changed after publication"

	if config.AllowedMethods[0] != "GET" {
		t.Fatal("published settings alias caller or retained draft")
	}
}

// TestAllowedHeadersDetachesInputAndDraft checks capture and publication copies.
// Later caller or retained-root edits cannot alter the fixed policy.
func TestAllowedHeadersDetachesInputAndDraft(t *testing.T) {
	t.Parallel()

	values := []string{"X-Request-ID"}
	var retained *Builder
	config := newSettings(func(corsPolicy *Builder) {
		retained = corsPolicy
		corsPolicy.AllowedHeaders(values...)

		values[0] = "changed before publication"
	})

	retained.settings.AllowedHeaders[0] = "changed after publication"

	if config.AllowedHeaders[0] != "X-Request-ID" {
		t.Fatal("published settings alias caller or retained draft")
	}
}

// TestExposedHeadersDetachesInputAndDraft checks capture and publication copies.
// Later caller or retained-root edits cannot alter the fixed policy.
func TestExposedHeadersDetachesInputAndDraft(t *testing.T) {
	t.Parallel()

	values := []string{"X-Trace-ID"}
	var retained *Builder
	config := newSettings(func(corsPolicy *Builder) {
		retained = corsPolicy
		corsPolicy.ExposedHeaders(values...)

		values[0] = "changed before publication"
	})

	retained.settings.ExposedHeaders[0] = "changed after publication"

	if config.ExposedHeaders[0] != "X-Trace-ID" {
		t.Fatal("published settings alias caller or retained draft")
	}
}
