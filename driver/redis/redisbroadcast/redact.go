package redisbroadcast

import (
	"fmt"
	"log/slog"
)

// redacted replaces non-empty secrets when a [Config] is formatted.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting it cannot call [Config.String] again.
type plainConfig Config

// redact returns a copy of config whose Password is replaced by a placeholder
// when it is set.
func (config Config) redact() plainConfig {
	if config.Password != "" {
		config.Password = redacted
	}

	return plainConfig(config)
}

// String formats config with Password replaced by "[REDACTED]" when set, so
// %v and %+v never print the secret.
//
// Example:
//
//	record := redisbroadcast.Config{Password: "example"}
//	_ = record.String()
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// It does not change the retained password or shared native TLS settings.
//
// Example:
//
//	record := redisbroadcast.Config{Password: "example"}
//	_ = fmt.Sprintf("%#v", record)
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// It preserves String's password-hiding behavior during structured logging.
//
// Example:
//
//	record := redisbroadcast.Config{Password: "example"}
//	_ = record.LogValue().String()
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
