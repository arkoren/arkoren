package webpushpush

import (
	"net/http"
	"time"

	"arkoren.dev/driver/webpush/webpushpush/internal/network"
)

// responseHeaderLimit bounds provider-controlled headers independently of
// the small plaintext budget and prevents unbounded HTTP parser allocation.
const responseHeaderLimit = 16 << 10

// httpDoer is the library's request boundary. Production always uses an owned
// hardened client; package tests substitute a deterministic provider here.
type httpDoer interface {
	// Do performs one prepared HTTP exchange. Production clients reject
	// redirects and have a finite timeout covering all response handling.
	Do(*http.Request) (*http.Response, error)
}

// newClient owns all transport policy: no proxy, redirect, HTTP/2 retry or
// pooled connection replay. TLS continues to verify the original DNS host,
// since only DialContext substitutes the checked address, not the URL.
func newClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: network.NewDialer(timeout), DisableKeepAlives: true,
			DisableCompression: true, TLSHandshakeTimeout: timeout,
			ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: responseHeaderLimit,
		},
	}
}
