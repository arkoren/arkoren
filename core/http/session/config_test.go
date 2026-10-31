package session

import (
	"encoding/json/jsontext"
	"reflect"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	Middleware(cache.New(memorycache.New()), func(sessionPolicy *Builder) {
		retained = sessionPolicy
		order++

		if sessionPolicy.Name("app.session") != sessionPolicy {
			t.Fatal("setting returned another draft")
		}
	}, func(sessionPolicy *Builder) {
		if order != 1 || sessionPolicy != retained {
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
	Middleware(cache.New(memorycache.New()), nil, func(sessionPolicy *Builder) {
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
	Middleware(cache.New(memorycache.New()), func(sessionPolicy *Builder) {
		panic("construction sentinel")
	}, func(sessionPolicy *Builder) {
		reached = true
	})
}

// TestBuilderPublicationDetachesRoot checks completed settings do not retain the
// mutable construction root. Services and function captures remain shared.
func TestBuilderPublicationDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *Builder
	config := newSettings(func(sessionPolicy *Builder) {
		retained = sessionPolicy
		sessionPolicy.Name("published")
	})
	retained.Name("changed")

	if config.Name != "published" {
		t.Fatal("retained draft changed published settings")
	}
}

// TestTombstoneTTLWithoutMaxLifetime checks tombstone retention against idle
// and maximum lifetime settings so retiring an ID remains effective under
// either policy.
func TestTombstoneTTLWithoutMaxLifetime(t *testing.T) {
	t.Parallel()

	config := newSettings(func(sessionPolicy *Builder) {
		sessionPolicy.MaxLifetime(-1)
	}, func(sessionPolicy *Builder) {
		sessionPolicy.TTL(time.Hour)
	}).withDefaults()

	if got := tombstoneTTL(config); got != time.Hour {
		t.Fatalf("tombstoneTTL = %s, want 1h", got)
	}

	config = newSettings(func(sessionPolicy *Builder) {
		sessionPolicy.MaxLifetime(3 * time.Hour)
	}).withDefaults()

	if got := tombstoneTTL(config); got != 3*time.Hour {
		t.Fatalf("tombstoneTTL = %s, want 3h", got)
	}
}

// TestCompletedSettingsUsesPrivateDefaultKey verifies stable runtime defaults.
func TestCompletedSettingsUsesPrivateDefaultKey(t *testing.T) {
	t.Parallel()

	first := newSettings()

	second := newSettings(func(sessionPolicy *Builder) {
		sessionPolicy.settings = settings{}
	})

	if first.Handle != (Handle{}) || first.Handle != second.Handle || first.PersistenceTimeout != 5*time.Second {
		t.Fatal("default runtime was not stable")
	}
}

// TestCompletedSettingsAppliesCallbacks verifies each setting.
func TestCompletedSettingsAppliesCallbacks(t *testing.T) {
	t.Parallel()

	called := false
	handle := NewHandle()
	config := newSettings(
		func(sessionPolicy *Builder) {
			sessionPolicy.Name("n")
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.Namespace(cache.NewNamespace[jsontext.Value]("p"))
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.Path("/app")
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.Domain("example.com")
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.AllowInsecure(true)
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.SameSite("strict")
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.Partitioned(true)
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.TTL(time.Minute)
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.MaxLifetime(time.Hour)
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.ExpirationDelta(time.Second)
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.Handle(handle)
		},
		func(cookies *Builder) {
			cookies.ErrorHandler(func(error) {
				called = true
			})
		},
		func(sessionPolicy *Builder) {
			sessionPolicy.PersistenceTimeout(2 * time.Second)
		},
	)
	config.ErrorHandler(nil)

	config.ErrorHandler = nil
	want := settings{
		Name:               "n",
		Namespace:          cache.NewNamespace[jsontext.Value]("p"),
		Path:               "/app",
		Domain:             "example.com",
		AllowInsecure:      true,
		SameSite:           "strict",
		Partitioned:        true,
		TTL:                time.Minute,
		MaxLifetime:        time.Hour,
		ExpirationDelta:    time.Second,
		Handle:             handle,
		PersistenceTimeout: 2 * time.Second,
	}

	if !called || !reflect.DeepEqual(config, want) {
		t.Fatalf("config = %#v", config)
	}
}
