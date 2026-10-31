package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

// TestPublicAddressRejectsSpecialUse exercises classes that global-unicast
// alone does not reject, including mapped IPv4 and IPv6 transition schemes.
func TestPublicAddressRejectsSpecialUse(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"0.1.2.3", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "192.0.0.9", "192.0.2.1", "192.88.99.1", "192.168.0.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1",
		"255.255.255.255", "::", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1",
		"ff02::1", "64:ff9b::a00:1", "2001::1", "2001:db8::1", "2002:a00:1::1",
		"3fff::1", "2001:4860:4860::8888%en0",
	} {
		if publicAddress(netip.MustParseAddr(address)) {
			t.Fatal("accepted special-use address")
		}
	}
}

// TestDialRejectsEmptyDNS forbids dialing when an authoritative resolver
// returns no usable addresses, without inventing a fallback hostname dial.
func TestDialRejectsEmptyDNS(t *testing.T) {
	t.Parallel()

	dialer := publicDialer{
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("empty DNS answer dialed")

			return nil, nil
		},
	}

	if _, err := dialer.DialContext(t.Context(), "tcp", "fcm.googleapis.com:443"); err == nil {
		t.Fatal("empty DNS accepted")
	}
}

// TestDialSuccessfulFallback returns only a connection made to a checked
// public IPv6 address after the first public IPv4 address is unreachable.
func TestDialSuccessfulFallback(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})

	calls := 0
	dialer := publicDialer{
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2606:4700:4700::1111")}, nil
		},
		dial: func(_ context.Context, _ string, address string) (net.Conn, error) {
			calls++

			if address == "8.8.8.8:443" {
				return nil, errors.New("unreachable")
			}

			if address != "[2606:4700:4700::1111]:443" {
				t.Fatal("unchecked fallback")
			}

			return client, nil
		},
	}

	connection, err := dialer.DialContext(t.Context(), "tcp", "fcm.googleapis.com:443")

	if err != nil || connection != client || calls != 2 {
		t.Fatal("public fallback failed")
	}
}

// TestPublicAddressAcceptsGlobal checks ordinary public IPv4 and IPv6 remain
// usable while invalid zero netip values fail closed.
func TestPublicAddressAcceptsGlobal(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(address)) {
			t.Fatal("rejected public address")
		}
	}

	if publicAddress(netip.Addr{}) {
		t.Fatal("accepted invalid address")
	}
}

// TestDialRejectsMixedDNS prevents fallback to a private answer even when
// the DNS server returns a public address first.
func TestDialRejectsMixedDNS(t *testing.T) {
	t.Parallel()

	dialer := publicDialer{
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("mixed DNS result was dialed")

			return nil, nil
		},
	}

	if _, err := dialer.DialContext(t.Context(), "tcp", "fcm.googleapis.com:443"); err == nil {
		t.Fatal("accepted mixed DNS")
	}
}

// TestDialPinsValidatedAddresses proves there is no second hostname lookup
// when the validated DNS answer is passed to the actual dial operation.
func TestDialPinsValidatedAddresses(t *testing.T) {
	t.Parallel()

	lookups, dials := 0, 0
	native := errors.New("network failed")
	dialer := publicDialer{
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			lookups++

			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2606:4700:4700::1111")}, nil
		},
		dial: func(_ context.Context, _ string, address string) (net.Conn, error) {
			dials++
			host, _, err := net.SplitHostPort(address)

			if err != nil {
				t.Fatal(err)
			}

			if _, err := netip.ParseAddr(host); err != nil {
				t.Fatal("actual dial received a hostname")
			}

			return nil, native
		},
	}

	_, err := dialer.DialContext(t.Context(), "tcp", "fcm.googleapis.com:443")

	if !errors.Is(err, native) || lookups != 1 || dials != 2 {
		t.Fatal("fallback lost native error or performed extra lookup")
	}
}

// TestDialRechecksDNSPerConnection prevents an approved public result from
// being reused after the same hostname rebinds to a private address.
func TestDialRechecksDNSPerConnection(t *testing.T) {
	t.Parallel()

	lookups, dials := 0, 0
	dialer := publicDialer{
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			lookups++

			if lookups == 1 {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}

			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			dials++

			return nil, errors.New("unreachable")
		},
	}

	_, _ = dialer.DialContext(t.Context(), "tcp", "fcm.googleapis.com:443")
	_, _ = dialer.DialContext(t.Context(), "tcp", "fcm.googleapis.com:443")

	if lookups != 2 || dials != 1 {
		t.Fatal("DNS rebinding reached actual dial")
	}
}
