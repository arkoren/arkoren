package smtpmail

import (
	"fmt"
	"log/slog"
)

// redacted replaces non-empty secrets when a [Config] is formatted.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting it cannot call [Config.String] again.
type plainConfig Config

// Format replaces fmt's value output with a fixed driver type label, ignoring
// the verb and flags passed to it. Reflection into a Driver would bypass Config's
// formatting methods and expose its password. The value receiver protects both
// driver pointers and copied driver values.
// The formatter has no error return, so errors from the fmt-owned writer cannot
// be propagated. It never formats credentials, hostnames, or configuration.
// The fmt package still handles type (%T) and pointer (%p) verbs itself.
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587)
//	if err != nil {
//		panic(err)
//	}
//	text := fmt.Sprintf("%+v", driver) // "smtpmail.Driver"
//	_ = text
func (driver Driver) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "smtpmail.Driver") // fmt.Formatter cannot return writer errors.
}

// redact returns a copy of config in which each secret field (Password) that
// is set is replaced by a placeholder. Empty secrets stay empty.
func (config Config) redact() plainConfig {
	if config.Password != "" {
		config.Password = redacted
	}

	return plainConfig(config)
}

// String formats config with its password replaced by "[REDACTED]" when set,
// so %v and %+v never print it. An empty password stays empty; other reporting
// fields, including Username, remain visible and may contain personal information.
//
// Example:
//
//	text := (smtpmail.Config{Password: "secret"}).String()
//	_ = strings.Contains(text, "[REDACTED]") // true
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString supplies Go-syntax formatting for %#v with the same password
// redaction as String. This protects the reporting record, not provider errors,
// native SMTP clients, or arbitrary message contents.
//
// Example:
//
//	text := fmt.Sprintf("%#v", smtpmail.Config{Password: "secret"})
//	_ = strings.Contains(text, "[REDACTED]") // true
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] as a redacted string, preventing a
// structured logger from serializing the raw password. Username and other
// non-secret reporting fields remain visible, as with String.
//
// Example:
//
//	value := (smtpmail.Config{Password: "secret"}).LogValue()
//	_ = strings.Contains(value.String(), "[REDACTED]") // true
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
