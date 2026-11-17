package iam

import (
	"net/http"
	"testing"
)

// TestRemoteClientIPIPv4 verifies the port is removed from an IPv4 peer.
func TestRemoteClientIPIPv4(t *testing.T) {
	t.Parallel()

	request := &http.Request{RemoteAddr: "192.0.2.10:1234"}
	if got := remoteClientIP(request); got != "192.0.2.10" {
		t.Fatalf("IPv4 peer = %q", got)
	}
}

// TestRemoteClientIPIPv6 verifies the complete IPv6 host is retained rather
// than shortened to the throttle bucket.
func TestRemoteClientIPIPv6(t *testing.T) {
	t.Parallel()

	request := &http.Request{RemoteAddr: "[2001:db8::1]:1234"}
	if got := remoteClientIP(request); got != "2001:db8::1" {
		t.Fatalf("IPv6 peer = %q", got)
	}
}

// TestRemoteClientIPMalformed verifies an address without a port is returned
// unchanged, matching the guard's default behavior.
func TestRemoteClientIPMalformed(t *testing.T) {
	t.Parallel()

	request := &http.Request{RemoteAddr: "malformed"}
	if got := remoteClientIP(request); got != "malformed" {
		t.Fatalf("malformed peer = %q", got)
	}
}
