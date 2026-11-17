package iam

import (
	"net/http"
	"testing"
)

// TestRemoteClientIP verifies that the default ignores forwarded headers and
// retains malformed peer addresses, matching the core guard's default.
func TestRemoteClientIP(t *testing.T) {
	t.Parallel()

	request := &http.Request{RemoteAddr: "[2001:db8::1]:1234", Header: http.Header{"X-Forwarded-For": {"192.0.2.1"}}}
	if got := remoteClientIP(request); got != "2001:db8::1" {
		t.Fatalf("IPv6 peer = %q", got)
	}
	request.RemoteAddr = "malformed"
	if got := remoteClientIP(request); got != "malformed" {
		t.Fatalf("malformed peer = %q", got)
	}
}
