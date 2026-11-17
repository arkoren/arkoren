package clientip_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"testing"

	"arkoren.dev/core/http/clientip"
)

// ExampleCloudflare resolves the client only for a Cloudflare connection peer.
func ExampleCloudflare() {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "173.245.48.1:443"
	request.Header.Set("CF-Connecting-IP", "192.0.2.1")
	fmt.Println(clientip.Cloudflare()(request))
	// Output: 192.0.2.1
}

// TestCloudflareValidHeader verifies an IPv4 peer and mapped client address.
func TestCloudflareValidHeader(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "173.245.48.1:443"
	request.Header.Set("CF-Connecting-IP", "::ffff:192.0.2.1")
	if got := clientip.Cloudflare()(request); got != "192.0.2.1" {
		t.Fatalf("got %q, want unmapped client", got)
	}
}

// TestCloudflareIPv6Peer verifies IPv6 peer trust and client-zone removal.
func TestCloudflareIPv6Peer(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[2606:4700::1]:443"
	request.Header.Set("CF-Connecting-IP", "2001:db8::1%eth0")
	if got := clientip.Cloudflare()(request); got != "2001:db8::1" {
		t.Fatalf("got %q, want unzoned client", got)
	}
}

// TestCloudflareMappedPeer verifies that a mapped Cloudflare peer is trusted.
func TestCloudflareMappedPeer(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[::ffff:173.245.48.1]:443"
	request.Header.Set("CF-Connecting-IP", "192.0.2.1")
	if got := clientip.Cloudflare()(request); got != "192.0.2.1" {
		t.Fatalf("got %q, want forwarded client", got)
	}
}

// TestCloudflareUntrustedIPv6 ignores a forged header from an outside peer.
func TestCloudflareUntrustedIPv6(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[2001:db8::1]:443"
	request.Header.Set("CF-Connecting-IP", "192.0.2.1")
	if got := clientip.Cloudflare()(request); got != "2001:db8::1" {
		t.Fatalf("got %q, want untrusted peer", got)
	}
}

// TestCloudflareInvalidPeer preserves the direct fallback without trusting data.
func TestCloudflareInvalidPeer(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "not-a-peer"
	request.Header.Set("CF-Connecting-IP", "192.0.2.1")
	if got := clientip.Cloudflare()(request); got != "not-a-peer" {
		t.Fatalf("got %q, want direct fallback", got)
	}
}

// TestCloudflareMissingHeader falls back to the peer and ignores XFF.
func TestCloudflareMissingHeader(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "173.245.48.1:443"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	if got := clientip.Cloudflare()(request); got != "173.245.48.1" {
		t.Fatalf("got %q, want peer", got)
	}
}

// TestCloudflareInvalidHeader rejects empty, malformed and non-bare values.
func TestCloudflareInvalidHeader(t *testing.T) {
	for _, value := range []string{
		"", "invalid", "192.0.2.1:443", "[2001:db8::1]:443",
		"[2001:db8::1]", "192.0.2.1, 192.0.2.2",
	} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = "173.245.48.1:443"
		request.Header.Set("CF-Connecting-IP", value)
		request.Header.Set("X-Forwarded-For", "192.0.2.3")
		if got := clientip.Cloudflare()(request); got != "173.245.48.1" {
			t.Fatalf("header %q: got %q, want peer", value, got)
		}
	}
}

// TestCloudflareDuplicateHeader rejects multiple header lines even if valid.
func TestCloudflareDuplicateHeader(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "173.245.48.1:443"
	request.Header.Add("CF-Connecting-IP", "192.0.2.1")
	request.Header.Add("CF-Connecting-IP", "192.0.2.2")
	if got := clientip.Cloudflare()(request); got != "173.245.48.1" {
		t.Fatalf("got %q, want peer", got)
	}
}

// TestCloudflareSnapshot checks both endpoints of every official range and
// neighboring addresses against the complete published snapshot.
func TestCloudflareSnapshot(t *testing.T) {
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
	prefixes := make([]netip.Prefix, 0, len(ranges))
	for _, value := range ranges {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	resolve := clientip.Cloudflare()
	for _, prefix := range prefixes {
		checkCloudflareBoundary(t, resolve, prefix, prefixes)
	}
}

// checkCloudflareBoundary compares each end and its neighbor with the complete
// snapshot; adjacent official prefixes can make a neighbor trusted too.
func checkCloudflareBoundary(
	t *testing.T,
	resolve func(*http.Request) string,
	prefix netip.Prefix,
	prefixes []netip.Prefix,
) {
	t.Helper()
	first := prefix.Masked().Addr()
	last := lastAddress(t, prefix)
	for _, peer := range []netip.Addr{first.Prev(), first, last, last.Next()} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = peer.String()
		request.Header.Set("CF-Connecting-IP", "192.0.2.1")
		want := peer.String()
		if slices.ContainsFunc(prefixes, func(allowed netip.Prefix) bool {
			return allowed.Contains(peer)
		}) {
			want = "192.0.2.1"
		}
		if got := resolve(request); got != want {
			t.Fatalf("range %s, peer %s: got %q, want %q", prefix, peer, got, want)
		}
	}
}

// lastAddress computes the inclusive end of a masked IPv4 or IPv6 prefix.
// It preserves the input address family so neighboring addresses stay comparable.
func lastAddress(t *testing.T, prefix netip.Prefix) netip.Addr {
	t.Helper()
	address := prefix.Masked().Addr()
	bytes := address.AsSlice()
	for bit := prefix.Bits(); bit < len(bytes)*8; bit++ {
		bytes[bit/8] |= 1 << (7 - bit%8)
	}
	last, ok := netip.AddrFromSlice(bytes)
	if !ok {
		t.Fatal("invalid address length from netip")
	}
	return last
}
