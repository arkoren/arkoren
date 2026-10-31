package server

import (
	"net/http"
	"testing"
	"time"
)

// TestCompletedSettingsAppliesDefaults checks the recommended server address,
// timeout, and header-size values produced when no callbacks are supplied.
func TestCompletedSettingsAppliesDefaults(t *testing.T) {
	t.Parallel()

	config := newSettings()

	if config.Addr != ":8080" || config.ReadHeaderTimeout != 10*time.Second ||
		config.WriteTimeout != 60*time.Second || config.ShutdownTimeout != 15*time.Second ||
		config.MaxHeaderBytes != 1<<20 {
		t.Fatalf("config = %#v", config)
	}
}

// TestCompletedSettingsFillsZeroValuesAfterReplacement checks that replacing
// the construction draft still normalizes omitted server settings after the
// last callback.
func TestCompletedSettingsFillsZeroValuesAfterReplacement(t *testing.T) {
	t.Parallel()

	config := newSettings(func(serverBuilder *Builder) {
		serverBuilder.settings = settings{
			Addr: ":9090",
		}
	})

	if config.Addr != ":9090" || config.ReadTimeout != 30*time.Second || config.IdleTimeout != 120*time.Second {
		t.Fatalf("config = %#v", config)
	}
}

// TestCompletedSettingsAppliesCallbacksInOrder checks that later server
// callbacks replace earlier values while preserving the remaining explicitly
// configured settings.
func TestCompletedSettingsAppliesCallbacksInOrder(t *testing.T) {
	t.Parallel()

	config := newSettings(
		func(serverBuilder *Builder) {
			serverBuilder.Addr(":1")
		},
		func(serverBuilder *Builder) {
			serverBuilder.Addr(":2")
		},
		func(serverBuilder *Builder) {
			serverBuilder.ReadHeaderTimeout(time.Second)
		},
		func(serverBuilder *Builder) {
			serverBuilder.ReadTimeout(2 * time.Second)
		},
		func(serverBuilder *Builder) {
			serverBuilder.IdleTimeout(3 * time.Second)
		},
		func(serverBuilder *Builder) {
			serverBuilder.MaxHeaderBytes(4)
		},
		func(serverBuilder *Builder) {
			serverBuilder.ShutdownTimeout(5 * time.Second)
		},
	)

	if config.Addr != ":2" || config.ReadHeaderTimeout != time.Second || config.ReadTimeout != 2*time.Second ||
		config.IdleTimeout != 3*time.Second || config.MaxHeaderBytes != 4 ||
		config.ShutdownTimeout != 5*time.Second {
		t.Fatalf("config = %#v", config)
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	New(http.NotFoundHandler(), func(serverBuilder *Builder) {
		retained = serverBuilder
		order++

		if serverBuilder.Addr("127.0.0.1:0") != serverBuilder {
			t.Fatal("setting returned another draft")
		}
	}, func(serverBuilder *Builder) {
		if order != 1 || serverBuilder != retained {
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
	New(http.NotFoundHandler(), omitted...)
	New(http.NotFoundHandler(), []func(*Builder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	New(http.NotFoundHandler(), nil, func(serverBuilder *Builder) {
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
	New(http.NotFoundHandler(), func(serverBuilder *Builder) {
		panic("construction sentinel")
	}, func(serverBuilder *Builder) {
		reached = true
	})
}

// TestBuilderPublicationDetachesRoot checks completed settings do not retain the
// mutable construction root. Services and function captures remain shared.
func TestBuilderPublicationDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *Builder
	config := newSettings(func(serverBuilder *Builder) {
		retained = serverBuilder
		serverBuilder.Addr(":9090")
	})
	retained.Addr(":9999")

	if config.Addr != ":9090" {
		t.Fatal("retained draft changed published settings")
	}
}
