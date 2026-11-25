package link

import (
	"fmt"
	"log/slog"
)

// redactedConfig is the diagnostic projection of [Config]. It omits the cache
// and event services. Origin and ignored query names remain visible;
// applications must keep those configuration values free of credentials.
type redactedConfig struct {
	// Origin retains the configured application origin in diagnostic output.
	Origin string
	// IgnoreQuery lists the query names excluded from signature calculation.
	IgnoreQuery []string
}

// redact returns config in its printable form without service pointers.
func (config Config) redact() redactedConfig {
	return redactedConfig{
		Origin:      config.Origin,
		IgnoreQuery: config.IgnoreQuery,
	}
}

// String formats config without service pointers, so %v and %+v never print
// them. It does not sanitize Origin or query-name strings.
//
// Example:
//
//	record := link.Config{Origin: "https://app.example.com"}
//	text := record.String()
//	_ = text // includes Origin and IgnoreQuery, never the live services
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// Origin and query names remain diagnostic configuration, not secret values.
//
// Example:
//
//	text := fmt.Sprintf("%#v", link.Config{Origin: "https://app.example.com"})
//	_ = text // service pointers are omitted
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the printable form of config.
// Services are omitted; supply a non-secret Origin.
//
// Example:
//
//	logger.InfoContext(ctx, "link settings", "settings", link.Config{Origin: "https://app.example.com"})
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
