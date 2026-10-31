package cors

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// newPolicy validates config and precomputes its header values.
func newPolicy(config settings) policy {
	origins := make([]string, 0, len(config.AllowedOrigins))

	for _, origin := range config.AllowedOrigins {
		if origin == "*" && config.AllowCredentials {
			panic("cors: AllowCredentials must not be used with wildcard AllowedOrigins")
		}

		origins = append(origins, normalizeOrigin(origin))
	}

	policy := policy{
		origins:     origins,
		anyOrigin:   len(origins) == 1 && origins[0] == "*",
		credentials: config.AllowCredentials,
		methods:     strings.Join(config.AllowedMethods, ", "),
		headers:     strings.Join(config.AllowedHeaders, ", "),
		exposed:     strings.Join(config.ExposedHeaders, ", "),
	}

	if seconds := config.MaxAge / time.Second; seconds > 0 {
		policy.maxAge = strconv.FormatInt(int64(seconds), 10)
	}

	return policy
}

// normalizeOrigin lowercases an ASCII origin or panics for invalid structure.
// It accepts only a scheme/host/port tuple or the complete wildcard; credential
// compatibility is checked separately against the completed policy. Diagnostics
// omit the input because malformed origins can contain credentials.
func normalizeOrigin(origin string) string {
	if origin == "*" {
		return origin
	}

	parsed, err := url.Parse(origin)

	if err != nil {
		panic("cors: invalid allowed origin")
	}

	authority := parsed.Scheme != "" && parsed.Hostname() != ""
	bare := parsed.User == nil && parsed.Path == "" && !strings.ContainsAny(origin, "?#")

	if !authority || !bare || !validOriginHost(parsed) {
		panic("cors: invalid allowed origin")
	}

	return strings.ToLower(origin)
}

// validOriginHost excludes patterns and non-ASCII host spellings that a browser
// cannot serialize as an ASCII origin. URL parsing already checks authority
// syntax; numeric ports are additionally bounded to the native 16-bit range.
func validOriginHost(origin *url.URL) bool {
	host := origin.Hostname()

	if strings.ContainsAny(host, "*%") || strings.HasSuffix(origin.Host, ":") {
		return false
	}

	if strings.IndexFunc(host, func(character rune) bool {
		return character > unicode.MaxASCII
	}) >= 0 {
		return false
	}

	if port := origin.Port(); port != "" {
		_, err := strconv.ParseUint(port, 10, 16)

		return err == nil
	}

	return true
}
