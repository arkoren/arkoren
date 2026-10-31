package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTransportCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestTransportCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *TransportBuilder
	order := 0
	Transport(nil, func(transport *TransportBuilder) {
		retained = transport
		order++

		if transport.Propagation(nil) != transport {
			t.Fatal("setting returned another draft")
		}
	}, func(transport *TransportBuilder) {
		if order != 1 || transport != retained {
			t.Fatal("callbacks were reordered or used different drafts")
		}

		order++
	})

	if order != 2 {
		t.Fatalf("callback count = %d, want 2", order)
	}
}

// TestTransportOmissionAndEmptyExpansions supply zero callbacks. A supplied nil
// is instead misuse and must panic before a later callback can execute.
func TestTransportOmissionAndEmptyExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*TransportBuilder)
	Transport(nil, omitted...)
	Transport(nil, []func(*TransportBuilder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	Transport(nil, nil, func(transport *TransportBuilder) {
		reached = true
	})
}

// TestTransportApplicationPanicPropagates keeps callback misuse separate from
// ordinary constructor validation and prevents later policies from executing.
func TestTransportApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	reached := false
	defer func() {
		if value := recover(); value != "construction sentinel" || reached {
			t.Fatalf("panic = %v, later callback = %v", value, reached)
		}
	}()
	Transport(nil, func(transport *TransportBuilder) {
		panic("construction sentinel")
	}, func(transport *TransportBuilder) {
		reached = true
	})
}

// TestTransportInstanceCallbacksRunOnce checks the instance path independently
// from package defaults; configuring an inactive instance performs no I/O.
func TestTransportInstanceCallbacksRunOnce(t *testing.T) {
	t.Parallel()

	instance := New(nil)
	calls := 0
	instance.Transport(nil, func(transport *TransportBuilder) {
		calls++
		transport.Propagation(nil)
	})

	if calls != 1 {
		t.Fatalf("callback count = %d, want 1", calls)
	}
}

// TestTransportStoresPropagationAndDetachesRoot verifies constructor delegation
// stores a request predicate without executing it or retaining the mutable root.
func TestTransportStoresPropagationAndDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *TransportBuilder
	calls := 0
	published := Transport(nil, func(transport *TransportBuilder) {
		retained = transport
		transport.Propagation(func(*http.Request) bool {
			calls++

			return true
		})
	}).(*transport)
	retained.Propagation(PropagateNone)

	if calls != 0 {
		t.Fatal("propagation predicate ran during construction")
	}

	if !published.propagate(httptest.NewRequest("GET", "https://example.com", nil)) || calls != 1 {
		t.Fatal("published predicate changed or was replayed")
	}
}

// TestPropagationHostsCopiesInputs fixes the destination policy at capture and
// publication while preserving the default permission for internal destinations.
func TestPropagationHostsCopiesInputs(t *testing.T) {
	t.Parallel()

	hosts := []string{"API.PARTNER.COM"}
	var retained *TransportBuilder
	config := newTransportSettings(func(transport *TransportBuilder) {
		retained = transport
		transport.PropagationHosts(hosts...)

		hosts[0] = "changed.example.com"
	})
	retained.Propagation(PropagateNone)

	if !config.Propagate(httptest.NewRequest("GET", "https://api.partner.com", nil)) {
		t.Fatal("published host policy aliases input or root")
	}

	if config.Propagate(httptest.NewRequest("GET", "https://changed.example.com", nil)) {
		t.Fatal("changed caller host became trusted")
	}
}
