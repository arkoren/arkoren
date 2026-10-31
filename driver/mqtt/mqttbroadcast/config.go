package mqttbroadcast

import (
	"log/slog"
	"time"
)

// Builder configures one construction draft. New and NewFrom invoke supplied
// callbacks directly, once, in order; a nil callback panics. An expanded nil
// callback slice makes no changes. Retaining this draft does not permit changes
// to the constructed driver. QoS defaults to 1, KeepAlive to 30 seconds, and
// authentication to empty. A nil Logger selects slog.Default(). Loggers and
// native managers remain shared; Builder has no native TLS or SDK settings.
//
// Example:
//
//	var configure mqttbroadcast.BuilderFunc = func(broker *mqttbroadcast.Builder) {
//		broker.QoS(1)
//	}
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// config holds draft settings, never the driver's published root.
	config Config
}

// BuilderFunc edits one fresh construction draft once in argument order. Nil
// entries panic; omitted callbacks retain defaults. Native managers stay shared.
type BuilderFunc = func(*Builder)

// Config is a diagnostic MQTT settings record, not a constructor input.
// Formatting and slog redact Password; direct field access does not. Native
// managers, callbacks, and loggers retain their original shared ownership.
//
// Example:
//
//	record := mqttbroadcast.Config{QoS: 1}
//	_ = record.QoS
type Config struct {
	// QoS selects 0, 1, or 2. The zero record value means QoS 0.
	QoS byte
	// Username is optional authentication data, preserved by formatting.
	Username string
	// Password is optional authentication data, redacted by formatting.
	Password string
	// KeepAlive is whole seconds from 1s through 65535s. Zero restores the
	// 30-second default; a negative duration disables keepalive.
	KeepAlive time.Duration
	// Logger records handler panics and reconnect failures; nil uses slog.Default.
	Logger *slog.Logger
}

// DefaultMQTTQoS is the initial delivery level. Builder.QoS(0) retains QoS 0.
//
// Example:
//
//	_ = mqttbroadcast.DefaultMQTTQoS
const DefaultMQTTQoS byte = 1

// DefaultMQTTKeepAlive is the default connection keepalive request.
//
// Example:
//
//	_ = mqttbroadcast.DefaultMQTTKeepAlive
const DefaultMQTTKeepAlive time.Duration = 30 * time.Second

// defaultConfig returns independent initial policy without lifecycle I/O.
func defaultConfig() Config {
	return Config{QoS: DefaultMQTTQoS, KeepAlive: DefaultMQTTKeepAlive}
}

// QoS selects delivery level 0, 1, or 2 and returns the same draft. The initial
// default is 1; explicit zero selects at-most-once delivery. New rejects other
// values after callbacks. NewFrom retains them but Publish and Subscribe reject
// them before contacting Paho because NewFrom has no error result.
//
// Example:
//
//	configure := func(broker *mqttbroadcast.Builder) {
//		broker.QoS(1)
//	}
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) QoS(qos byte) *Builder {
	broker.config.QoS = qos

	return broker
}

// Username sets optional authentication, empty by default, and returns the same
// draft. New applies Password only when Username is nonempty; URL userinfo is
// rejected rather than used for authentication. NewFrom ignores this setting.
// Use TLS and load credentials from application configuration rather than source.
//
// Example:
//
//	configure := func(broker *mqttbroadcast.Builder) {
//		broker.Username("billing")
//	}
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Username(username string) *Builder {
	broker.config.Username = username

	return broker
}

// Password sets optional authentication, empty by default, and returns the same
// draft. It is applied only with a nonempty Username; New rejects URL userinfo
// rather than using it for authentication. NewFrom ignores it. The string is
// retained in memory; load real passwords from a secret source and use TLS.
//
// Example:
//
//	configure := func(broker *mqttbroadcast.Builder) {
//		broker.Username("billing")
//		broker.Password(os.Getenv("MQTT_PASSWORD"))
//	}
//	driver, err := mqttbroadcast.New([]string{"mqtts://broker.example:8883"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Password(password string) *Builder {
	broker.config.Password = password

	return broker
}

// KeepAlive sets the maximum idle interval between outbound control packets and
// returns the same draft. Zero restores the 30-second default; negative disables
// MQTT keepalive. New rejects positive fractions of a second and values above
// 65535 seconds instead of truncating or overflowing the wire field. NewFrom
// ignores this setting because its manager already owns connection policy. The
// broker can override the requested interval in its CONNACK response.
//
// Example:
//
//	configure := func(broker *mqttbroadcast.Builder) {
//		broker.KeepAlive(30 * time.Second)
//	}
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) KeepAlive(keepAlive time.Duration) *Builder {
	broker.config.KeepAlive = keepAlive

	return broker
}

// Logger selects the shared logger for handler panic recovery and failed
// reconnect subscriptions, and returns the same draft. Nil selects slog.Default()
// at construction. Both New and NewFrom apply it; it does not configure Paho's
// separate native loggers. Ordinary handler errors do not produce retries.
//
// Example:
//
//	configure := func(broker *mqttbroadcast.Builder) {
//		broker.Logger(slog.Default())
//	}
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Logger(logger *slog.Logger) *Builder {
	broker.config.Logger = logger

	return broker
}
