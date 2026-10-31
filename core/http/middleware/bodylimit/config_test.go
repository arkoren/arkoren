package bodylimit

import (
	"testing"

	"arkoren.dev/core/http/request"
)

// TestCompletedSettingsDefaults checks that omitted, zero, negative, and
// replaced body-limit settings all normalize to the documented helper default.
func TestCompletedSettingsDefaults(t *testing.T) {
	t.Parallel()

	for _, opts := range [][]func(*Builder){nil, {func(limiter *Builder) {
		limiter.Limit(0)
	}}, {func(limiter *Builder) {
		limiter.Limit(-1)
	}}, {func(limiter *Builder) {
		limiter.settings = settings{}
	}}} {
		if got := newSettings(opts...).Limit; got != request.DefaultMaxBodySize {
			t.Errorf("Limit = %d, want %d", got, request.DefaultMaxBodySize)
		}
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	Middleware(func(limiter *Builder) {
		retained = limiter
		order++

		if limiter.Limit(1024) != limiter {
			t.Fatal("setting returned another draft")
		}
	}, func(limiter *Builder) {
		if order != 1 || limiter != retained {
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
	Middleware(nil, func(limiter *Builder) {
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
	Middleware(func(limiter *Builder) {
		panic("construction sentinel")
	}, func(limiter *Builder) {
		reached = true
	})
}

// TestBuilderPublicationDetachesRoot checks completed settings do not retain the
// mutable construction root. Services and function captures remain shared.
func TestBuilderPublicationDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *Builder
	config := newSettings(func(limiter *Builder) {
		retained = limiter
		limiter.Limit(1024)
	})
	retained.Limit(4096)

	if config.Limit != 1024 {
		t.Fatal("retained draft changed published settings")
	}
}
