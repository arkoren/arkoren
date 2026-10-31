package resource

import (
	"net/url"
	"strings"
)

// ValidHTTPS rejects user info and fragments in URLs accepted as callback or
// approval destinations. Query strings are preserved and must never be logged.
func ValidHTTPS(value string) bool {
	destination, err := url.Parse(value)

	if err != nil {
		return false
	}

	validOrigin := destination.Scheme == "https" && destination.Hostname() != ""
	clean := destination.User == nil && destination.Fragment == ""

	return validOrigin && clean
}

// ValidToken bounds native identifier and credential suffixes to ASCII
// alphanumerics. Separators and escapes cannot enter API paths through IDs.
func ValidToken(value string) bool {
	return len(value) > 0 && len(value) <= 100 &&
		strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") == ""
}
