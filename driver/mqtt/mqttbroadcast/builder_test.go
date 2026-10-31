package mqttbroadcast

import (
	"log/slog"
	"testing"
	"time"
)

// TestNewFromUsesLogger checks the borrowed-client role retains the supplied
// logger rather than applying a connection-only policy or substituting one.
func TestNewFromUsesLogger(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	driver := NewFrom(nil, func(broker *Builder) {
		broker.Logger(logger)
	})

	if driver.logger != logger {
		t.Fatal("borrowed-client construction ignored Logger")
	}
}

// TestNewCapturesURLsBeforeCallbacks checks caller-owned failover URLs cannot
// change the driver during construction or after parsed native URLs are stored.
func TestNewCapturesURLsBeforeCallbacks(t *testing.T) {
	t.Parallel()

	urls := []string{"mqtt://localhost:1883"}

	driver, err := New(urls, func(broker *Builder) {
		urls[0] = "mqtt://localhost:1884"
	})

	if err != nil {
		t.Fatal(err)
	}

	urls[0] = "mqtt://localhost:1885"

	if got := driver.clientConfig.ServerUrls[0].String(); got != "mqtt://localhost:1883" {
		t.Fatalf("server URL = %q, want captured URL", got)
	}
}

// TestNewPublishesDetachedQoS checks retained drafts cannot change delivery
// quality and the explicit zero setting survives the QoS 1 default.
func TestNewPublishesDetachedQoS(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0

	driver, err := New([]string{"mqtt://localhost:1883"}, func(broker *Builder) {
		retained = broker
		calls++
		broker.QoS(0)
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.QoS(2)

	if calls != 1 || driver.qos != 0 {
		t.Fatal("construction replayed callbacks, retained its draft, or lost QoS 0")
	}
}

// TestNewFromAppliesOnlyBorrowedClientSettings preserves the native cyclic
// SetClient/HandlePublish bridge: nil is valid until the client is attached.
func TestNewFromAppliesOnlyBorrowedClientSettings(t *testing.T) {
	t.Parallel()

	calls := 0

	driver := NewFrom(nil, func(broker *Builder) {
		calls++
		broker.QoS(2)
		broker.Username("ignored")
		broker.Password("ignored")
		broker.KeepAlive(time.Second)
	})

	if calls != 1 || driver.owned || driver.Client() != nil {
		t.Fatal("borrowed client ownership or callback execution changed")
	}

	if driver.qos != 2 {
		t.Fatal("borrowed client ignored QoS")
	}
}

// TestNewRunsCallbacksBeforeInvalidURLs checks required-input validation
// follows all supplied policies and performs no connection I/O.
func TestNewRunsCallbacksBeforeInvalidURLs(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := New(nil, func(broker *Builder) {
		calls++
	})

	if calls != 1 || err == nil {
		t.Fatalf("callbacks = %d, error = %v", calls, err)
	}
}
