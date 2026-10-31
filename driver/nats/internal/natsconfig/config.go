// Package natsconfig validates native connection and routing syntax without I/O.
// It preserves valid native spelling rather than normalizing deployed subjects.
//
// Example:
//
//	if err := natsconfig.URLs([]string{"localhost:4222"}); err != nil {
//		return err
//	}
package natsconfig

import (
	"errors"
	"net/url"
	"strings"
)

// maxAssetNameLength is the native server's stream/durable name limit in bytes.
// Keeping it here makes both constructors reject overlong identifiers before I/O.
const maxAssetNameLength = 255

// URLs validates server entries and comma-separated lists accepted by native
// Connect. Empty slices select native defaults; empty entries, absent hosts and
// unsupported schemes fail. Errors omit input because URLs can carry secrets.
//
// Example:
//
//	if err := natsconfig.URLs([]string{"tls://broker:4222"}); err != nil {
//		return err
//	}
func URLs(values []string) error {
	if len(values) == 0 {
		return nil
	}

	for value := range strings.SplitSeq(strings.Join(values, ","), ",") {
		if !validURL(value) {
			return errors.New("invalid URLs: expected NATS server host and supported scheme")
		}
	}

	return nil
}

// validURL accepts native schemeless hosts and the four native transport schemes.
// Parse failures are reduced to false so url.Error cannot reveal credentials.
func validURL(value string) bool {
	value = strings.TrimSpace(value)

	if !strings.Contains(value, "://") {
		value = "nats://" + value
	}

	parsed, err := url.Parse(value)

	if err != nil {
		return false
	}

	if parsed.Hostname() == "" {
		return false
	}

	switch parsed.Scheme {
	case "nats", "tls", "ws", "wss":
		return true
	}

	return false
}

// Subject reports whether value is a nonempty literal NATS publication subject.
// Native uppercase letters and underscores remain valid; wildcard and empty
// tokens are rejected because these destinations must also accept publication.
//
// Example:
//
//	if !natsconfig.Subject("jobs.email") {
//		return errors.New("invalid job subject")
//	}
func Subject(value string) bool {
	if strings.ContainsAny(value, "*> \t\r\n") {
		return false
	}

	for token := range strings.SplitSeq(value, ".") {
		if token == "" {
			return false
		}
	}

	return true
}

// Name reports whether a stream or durable name fits native asset-name syntax.
// These names cannot contain subject separators, wildcards, path separators or
// whitespace, and are limited to the server's 255-byte maximum.
//
// Example:
//
//	if !natsconfig.Name("EMAILS") {
//		return errors.New("invalid stream name")
//	}
func Name(value string) bool {
	return value != "" && len(value) <= maxAssetNameLength && !strings.ContainsAny(value, ".*>/\\ \t\r\n")
}
