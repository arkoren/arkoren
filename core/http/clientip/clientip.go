// Package clientip resolves the address associated with an HTTP request.
// Direct uses only the connection peer; Proxies and Cloudflare accept headers
// only from explicitly trusted peers. These resolvers return addresses, not
// rate-limit buckets, and do not authenticate a proxy's tenant or zone.
//
// For example, use clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
// when an isolated reverse proxy in that network appends X-Forwarded-For:
//
//	request, _ := http.NewRequest(http.MethodGet, "/", nil)
//	request.RemoteAddr = "10.0.0.1:443"
//	request.Header.Set("X-Forwarded-For", "192.0.2.1")
//	address := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))(request)
//	fmt.Println(address) // 192.0.2.1
//
// Trust only networks whose members cannot send forged forwarding headers.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Direct returns a resolver that extracts the host from RemoteAddr, or returns
// RemoteAddr unchanged when it is not a host:port pair. It ignores all headers
// and preserves the host's spelling and IPv6 zone, matching auth's default.
//
// Example:
//
//	request, _ := http.NewRequest(http.MethodGet, "/", nil)
//	request.RemoteAddr = "192.0.2.1:443"
//	fmt.Println(clientip.Direct()(request)) // 192.0.2.1
func Direct() func(*http.Request) string {
	return func(request *http.Request) string {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			return request.RemoteAddr
		}

		return host
	}
}

// Proxies returns a resolver for proxies that append X-Forwarded-For. It copies
// trusted and ignores invalid prefixes; no addresses are trusted implicitly.
// After checking the connection peer, it walks all header lines right to left,
// stopping at the first untrusted address or at the nearest trusted hop when
// an entry is malformed. IP literals only are accepted, not IP:port entries.
// Parsed addresses are unmapped and IPv6 zones removed; an unparseable peer
// is returned as Direct would return it. IPv6 addresses are not bucketed.
//
// Trust only isolated proxies that append or replace the header. In particular,
// trusting a network accessible to arbitrary clients permits spoofing.
//
// Example:
//
//	request, _ := http.NewRequest(http.MethodGet, "/", nil)
//	request.RemoteAddr = "10.0.0.1:443"
//	request.Header.Set("X-Forwarded-For", "192.0.2.1")
//	resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
//	fmt.Println(resolve(request)) // 192.0.2.1
func Proxies(trusted ...netip.Prefix) func(*http.Request) string {
	trusted = slices.Clone(trusted)

	return func(request *http.Request) string {
		host := Direct()(request)
		hop, err := netip.ParseAddr(host)
		if err != nil {
			return host
		}

		hop = hop.Unmap().WithZone("")
		if !contains(trusted, hop) {
			return hop.String()
		}

		chain := strings.Join(request.Header.Values("X-Forwarded-For"), ",")
		for _, entry := range slices.Backward(strings.Split(chain, ",")) {
			address, err := netip.ParseAddr(strings.TrimSpace(entry))
			if err != nil {
				break
			}

			hop = address.Unmap().WithZone("")
			if !contains(trusted, hop) {
				break
			}
		}

		return hop.String()
	}
}

// contains checks whether an address belongs to the caller's trust snapshot.
// Invalid prefixes do not contain any address. Addresses are already normalized.
func contains(trusted []netip.Prefix, address netip.Addr) bool {
	return slices.ContainsFunc(trusted, func(prefix netip.Prefix) bool {
		return prefix.Contains(address)
	})
}
