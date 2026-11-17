package clientip_test

import (
	"fmt"
	"net/http/httptest"
	"net/netip"
	"testing"

	"arkoren.dev/core/http/clientip"
	"arkoren.dev/core/http/middleware/ratelimit"
)

// ExampleDirect shows that the connection host, not a forwarding header, wins.
func ExampleDirect() {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.1:443"
	fmt.Println(clientip.Direct()(request))
	// Output: 192.0.2.1
}

// ExampleProxies resolves a client through an explicitly trusted proxy.
func ExampleProxies() {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.1:443"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
	fmt.Println(resolve(request))
	// Output: 192.0.2.1
}

// TestDirectHost verifies host extraction and unchanged fallback representations.
func TestDirectHost(t *testing.T) {
	for _, pair := range [][2]string{
		{"192.0.2.1:443", "192.0.2.1"},
		{"2001:db8::1", "2001:db8::1"},
		{"", ""},
		{"not-an-address", "not-an-address"},
		{"[::ffff:192.0.2.1]:443", "::ffff:192.0.2.1"},
		{"[fe80::1%en0]:443", "fe80::1%en0"},
	} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = pair[0]
		if got := clientip.Direct()(request); got != pair[1] {
			t.Fatalf("RemoteAddr %q: got %q, want %q", pair[0], got, pair[1])
		}
	}
}

// TestDirectIgnoresHeaders confirms forwarding headers never alter the peer.
func TestDirectIgnoresHeaders(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.1:443"
	request.Header.Set("X-Forwarded-For", "198.51.100.1")
	request.Header.Set("CF-Connecting-IP", "198.51.100.2")
	if got := clientip.Direct()(request); got != "192.0.2.1" {
		t.Fatalf("got %q, want direct peer", got)
	}
}

// TestProxiesNoPrefixes verifies that no networks are trusted by default.
func TestProxiesNoPrefixes(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	if got := clientip.Proxies()(request); got != "127.0.0.1" {
		t.Fatalf("got %q, want untrusted peer", got)
	}
}

// TestProxiesInvalidPrefix confirms a zero prefix cannot grant trust.
func TestProxiesInvalidPrefix(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.1:443"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	if got := clientip.Proxies(netip.Prefix{})(request); got != "10.0.0.1" {
		t.Fatalf("got %q, want untrusted peer", got)
	}
}

// TestProxiesMappedPrefix confirms normalized addresses require IPv4 prefixes.
func TestProxiesMappedPrefix(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[::ffff:10.0.0.1]:443"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	resolve := clientip.Proxies(netip.MustParsePrefix("::ffff:10.0.0.0/104"))
	if got := resolve(request); got != "10.0.0.1" {
		t.Fatalf("got %q, want unmapped untrusted peer", got)
	}
}

// TestProxiesSnapshot confirms later prefix mutations cannot change trust.
func TestProxiesSnapshot(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	resolve := clientip.Proxies(trusted...)
	trusted[0] = netip.MustParsePrefix("192.0.2.0/24")
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.1:443"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	if got := resolve(request); got != "192.0.2.1" {
		t.Fatalf("got %q, want forwarded client", got)
	}
}

// TestProxiesUntrustedPeer verifies a client cannot spoof the forwarded chain.
func TestProxiesUntrustedPeer(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "198.51.100.1:443"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
	if got := resolve(request); got != "198.51.100.1" {
		t.Fatalf("got %q, want untrusted peer", got)
	}
}

// TestProxiesTrustedWithoutHeader returns the trusted peer when no chain exists.
func TestProxiesTrustedWithoutHeader(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.1:443"
	resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
	if got := resolve(request); got != "10.0.0.1" {
		t.Fatalf("got %q, want peer", got)
	}
}

// TestProxiesAllTrusted returns the leftmost reported address when all match.
func TestProxiesAllTrusted(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.1:443"
	request.Header.Set("X-Forwarded-For", "10.0.0.3, 10.0.0.2")
	resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
	if got := resolve(request); got != "10.0.0.3" {
		t.Fatalf("got %q, want leftmost trusted hop", got)
	}
}

// TestProxiesMultipleLines stops at the first untrusted hop from the right.
func TestProxiesMultipleLines(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[::ffff:10.0.0.1]:443"
	request.Header.Add("X-Forwarded-For", "192.0.2.99")
	request.Header.Add("X-Forwarded-For", "::ffff:198.51.100.1, 10.0.0.2")
	resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
	if got := resolve(request); got != "198.51.100.1" {
		t.Fatalf("got %q, want first untrusted hop", got)
	}
}

// TestProxiesMalformedChain returns the nearest trusted reporting hop.
func TestProxiesMalformedChain(t *testing.T) {
	for _, entry := range []string{"invalid", "", "192.0.2.1:443", "[2001:db8::1]:443"} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = "10.0.0.1:443"
		request.Header.Set("X-Forwarded-For", "192.0.2.1, "+entry+", 10.0.0.2")
		resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
		if got := resolve(request); got != "10.0.0.2" {
			t.Fatalf("entry %q: got %q, want nearest trusted hop", entry, got)
		}
	}
}

// TestProxiesTrustedIPv6 removes zones from parsed peer and forwarded hops.
func TestProxiesTrustedIPv6(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[fe80::1%en0]:443"
	request.Header.Set("X-Forwarded-For", "2001:db8::1%en1, fe80::2%en0")
	resolve := clientip.Proxies(netip.MustParsePrefix("fe80::/10"))
	if got := resolve(request); got != "2001:db8::1" {
		t.Fatalf("got %q, want unzoned client", got)
	}
}

// TestProxiesInvalidPeer preserves the direct fallback without parsing headers.
func TestProxiesInvalidPeer(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "invalid"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	if got := clientip.Proxies()(request); got != "invalid" {
		t.Fatalf("got %q, want invalid direct peer", got)
	}
}

// TestProxiesIPv6IsNotBucketed distinguishes identity from rate-limit keys.
func TestProxiesIPv6IsNotBucketed(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.1:443"
	request.Header.Set("X-Forwarded-For", "2001:db8::1234")
	resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
	if got := resolve(request); got != "2001:db8::1234" {
		t.Fatalf("got %q, want complete address", got)
	}
	if got := ratelimit.AddressKey(resolve(request)); got != "2001:db8::/64" {
		t.Fatalf("got %q, want rate-limit bucket", got)
	}
}
