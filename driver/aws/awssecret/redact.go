package awssecret

import (
	"fmt"
	"log/slog"
)

// redactedConfig mirrors [Config] for formatting, with Credentials reduced
// to its type: a static provider would otherwise print its secret key.
type redactedConfig struct {
	// Region preserves the configured AWS region in diagnostic output.
	Region string

	// Endpoint preserves the configured service endpoint for diagnostics.
	Endpoint string

	// Name preserves the configured name while credentials remain hidden.
	Name string

	// Credentials contains only the provider type marker, never its secrets.
	Credentials string
}

// redact returns config with Credentials replaced by "[REDACTED <type>]" when set.
func (config Config) redact() redactedConfig {
	credentials := ""

	if config.Credentials != nil {
		credentials = fmt.Sprintf("[REDACTED %T]", config.Credentials)
	}

	return redactedConfig{
		Region:      config.Region,
		Endpoint:    config.Endpoint,
		Name:        config.Name,
		Credentials: credentials,
	}
}

// String formats config with its credentials redacted, so %v and %+v never
// print credential-provider contents. Other record fields are not sanitized.
//
// Example:
//
//	record := awssecret.Config{Region: "eu-west-1", Credentials: provider}
//	diagnostic := record.String()
//	_ = diagnostic
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// It preserves diagnostic fields while replacing providers with type markers.
//
// Example:
//
//	diagnostic := fmt.Sprintf("%#v", awssecret.Config{Credentials: provider})
//	_ = diagnostic
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// It does not retrieve credentials or invoke a provider's formatter.
//
// Example:
//
//	slog.Info("secret configuration", "config", awssecret.Config{Credentials: provider})
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
