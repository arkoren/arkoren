package clientip

import (
	"net/http"
	"net/netip"
	"strings"
)

// Cloudflare returns a resolver that accepts exactly one bare IP in
// CF-Connecting-IP only when the connection peer belongs to the embedded
// Cloudflare ranges. Otherwise it returns the direct peer, normalizing parsed
// addresses by unmapping IPv4 and removing IPv6 zones. It never falls back
// to another header. The snapshot can become stale and requires releases to
// update; an intervening proxy is not covered.
//
// Cloudflare's shared ranges do not authenticate your zone: protect the origin
// with zone-specific authenticated origin pulls or a Cloudflare Tunnel.
// Workers and Pseudo IPv4 settings can change the header's meaning.
//
// Example:
//
//	request, _ := http.NewRequest(http.MethodGet, "/", nil)
//	request.RemoteAddr = "173.245.48.1:443"
//	request.Header.Set("CF-Connecting-IP", "192.0.2.1")
//	fmt.Println(clientip.Cloudflare()(request)) // 192.0.2.1
func Cloudflare() func(*http.Request) string {
	// Snapshot retrieved 2026-11-17 from Cloudflare's official published lists:
	// https://www.cloudflare.com/ips-v4 and https://www.cloudflare.com/ips-v6.
	// Core maintainers must compare both lists and update this snapshot when
	// Cloudflare changes them; resolution never performs network I/O.
	ranges := []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22",
		"103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
		"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
		"198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32",
		"2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29",
		"2c0f:f248::/32",
	}
	trusted := make([]netip.Prefix, 0, len(ranges))
	for _, value := range ranges {
		prefix, err := netip.ParsePrefix(value)
		// An invalid embedded range must fail conservatively, not expand trust
		// or panic at construction. Snapshot tests check every listed range.
		if err == nil {
			trusted = append(trusted, prefix)
		}
	}

	return func(request *http.Request) string {
		host := Direct()(request)
		peer, err := netip.ParseAddr(host)
		if err != nil {
			return host
		}

		peer = peer.Unmap().WithZone("")
		values := request.Header.Values("CF-Connecting-IP")
		if !contains(trusted, peer) || len(values) != 1 {
			return peer.String()
		}

		address, err := netip.ParseAddr(strings.TrimSpace(values[0]))
		if err != nil {
			return peer.String()
		}

		return address.Unmap().WithZone("").String()
	}
}
