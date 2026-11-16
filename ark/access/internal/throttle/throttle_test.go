package throttle_test

import (
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
