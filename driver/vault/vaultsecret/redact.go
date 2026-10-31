package vaultsecret

import (
	"fmt"
	"log/slog"
)

// redacted replaces non-empty secrets when a [Config] is formatted.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting it cannot call [Config.String] again.
type plainConfig Config

// redact returns a copy of config in which each secret field (Token) that is
// set is replaced by a placeholder. Empty secrets stay empty.
func (config Config) redact() plainConfig {
	if config.Token != "" {
		config.Token = redacted
	}

	return plainConfig(config)
}

// String formats config with its secrets replaced by "[REDACTED]" when set,
// so %v and %+v never print Token. Native Auth and Address formatting remains
// caller-owned, as documented on Config; this is not recursive sanitization.
//
// Example:
//
//	record := vaultsecret.Config{Mount: "application", Token: token}
//	diagnostic := record.String()
//	_ = diagnostic
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// It masks Token without copying or modifying a shared native Auth method.
//
// Example:
//
//	diagnostic := fmt.Sprintf("%#v", vaultsecret.Config{Token: token})
//	_ = diagnostic
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// Only the static Token field is automatically masked; keep other fields safe.
//
// Example:
//
//	slog.Info("Vault configuration", "config", vaultsecret.Config{Token: token})
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
