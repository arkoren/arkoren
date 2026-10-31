package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// NewDialer resolves and validates every connection's complete DNS answer and
// dials only numeric addresses. The HTTP transport retains the original URL
// hostname for TLS verification. Construction performs no DNS or network I/O.
func NewDialer(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	public := publicDialer{lookup: net.DefaultResolver.LookupNetIP, dial: dialer.DialContext}

	return public.DialContext
}

// publicDialer resolves at the moment of dialing, then connects only to the
// checked numeric address. Its private seams test rebinding without loosening
// production endpoint policy or exposing arbitrary client injection.
type publicDialer struct {
	// lookup resolves the original DNS hostname once per connection.
	lookup func(context.Context, string, string) ([]netip.Addr, error)
	// dial connects directly to a validated numeric address.
	dial func(context.Context, string, string) (net.Conn, error)
}

// DialContext rejects mixed public/private DNS answers before trying any
// address. Fallback is limited to validated addresses and never repeats an
// HTTP request. The request context bounds DNS resolution and all attempts.
// address must use port 443. DNS/dial errors retain native identity; a successful
// connection belongs to the transport, which still validates the original TLS
// hostname. Construct the dialer with nonnil resolver and dial functions.
//
// Example:
//
//	dialer := publicDialer{
//		lookup: net.DefaultResolver.LookupNetIP,
//		dial:   (&net.Dialer{}).DialContext,
//	}
//	transport := &http.Transport{DialContext: dialer.DialContext}
//	_ = transport // No DNS lookup occurs until an actual request.
func (dialer publicDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)

	if err != nil || port != "443" {
		return nil, errors.New("web push dial requires port 443")
	}

	addresses, err := dialer.lookup(ctx, "ip", host)

	if err != nil {
		return nil, err
	}

	if err := validateAddresses(addresses); err != nil {
		return nil, err
	}

	last := errors.New("web push public address connection failed")

	for _, address := range addresses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		connection, err := dialer.dial(ctx, network, net.JoinHostPort(address.String(), port))

		if err == nil {
			return connection, nil
		}

		last = err
	}

	return nil, last
}

// validateAddresses makes the complete DNS answer a fail-closed boundary.
// Empty answers and a single nonpublic address both forbid all dialing.
func validateAddresses(addresses []netip.Addr) error {
	if len(addresses) == 0 {
		return errors.New("web push DNS answer is empty")
	}

	for _, address := range addresses {
		if !publicAddress(address) {
			return errors.New("web push DNS answer contains a forbidden address")
		}
	}

	return nil
}

// publicAddress excludes special-use ranges in addition to private and
// non-unicast addresses. IPv6 is restricted to current global allocation
// 2000::/3, minus documentation, transition and special-purpose ranges.
// Conservative rejection is preferable to reaching an internal service.
func publicAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}

	address = address.Unmap()

	if !address.IsGlobalUnicast() || address.IsPrivate() {
		return false
	}

	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}

	for _, prefix := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24",
		"192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24",
		"192.31.196.0/24", "192.52.193.0/24", "192.175.48.0/24", "2620:4f:8000::/48",
		"203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32",
		"2002::/16", "3fff::/20",
	} {
		if netip.MustParsePrefix(prefix).Contains(address) {
			return false
		}
	}

	return true
}
