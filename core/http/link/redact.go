package link

import (
	"fmt"
	"log/slog"
)

// redacted replaces the secrets of a [Config] when it is formatted.
const redacted = "[REDACTED]"

// redactedConfig is the diagnostic projection of [Config]. It replaces the
// []key.Shared rotation list with one marker and omits cache and event services.
// Origin and ignored query names remain visible; applications must keep those
// configuration values free of credentials.
type redactedConfig struct {
	// Origin retains the configured application origin in diagnostic output.
	Origin string
	// PreviousSecrets indicates configured rotation keys without exposing their
	// bytes.
	PreviousSecrets string
	// IgnoreQuery lists the query names excluded from signature calculation.
	IgnoreQuery []string
}

// redact returns config in its printable form. PreviousSecrets is empty when
// config has none and "[REDACTED]" otherwise.
func (config Config) redact() redactedConfig {
	view := redactedConfig{
		Origin:      config.Origin,
		IgnoreQuery: config.IgnoreQuery,
	}

	if len(config.PreviousSecrets) > 0 {
		view.PreviousSecrets = redacted
	}

	return view
}

// String formats config with PreviousSecrets replaced by "[REDACTED]" when
// set, so %v and %+v never print them.
// It omits service pointers, but does not sanitize Origin or query-name strings.
//
// Example:
//
//	record := link.Config{PreviousSecrets: []key.Shared{previous}}
//	text := record.String()
//	_ = text // includes PreviousSecrets:[REDACTED], never the key bytes
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// Origin and query names remain diagnostic configuration, not secret values.
//
// Example:
//
//	text := fmt.Sprintf("%#v", link.Config{PreviousSecrets: []key.Shared{previous}})
//	_ = text // previous keys are represented by a redaction marker
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// Only rotation keys and services are omitted; supply a non-secret Origin.
//
// Example:
//
//	logger.InfoContext(ctx, "link settings", "settings", link.Config{Origin: "https://app.example.com"})
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
