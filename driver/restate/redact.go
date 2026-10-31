package restate

import (
	"fmt"
	"log/slog"
)

// redacted replaces non-empty secrets when a [Config] is formatted.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting it cannot call [Config.String] again.
type plainConfig Config

// redact returns a copy of config in which each secret field (AuthKey and AdminAuthKey) that
// is set is replaced by a placeholder. Empty secrets stay empty.
func (config Config) redact() plainConfig {
	if config.AuthKey != "" {
		config.AuthKey = redacted
	}

	if config.AdminAuthKey != "" {
		config.AdminAuthKey = redacted
	}

	return plainConfig(config)
}

// String formats config with its secrets replaced by "[REDACTED]" when set,
// so %v and %+v never print them.
// Native objects and URL fields retain their existing formatting; avoid putting
// secrets in URLs. It does not mutate the record or its shared dependencies.
//
// Example:
//
//	fmt.Println(Config{AuthKey: "example"}.String())
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// It redacts the same token fields, not arbitrary native object internals.
//
// Example:
//
//	fmt.Printf("%#v\n", Config{AdminAuthKey: "example"})
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// Configure structured logging with the record, not its raw token fields.
//
// Example:
//
//	slog.InfoContext(ctx, "host settings", "config", Config{AuthKey: "example"})
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
