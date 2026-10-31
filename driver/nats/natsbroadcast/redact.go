package natsbroadcast

import (
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
)

// redacted replaces nonempty secrets in Config's diagnostic copy. It also
// replaces complete URL entries when their authority cannot be identified.
const redacted = "[REDACTED]"

// plainConfig has the fields of [Config] but none of its methods, so
// formatting the redacted copy cannot reenter Config's fmt methods.
type plainConfig Config

// redact produces the method-free diagnostic copy used by Config's renderers.
// It detaches URLs before replacing userinfo or ambiguous entries, and replaces
// nonempty Password, Token, and NKeySeed fields. Empty secrets remain empty;
// native objects and other fields are retained, not recursively sanitized.
func (config Config) redact() plainConfig {
	config.URLs = slices.Clone(config.URLs)

	for index, value := range config.URLs {
		config.URLs[index] = redactURL(value)
	}

	if config.Password != "" {
		config.Password = redacted
	}

	if config.Token != "" {
		config.Token = redacted
	}

	if config.NKeySeed != "" {
		config.NKeySeed = redacted
	}

	return plainConfig(config)
}

// redactURL hides userinfo, including passwordless NATS tokens. Entries that
// cannot be parsed with a nonempty authority hostname are replaced wholesale:
// schemeless credentials can otherwise survive in opaque or path text. This
// diagnostic policy does not normalize native connection inputs. Comma-separated
// server entries are handled independently; parse errors are never printed.
func redactURL(value string) string {
	values := strings.Split(value, ",")

	for index, value := range values {
		parsed, err := url.Parse(value)

		if err != nil || parsed.Hostname() == "" {
			values[index] = redacted
			continue
		}

		if parsed.User != nil {
			parsed.User = url.User(redacted)
		}

		values[index] = parsed.String()
	}

	return strings.Join(values, ",")
}

// Format implements [fmt.Formatter] so numeric, boolean, and unknown verbs
// cannot bypass redaction through fmt's ordinary field-by-field fallback. Value
// verbs retain String/GoString output; other verbs format the redacted string,
// preserving width, precision, and string quoting/encoding. It never changes
// the record. Go bypasses this method for %T, %p, and invalid %w; see [Config]
// for the resulting limits and the safe pattern for externally chosen formats.
//
// Example:
//
//	record := natsbroadcast.Config{Token: "example"}
//	text := fmt.Sprintf("%d", record) // A format error, but no token disclosure.
//	fmt.Println(text)
func (config Config) Format(state fmt.State, verb rune) {
	var text string

	if verb == 'v' && state.Flag('#') {
		text = config.GoString()
	} else {
		text = config.String()
	}

	if verb == 'v' {
		verb = 's'
	}

	// fmt.Formatter has no error result; fmt owns the destination's write errors.
	_, _ = fmt.Fprintf(state, fmt.FormatString(state, verb), text)
}

// String formats config with its secrets replaced by "[REDACTED]" when set,
// so %v and %+v never print them.
// URL userinfo is hidden, and formatting never mutates the record's URL slice.
//
// Example:
//
//	record := natsbroadcast.Config{Token: "example"}
//	safe := record.String()
//	fmt.Println(safe)
func (config Config) String() string {
	return fmt.Sprintf("%+v", config.redact())
}

// GoString formats config like [Config.String] for %#v.
// It also hides URL userinfo; the diagnostic representation is not serialization.
//
// Example:
//
//	record := natsbroadcast.Config{Token: "example"}
//	fmt.Printf("%#v\n", record)
func (config Config) GoString() string {
	return fmt.Sprintf("%#v", config.redact())
}

// LogValue implements [slog.LogValuer] with the redacted form of config.
// The record becomes a diagnostic string, not structured credential fields.
//
// Example:
//
//	record := natsbroadcast.Config{Token: "example"}
//	slog.Info("NATS settings", "config", record)
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
