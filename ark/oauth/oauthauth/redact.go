package oauthauth

import (
	"fmt"
	"log/slog"
)

// redacted replaces non-empty secrets when a [Config] is formatted.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting it cannot call [Config.String] again.
type plainConfig Config

// redact returns a copy of config in which each secret field (ClientSecret) that
// is set is replaced by a placeholder. Empty secrets stay empty.
func (config Config) redact() plainConfig {
	if config.ClientSecret != "" {
		config.ClientSecret = redacted
	}

	return plainConfig(config)
}

// String formats config with its secrets replaced by "[REDACTED]" when set,
// so %v and %+v never print them.
//
// Example:
//
//	text := (oauth.Config{ClientSecret: "secret"}).String()
//	_ = text // contains [REDACTED]
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// Empty secrets remain empty; formatting does not mutate the representation.
//
// Example:
//
//	text := (oauth.Config{ClientSecret: "secret"}).GoString()
//	_ = text // contains [REDACTED]
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// Use it for diagnostics without logging confidential client credentials.
//
// Example:
//
//	value := (oauth.Config{ClientSecret: "secret"}).LogValue()
//	_ = value.String() // contains [REDACTED]
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
