package ratelimit

import (
	"net/http"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/foundation/problem"

	"arkoren.dev/core/service/cache/memorycache"
)

// TestCompletedSettingsReturnsIndependentErrorResponses verifies independent defaults.
func TestCompletedSettingsReturnsIndependentErrorResponses(t *testing.T) {
	t.Parallel()

	first := newSettings()

	first.ErrorResponse = first.ErrorResponse.Derive(func(details *problem.Builder) {
		details.Extension("retry_after", "later")
	})

	second := newSettings()

	if second.ErrorResponse.Extensions() != nil || second.ErrorResponse.Status() != ErrRateLimited.Status() ||
		second.ErrorResponse.Title() != ErrRateLimited.Title() {
		t.Fatalf("second config = %#v", second)
	}
}

// TestCompletedSettingsAppliesCallbacks verifies each setting after replacement.
func TestCompletedSettingsAppliesCallbacks(t *testing.T) {
	t.Parallel()

	config := newSettings(
		func(limiter *Builder) {
			limiter.settings = settings{}
		},
		func(limiter *Builder) {
			limiter.Limit(5)
		},
		func(limiter *Builder) {
			limiter.Window(time.Second)
		},
		func(limiter *Builder) {
			limiter.KeyFunc(func(*http.Request) string {
				return "k"
			})
		},
		func(limiter *Builder) {
			limiter.Namespace(cache.NewNamespace[int64]("p"))
		},
		func(limiter *Builder) {
			limiter.ErrorResponse((problem.Details{}).Derive(func(details *problem.Builder) {
				details.Status(http.StatusTeapot)
			}))
		},
	)

	if config.Limit != 5 || config.Window != time.Second || config.KeyFunc(nil) != "k" ||
		config.Namespace.Name() != "p" || config.ErrorResponse.Status() != http.StatusTeapot {
		t.Fatalf("config = %#v", config)
	}
}

// TestCompletedSettingsFillsZeroValues verifies defaults after replacement.
func TestCompletedSettingsFillsZeroValues(t *testing.T) {
	t.Parallel()

	config := newSettings(func(limiter *Builder) {
		limiter.settings = settings{}
	})

	if config.Limit != 60 || config.Window != time.Minute || config.KeyFunc == nil ||
		config.ErrorResponse.Status() != http.StatusTooManyRequests {
		t.Fatalf("config = %#v", config)
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	Middleware(cache.New(memorycache.New()), func(limiter *Builder) {
		retained = limiter
		order++

		if limiter.Limit(5) != limiter {
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
	Middleware(cache.New(memorycache.New()), omitted...)
	Middleware(cache.New(memorycache.New()), []func(*Builder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	Middleware(cache.New(memorycache.New()), nil, func(limiter *Builder) {
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
	Middleware(cache.New(memorycache.New()), func(limiter *Builder) {
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
		limiter.Limit(5)
	})
	retained.Limit(9)

	if config.Limit != 5 {
		t.Fatal("retained draft changed published settings")
	}
}
