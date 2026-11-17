package clientip_test

import (
	"net/http/httptest"
	"net/netip"
	"testing"

	"arkoren.dev/core/http/clientip"
	"arkoren.dev/core/http/middleware/ratelimit"
)

// TestDirectPreservesHost verifies the auth-compatible host and fallback rules.
func TestDirectPreservesHost(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[fe80::1%en0]:123"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	if got := clientip.Direct()(request); got != "fe80::1%en0" {
		t.Fatal(got)
	}
	request.RemoteAddr = "not-an-address"
	if got := clientip.Direct()(request); got != "not-an-address" {
		t.Fatal(got)
	}
}

// TestProxiesWalksHeaders verifies right-to-left traversal, spoof resistance,
// malformed-hop fallback, normalization, and the independent trust snapshot.
func TestProxiesWalksHeaders(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), {}}
	resolve := clientip.Proxies(trusted...)
	trusted[0] = netip.MustParsePrefix("192.0.2.0/24")
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[::ffff:10.0.0.1]:80"
	request.Header.Add("X-Forwarded-For", "192.0.2.99")
	request.Header.Add("X-Forwarded-For", "::ffff:198.51.100.1, 10.0.0.2")
	if got := resolve(request); got != "198.51.100.1" {
		t.Fatal(got)
	}
	request.Header.Set("X-Forwarded-For", "192.0.2.1, invalid, 10.0.0.2")
	if got := resolve(request); got != "10.0.0.2" {
		t.Fatal(got)
	}
	request.RemoteAddr = "198.51.100.2:80"
	if got := resolve(request); got != "198.51.100.2" {
		t.Fatal(got)
	}
}

// TestProxiesFallbacks verifies empty, malformed and port-bearing chains never
// contribute attacker-controlled strings, and that zones are removed.
func TestProxiesFallbacks(t *testing.T) {
	resolve := clientip.Proxies(netip.MustParsePrefix("fe80::/10"))
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[fe80::1%en0]:80"
	if got := resolve(request); got != "fe80::1" {
		t.Fatal(got)
	}
	request.Header.Set("X-Forwarded-For", "192.0.2.1:443")
	if got := resolve(request); got != "fe80::1" {
		t.Fatal(got)
	}
	request.Header.Set("X-Forwarded-For", "fe80::2%en1, fe80::3")
	if got := resolve(request); got != "fe80::2" {
		t.Fatal(got)
	}
	request.RemoteAddr = "invalid"
	if got := resolve(request); got != "invalid" {
		t.Fatal(got)
	}
}

// TestProxiesRetainsIPv6 distinguishes client identity from rate-limit buckets.
func TestProxiesRetainsIPv6(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.1:80"
	request.Header.Set("X-Forwarded-For", "2001:db8::1234")
	resolve := clientip.Proxies(netip.MustParsePrefix("10.0.0.0/8"))
	if got := resolve(request); got != "2001:db8::1234" {
		t.Fatal(got)
	}
	if got := ratelimit.AddressKey(resolve(request)); got != "2001:db8::/64" {
		t.Fatal(got)
	}
}
