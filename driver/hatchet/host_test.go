package hatchet

import (
	"errors"
	"testing"

	sdk "github.com/hatchet-dev/hatchet/sdks/go"
)

// TestHostSettingsDefaults verifies that construction supplies the expected
// native connection and worker defaults without startup I/O.
func TestHostSettingsDefaults(t *testing.T) {
	t.Parallel()

	config := buildSettings()

	if config.WorkerName != "arkoren" || config.Slots != 100 {
		t.Fatalf("config = %+v", config)
	}
}

// TestHostSettingsApplyCallbacksInOrder verifies that later construction
// callbacks replace settings applied by earlier callbacks.
func TestHostSettingsApplyCallbacksInOrder(t *testing.T) {
	t.Parallel()

	config := buildSettings(func(host *Builder) {
		host.Token("old")
	}, func(host *Builder) {
		host.Token("new")
	})

	if config.Token != "new" {
		t.Fatalf("Token = %q", config.Token)
	}
}

// TestClientIsNilBeforeStart verifies that construction does not create a
// native client before lifecycle startup.
func TestClientIsNilBeforeStart(t *testing.T) {
	t.Parallel()

	if New().Client() != nil {
		t.Fatal("Client() != nil")
	}
}

// TestPingFailsBeforeStart verifies that readiness reports the host's not-
// started sentinel before connecting.
func TestPingFailsBeforeStart(t *testing.T) {
	t.Parallel()

	if err := New().Ping(t.Context()); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Ping() error = %v", err)
	}
}

// TestRegisterRejectsNil verifies that a nil native registration callback is
// rejected before it can reach startup.
func TestRegisterRejectsNil(t *testing.T) {
	t.Parallel()

	if err := New().Register(nil); err == nil {
		t.Fatal("Register(nil) error = nil")
	}
}

// TestStartRejectsInvalidHostPort verifies that malformed connection settings
// fail at the existing startup validation boundary.
func TestStartRejectsInvalidHostPort(t *testing.T) {
	t.Parallel()

	err := New(func(host *Builder) {
		host.HostPort("nope")
	}, func(host *Builder) {
		host.Token("t")
	}).Start(t.Context())

	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Start() error = %v", err)
	}
}

// TestStartRejectsUnknownTLSStrategy verifies that an unsupported TLS mode is
// rejected instead of silently selecting another transport.
func TestStartRejectsUnknownTLSStrategy(t *testing.T) {
	t.Parallel()

	err := New(func(host *Builder) {
		host.TLSStrategy("mtls")
	}).Start(t.Context())

	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Start() error = %v", err)
	}
}

// TestStopWithoutStartSucceeds verifies that stopping a host that never started
// requires no native resources and succeeds.
func TestStopWithoutStartSucceeds(t *testing.T) {
	t.Parallel()

	if err := New().Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

// TestStartRejectsMalformedToken verifies that an invalid credential fails
// startup rather than creating a usable native client.
func TestStartRejectsMalformedToken(t *testing.T) {
	t.Parallel()

	if err := New(func(host *Builder) {
		host.Token("not-a-token")
	}).Start(t.Context()); err == nil {
		t.Fatal("Start() error = nil")
	}
}

// TestHostSettingsWorkerPolicy verifies that explicit durable slots,
// registration timeout, and client-only settings survive construction.
func TestHostSettingsWorkerPolicy(t *testing.T) {
	t.Parallel()

	config := buildSettings(func(host *Builder) {
		host.DurableSlots(7)
	}, func(host *Builder) {
		host.NoWorker(true)
	})

	if config.DurableSlots != 7 || !config.NoWorker {
		t.Fatalf("config = %+v", config)
	}
}

// TestNamespacePrefix verifies that configured namespaces are normalized with
// the separator expected by native workflow names.
func TestNamespacePrefix(t *testing.T) {
	t.Parallel()

	if got := New(func(host *Builder) {
		host.Namespace("ns")
	}).Namespace(); got != "ns_" {
		t.Fatalf("Namespace() = %q", got)
	}

	if got := New().Namespace(); got != "" {
		t.Fatalf("Namespace() = %q", got)
	}
}

// TestFailedStartKeepsRegistrationOpen verifies that invalid startup settings
// leave registration open, so a failed connection attempt does not freeze later
// declarations.
func TestFailedStartKeepsRegistrationOpen(t *testing.T) {
	t.Parallel()

	host := New(func(host *Builder) {
		host.NoWorker(true)
	}, func(host *Builder) {
		host.Token("x")
	}, func(host *Builder) {
		host.TLSStrategy("bogus")
	})

	if err := host.Start(t.Context()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Start() error = %v", err)
	}

	if err := host.Register(func(*sdk.Client) (sdk.WorkflowBase, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("Register() after failed Start error = %v", err)
	}
}
