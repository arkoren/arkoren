// Package network owns endpoint authority policy and pinned public-address dialing.
package network

import (
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strings"
)

// parseEndpoint validates HTTPS authorities without changing opaque path or
// query bytes. Canonical DNS names avoid alternate spellings at the policy
// boundary; explicit :443 is accepted, other ports and IP literals are not.
func parseEndpoint(endpoint string) (*url.URL, error) {
	parsed, err := url.Parse(endpoint)

	if err != nil {
		return nil, errors.New("web push endpoint URI is invalid")
	}

	if parsed.Scheme != "https" || parsed.Opaque != "" {
		return nil, errors.New("web push endpoint requires https")
	}

	if parsed.User != nil || strings.Contains(endpoint, "#") {
		return nil, errors.New("web push endpoint credentials and fragments are forbidden")
	}

	if err := validateAuthority(parsed); err != nil {
		return nil, err
	}

	return parsed, nil
}

// validateAuthority permits only canonical DNS hostnames on the standard
// TLS port. DNS labels are ASCII and cannot encode an IP or zone identifier.
func validateAuthority(endpoint *url.URL) error {
	host := endpoint.Hostname()

	if endpoint.Host != host && endpoint.Host != host+":443" {
		return errors.New("web push endpoint requires port 443")
	}

	if host != strings.ToLower(host) || len(host) > 253 {
		return errors.New("web push endpoint hostname is not canonical")
	}

	if _, err := netip.ParseAddr(host); err == nil {
		return errors.New("web push endpoint IP literals are forbidden")
	}

	for label := range strings.SplitSeq(host, ".") {
		if !validLabel(label) {
			return errors.New("web push endpoint hostname is invalid")
		}
	}

	return nil
}

// validLabel recognizes conservative DNS label syntax. Numeric labels are
// allowed, but netip validation separately excludes ordinary IP literals.
func validLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}

	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}

	return strings.IndexFunc(label, func(character rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789-", character)
	}) == -1
}

// allowsOrigin applies either exact trusted overrides or the curated provider
// policy. Apple's domain family requires a leading label boundary, not an
// arbitrary string suffix. Explicit standard ports share the same origin.
func allowsOrigin(endpoint *url.URL, origins []string) bool {
	origin := "https://" + endpoint.Hostname()

	if origins != nil {
		return slices.Contains(origins, origin) || slices.Contains(origins, origin+":443")
	}

	host := endpoint.Hostname()

	return slices.Contains([]string{"fcm.googleapis.com", "updates.push.services.mozilla.com"}, host) ||
		strings.HasSuffix(host, ".push.apple.com")
}

// Endpoint validates a capability URL without changing its path or query, then
// reports whether its origin satisfies the selected policy. Policy errors never
// include the supplied URL. A nil origins slice selects curated providers.
func Endpoint(endpoint string, origins []string) (*url.URL, bool, error) {
	parsed, err := parseEndpoint(endpoint)

	if err != nil {
		return nil, false, err
	}

	return parsed, allowsOrigin(parsed, origins), nil
}

// ValidateOrigins checks trusted overrides once. Origins have canonical
// lowercase authorities and no trailing slash so matching is unambiguous.
func ValidateOrigins(origins []string) error {
	for _, origin := range origins {
		parsed, err := parseEndpoint(origin)

		if err != nil {
			return errors.New("web push allowed origin is invalid")
		}

		if origin != "https://"+parsed.Host {
			return errors.New("web push allowed origin must contain only scheme and authority")
		}
	}

	return nil
}
