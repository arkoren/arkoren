package httptrack

import (
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Method bounds method cardinality without retaining arbitrary request text.
func Method(method string) string {
	switch method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return method
	default:
		return "_OTHER"
	}
}

// Route returns the matched route template without its method prefix.
func Route(request *http.Request) string {
	if _, route, ok := strings.Cut(request.Pattern, " "); ok {
		return route
	}

	return request.Pattern
}

// SpanName names unknown methods HTTP and omits absent routes, never replacing
// a missing template with an unbounded request path.
func SpanName(method, route string) string {
	if method == "_OTHER" {
		method = "HTTP"
	}

	if route == "" {
		return method
	}

	return method + " " + route
}

// Scheme reports the incoming transport scheme, ignoring forwarded headers.
func Scheme(request *http.Request) string {
	if request.TLS != nil {
		return "https"
	}

	return "http"
}

// ProtocolVersion removes an HTTP/ prefix when present.
func ProtocolVersion(protocol string) string {
	if version, ok := strings.CutPrefix(protocol, "HTTP/"); ok {
		return version
	}

	return protocol
}

// Port returns the explicit URL port, the scheme default, or zero if unknown.
func Port(request *http.Request) int {
	if port, err := strconv.Atoi(request.URL.Port()); err == nil {
		return port
	}

	switch request.URL.Scheme {
	case "https":
		return 443
	case "http":
		return 80
	}

	return 0
}

// RemoteIP parses only the peer address, never caller-controlled forwarding
// headers. IPv4-mapped addresses are unmapped and zones are removed.
func RemoteIP(request *http.Request) (netip.Addr, bool) {
	if addrPort, err := netip.ParseAddrPort(request.RemoteAddr); err == nil {
		return addrPort.Addr().Unmap().WithZone(""), true
	}

	addr, err := netip.ParseAddr(request.RemoteAddr)

	if err != nil {
		return netip.Addr{}, false
	}

	return addr.Unmap().WithZone(""), true
}

// NormalizeHost lowercases a host and drops one trailing root dot.
func NormalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// InternalDestination classifies a normalized host without DNS resolution.
// Private literals, single-label names and explicit internal suffixes qualify.
func InternalDestination(host string) bool {
	if host == "" {
		return false
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap().WithZone("")

		return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
	}

	return !strings.Contains(host, ".") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".cluster.local")
}

// MatchHost matches normalized hosts to lowercased exact or wildcard patterns.
// A wildcard requires a subdomain; it never admits the bare domain.
func MatchHost(host string, patterns []string) bool {
	return slices.ContainsFunc(patterns, func(pattern string) bool {
		if domain, ok := strings.CutPrefix(pattern, "*."); ok {
			return strings.HasSuffix(host, "."+domain)
		}

		return host == pattern
	})
}
