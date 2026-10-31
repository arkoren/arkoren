package mqttbroadcast_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/driver/mqtt/mqttbroadcast"
)

// TestNewRejectsMalformedBrokerURLs applies the same construction rejection to
// missing endpoints, unsupported transports, unusable ports, and ignored fields.
func TestNewRejectsMalformedBrokerURLs(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"", "localhost:1883", "mqtt://", "mqtt://:1883", "http://localhost:1883",
		"mqtt://localhost", "mqtt://localhost:0", "mqtt://localhost:65536",
		"mqtt://localhost:bad", "mqtt://localhost:1883/ignored",
		"mqtt://localhost:1883?ignored=true", "mqtt://localhost:1883#ignored",
		"wss://localhost:65536/mqtt", "wss://localhost/mqtt#ignored",
	} {
		if _, err := mqttbroadcast.New([]string{address}); err == nil {
			t.Fatalf("New(%q) succeeded", address)
		}
	}
}

// TestNewAcceptsPinnedPahoTransports preserves all explicit native aliases and
// WebSocket paths, queries, and default ports without performing network I/O.
func TestNewAcceptsPinnedPahoTransports(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"mqtt://localhost:1883", "tcp://localhost:1883", "mqtts://localhost:8883",
		"ssl://localhost:8883", "tls://localhost:8883", "mqtt+ssl://localhost:8883",
		"tcps://localhost:8883", "ws://localhost/mqtt?mode=events", "wss://localhost/mqtt",
		"mqtt://[::1]:1883",
	} {
		if _, err := mqttbroadcast.New([]string{address}); err != nil {
			t.Fatalf("New(%q): %v", address, err)
		}
	}
}

// TestNewURLFailuresDoNotExposeCredentials covers both parser failures and
// parsed-but-rejected userinfo. The error chain must not retain raw URL text.
func TestNewURLFailuresDoNotExposeCredentials(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"mqtt://private-user:private-password@localhost:bad",
		"mqtt://private-user:private-password@localhost:1883",
		"mqtt://private-user:private-password@%zz:1883",
	} {
		_, err := mqttbroadcast.New([]string{address})

		if err == nil {
			t.Fatal("credential URL was accepted")
		}

		if strings.Contains(err.Error(), "private-user") || strings.Contains(err.Error(), "private-password") {
			t.Fatalf("URL credentials leaked: %v", err)
		}
	}
}

// TestNewRejectsInvalidQoS verifies all out-of-range levels fail construction
// before a native connection can be created.
func TestNewRejectsInvalidQoS(t *testing.T) {
	t.Parallel()

	for _, qos := range []byte{3, 127, 255} {
		_, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, func(broker *mqttbroadcast.Builder) {
			broker.QoS(qos)
		})

		if err == nil {
			t.Fatalf("New accepted QoS %d", qos)
		}
	}
}

// TestNewFromRejectsInvalidQoSAtUse preserves its pointer-only constructor while
// refusing invalid native subscription bytes before even looking up a manager.
func TestNewFromRejectsInvalidQoSAtUse(t *testing.T) {
	t.Parallel()

	driver := mqttbroadcast.NewFrom(nil, func(broker *mqttbroadcast.Builder) {
		broker.QoS(3)
	})

	err := driver.Publish(context.Background(), broadcast.Message{Name: "sensor.changed"})

	if err == nil || !strings.Contains(err.Error(), "QoS") {
		t.Fatalf("Publish error = %v, want QoS validation", err)
	}

	_, err = driver.Subscribe(context.Background(), "sensor.changed", func([]byte) {
	})

	if err == nil || !strings.Contains(err.Error(), "QoS") {
		t.Fatalf("Subscribe error = %v, want QoS validation", err)
	}
}

// TestNewRejectsUnrepresentableKeepAlive prevents fractions from truncating and
// large durations from wrapping MQTT's uint16 seconds field.
func TestNewRejectsUnrepresentableKeepAlive(t *testing.T) {
	t.Parallel()

	for _, interval := range []time.Duration{time.Nanosecond, 1500 * time.Millisecond, 65536 * time.Second} {
		_, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, func(broker *mqttbroadcast.Builder) {
			broker.KeepAlive(interval)
		})

		if err == nil {
			t.Fatalf("New accepted KeepAlive %v", interval)
		}
	}
}

// TestNewAcceptsKeepAlivePolicy verifies default, disabled, and exact positive
// durations remain valid construction policies without lifecycle I/O.
func TestNewAcceptsKeepAlivePolicy(t *testing.T) {
	t.Parallel()

	for _, interval := range []time.Duration{0, -time.Second, time.Second, 45 * time.Second, 65535 * time.Second} {
		_, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, func(broker *mqttbroadcast.Builder) {
			broker.KeepAlive(interval)
		})

		if err != nil {
			t.Fatalf("New rejected KeepAlive %v: %v", interval, err)
		}
	}
}

// TestKeepAliveDurationEncodesSeconds observes the native CONNECT rather than
// driver internals, proving conversion divides duration units exactly once.
func TestKeepAliveDurationEncodesSeconds(t *testing.T) {
	t.Parallel()

	peer := newProtocolBroker(t)

	newOwnedProtocolDriver(t, peer, func(broker *mqttbroadcast.Builder) {
		broker.KeepAlive(45 * time.Second)
	})

	if packet := <-peer.connected; packet.KeepAlive != 45 {
		t.Fatalf("wire KeepAlive = %d, want 45 seconds", packet.KeepAlive)
	}
}

// TestKeepAliveZeroRestoresDefault observes the omitted default's wire value
// after an explicit zero, rather than depending on private builder storage.
func TestKeepAliveZeroRestoresDefault(t *testing.T) {
	t.Parallel()

	peer := newProtocolBroker(t)

	newOwnedProtocolDriver(t, peer, func(broker *mqttbroadcast.Builder) {
		broker.KeepAlive(0)
	})

	if packet := <-peer.connected; packet.KeepAlive != 30 {
		t.Fatalf("wire KeepAlive = %d, want default 30 seconds", packet.KeepAlive)
	}
}

// TestKeepAliveNegativeDisablesPings proves the disabled duration policy reaches
// MQTT as zero, not the constructor's default or a wrapped unsigned number.
func TestKeepAliveNegativeDisablesPings(t *testing.T) {
	t.Parallel()

	peer := newProtocolBroker(t)

	newOwnedProtocolDriver(t, peer, func(broker *mqttbroadcast.Builder) {
		broker.KeepAlive(-time.Second)
	})

	if packet := <-peer.connected; packet.KeepAlive != 0 {
		t.Fatalf("wire KeepAlive = %d, want disabled 0", packet.KeepAlive)
	}
}
