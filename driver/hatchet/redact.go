package hatchet

import (
	"fmt"
	"log/slog"
)

// redacted replaces non-empty secrets when a [Config] is formatted.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting it cannot call [Config.String] again.
type plainConfig Config

// redact returns a copy of config in which each secret field (Token) that
// is set is replaced by a placeholder. Empty secrets stay empty.
func (config Config) redact() plainConfig {
	if config.Token != "" {
		config.Token = redacted
	}

	return plainConfig(config)
}

// String formats config with its secrets replaced by "[REDACTED]" when set,
// so %v and %+v do not print Token. It copies the record, not SDK hook captures;
// this formatting is not a general-purpose sanitizer for arbitrary native data.
//
// Example:
//
//	record := Config{Token: "secret"}
//	text := record.String()
//	_ = strings.Contains(text, "[REDACTED]") // true
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// The Go-syntax representation retains nonsecret diagnostic settings.
//
// Example:
//
//	record := Config{Token: "secret"}
//	_ = record.GoString() // Token is redacted, not a restorable credential.
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// It returns a string value without mutating the record or its Token.
//
// Example:
//
//	record := Config{Token: "secret"}
//	slog.Info("Hatchet settings", "config", record.LogValue())
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
