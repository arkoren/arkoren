package hatchet

import (
	"log/slog"
	"testing"

	sdk "github.com/hatchet-dev/hatchet/sdks/go"
)

// TestHostConstructionCallbacks verifies ordered, once-only edits and
// publication isolation from retained construction drafts.
func TestHostConstructionCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	var retained *Builder

	component := New(func(host *Builder) {
		calls++
		retained = host

		if host.WorkerName("first") != host {
			t.Fatal("setting returned a different draft")
		}
	}, func(host *Builder) {
		if calls != 1 || host != retained {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		host.WorkerName("worker")
	})

	retained.WorkerName("changed")

	if calls != 2 || component.config.WorkerName != "worker" {
		t.Fatalf("published settings changed or callback replayed: calls=%d settings=%+v", calls, component.config)
	}
}

// TestHostEmptyConstructionCallbacks verifies that omitted policies and
// expanded nil and empty policy slices all preserve the same defaults.
func TestHostEmptyConstructionCallbacks(t *testing.T) {
	t.Parallel()

	var absent []func(*Builder)
	component := New(absent...)

	empty := []func(*Builder){}
	other := New(empty...)

	if component.config.WorkerName != other.config.WorkerName {
		t.Fatal("empty construction policies changed defaults")
	}
}

// TestHostNilConstructionCallback verifies that supplied nil callbacks are
// programmer errors, not silently ignored optional settings.
func TestHostNilConstructionCallback(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_ = New(nil)
}

// TestHostCopiesClientOptions verifies input capture and publication ownership
// without invoking native SDK callbacks during host construction.
func TestHostCopiesClientOptions(t *testing.T) {
	t.Parallel()

	// A nil native option also proves construction does not execute SDK hooks.
	options := []sdk.ClientOpt{sdk.WithToken("initial"), nil}
	var retained *Builder

	host := New(func(host *Builder) {
		retained = host
		host.ClientOptions(options...)
		options[0] = nil
	})

	retained.config.ClientOptions[0] = nil

	if host.config.ClientOptions[0] == nil {
		t.Fatal("native options were invoked or their container was shared")
	}
}

// TestNoWorkerPermission verifies the false default, explicit restoration, and
// HasWorker's permission meaning even when no workflow is registered.
func TestNoWorkerPermission(t *testing.T) {
	t.Parallel()

	if !New().HasWorker() {
		t.Fatal("worker permission must default to enabled")
	}

	host := New(func(host *Builder) {
		host.NoWorker(true)
		host.NoWorker(false)
	})

	if !host.HasWorker() || len(host.registrations) != 0 {
		t.Fatal("false must restore permission without inventing registrations")
	}
}

// TestHostNormalizesEmptySettings verifies defaults are applied after callbacks,
// including the existing nonpositive registration timeout fallback.
func TestHostNormalizesEmptySettings(t *testing.T) {
	t.Parallel()

	host := New(func(host *Builder) {
		host.WorkerName("")
		host.Slots(0)
		host.RegisterTimeout(-1)
	})

	if host.config.WorkerName != DefaultWorkerName || host.config.Slots != DefaultSlots {
		t.Fatal("empty worker settings did not use defaults")
	}

	if host.config.RegisterTimeout != DefaultRegisterTimeout {
		t.Fatal("nonpositive registration timeout did not use its default")
	}
}

// TestNativeClientOptionsFollowDerivedSettings checks native option ordering
// without connecting to an engine. The logger override remains in connect.
func TestNativeClientOptionsFollowDerivedSettings(t *testing.T) {
	t.Parallel()

	logger := slog.Default()
	host := New(func(host *Builder) {
		host.Token("derived")
		host.Namespace("known")
		host.Logger(logger)
		host.ClientOptions(sdk.WithToken("native"))
	})

	options, err := host.clientOptions()

	if err != nil {
		t.Fatal(err)
	}

	if len(options) < 2 || host.Namespace() != "known_" || host.logger != logger {
		t.Fatal("native options replaced driver-known namespace or host logger")
	}
}
