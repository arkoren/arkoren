package mqttbroadcast

import (
	"fmt"
	"log/slog"
)

// redacted replaces non-empty secrets when a [Config] is formatted.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting it cannot call [Config.String] again.
type plainConfig Config

// redact returns a copy of config in which each secret field (Password) that
// is set is replaced by a placeholder. Empty secrets stay empty.
func (config Config) redact() plainConfig {
	if config.Password != "" {
		config.Password = redacted
	}

	return plainConfig(config)
}

// String formats config with its secrets replaced by "[REDACTED]" when set,
// so %v and %+v never print Password. Username is not redacted, and direct field
// access is unchanged. This formatting does not sanitize native SDK objects.
//
// Example:
//
//	record := mqttbroadcast.Config{Password: "example"}
//	text := record.String()
//	_ = text // Password appears as [REDACTED].
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v. It redacts nonempty
// Password without mutating the diagnostic record or hiding Username.
//
// Example:
//
//	record := mqttbroadcast.Config{Password: "example"}
//	text := fmt.Sprintf("%#v", record)
//	_ = text // Password appears as [REDACTED].
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config. It
// retains Username and configuration metadata; direct password access is unsafe.
//
// Example:
//
//	record := mqttbroadcast.Config{Password: "example"}
//	slog.InfoContext(ctx, "MQTT settings", "config", record)
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
