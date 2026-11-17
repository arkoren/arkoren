package clientip_test

import (
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/clientip"
)

// TestCloudflarePeerAndHeader verifies both address families, unmapping,
// direct-peer fallback, and rejection of duplicate and malformed headers.
func TestCloudflarePeerAndHeader(t *testing.T) {
	resolve := clientip.Cloudflare()
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "173.245.48.1:443"
	request.Header.Set("CF-Connecting-IP", "::ffff:192.0.2.1")
	if got := resolve(request); got != "192.0.2.1" {
		t.Fatal(got)
	}
	request.RemoteAddr = "[2606:4700::1]:443"
	request.Header.Set("CF-Connecting-IP", "2001:db8::1%eth0")
	if got := resolve(request); got != "2001:db8::1" {
		t.Fatal(got)
	}
	request.Header.Add("CF-Connecting-IP", "192.0.2.2")
	if got := resolve(request); got != "2606:4700::1" {
		t.Fatal(got)
	}
	request.Header.Set("CF-Connecting-IP", "192.0.2.1, 192.0.2.2")
	request.Header.Set("X-Forwarded-For", "192.0.2.3")
	if got := resolve(request); got != "2606:4700::1" {
		t.Fatal(got)
	}
	request.RemoteAddr = "198.51.100.1:443"
	request.Header.Set("CF-Connecting-IP", "192.0.2.1")
	if got := resolve(request); got != "198.51.100.1" {
		t.Fatal(got)
	}
}

// TestCloudflareSnapshot covers every published range and verifies that nearby
// untrusted addresses cannot make the resolver accept a forged header.
func TestCloudflareSnapshot(t *testing.T) {
	resolve := clientip.Cloudflare()
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("CF-Connecting-IP", "192.0.2.1")
	for _, peer := range []string{
		"173.245.48.1", "103.21.244.1", "103.22.200.1", "103.31.4.1",
		"141.101.64.1", "108.162.192.1", "190.93.240.1", "188.114.96.1",
		"197.234.240.1", "198.41.128.1", "162.158.0.1", "104.16.0.1",
		"104.24.0.1", "172.64.0.1", "131.0.72.1", "2400:cb00::1",
		"2606:4700::1", "2803:f800::1", "2405:b500::1", "2405:8100::1",
		"2a06:98c0::1", "2c0f:f248::1",
	} {
		request.RemoteAddr = peer
		if got := resolve(request); got != "192.0.2.1" {
			t.Fatalf("peer %s: %s", peer, got)
		}
	}
	request.RemoteAddr = "173.245.47.255:443"
	if got := resolve(request); got != "173.245.47.255" {
		t.Fatal(got)
	}
}
