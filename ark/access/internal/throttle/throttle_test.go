package throttle_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/ark/access/internal/throttle"
)

// TestBucket verifies IPv6 addresses share a key per /64 while IPv4 stays per
// address.
func TestBucket(t *testing.T) {
	t.Parallel()

	for client, want := range map[string]string{
		"203.0.113.9":                      "203.0.113.9",
		"::ffff:203.0.113.9":               "::ffff:203.0.113.9",
		"2001:db8:1:2:aaaa:bbbb:cccc:dddd": "2001:db8:1:2::/64",
		"2001:db8:1:2::1":                  "2001:db8:1:2::/64",
		"not an address":                   "not an address",
		"":                                 "",
	} {
		if got := throttle.Bucket(client); got != want {
			t.Errorf("Bucket(%q) = %q, want %q", client, got, want)
		}
	}
}

// TestRemoteHostDropsThePort verifies the default client address is the host
// part of RemoteAddr.
func TestRemoteHostDropsThePort(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "[2001:db8::1]:443"
	if got := throttle.RemoteHost(r); got != "2001:db8::1" {
		t.Fatalf("RemoteHost = %q, want 2001:db8::1", got)
	}
}

// TestRemoteHostWithoutPortIsWhole verifies a RemoteAddr without a port is
// used as it is.
func TestRemoteHostWithoutPortIsWhole(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9"
	if got := throttle.RemoteHost(r); got != "203.0.113.9" {
		t.Fatalf("RemoteHost = %q, want 203.0.113.9", got)
	}
}
