// Package protocol owns Stripe resource translation and one-attempt transport policy.
package protocol

import (
	"io"
	"net/http"
	"strings"
)

// mutationTransport prevents net/http from replaying Stripe mutations internally,
// independently of SDK retries. Stripe supplies GetBody and Idempotency-Key,
// which otherwise make a POST replayable after a reused connection loses a reply.
// Only New installs this boundary; NewFrom must preserve native client policies.
type mutationTransport struct {
	// next is the caller-owned transport, or nil for http.DefaultTransport.
	// Its lifecycle and any independent, custom retry policy remain caller-owned.
	next http.RoundTripper
}

// NewTransport prevents standard HTTP mutation replays without changing the
// caller-owned transport or its lifecycle. A nil next uses the native default.
func NewTransport(next http.RoundTripper) http.RoundTripper {
	return mutationTransport{next: next}
}

// RoundTrip leaves reads unchanged and delegates mutations with no rewind hook.
// It clones the request before changing it, retaining the original body bytes and
// idempotency headers. Empty bodies use a non-nil, unknown-length empty stream:
// nil/NoBody remains replayable in net/http even with GetBody removed. No payload
// bytes are invented; HTTP/1 can use chunked framing for an originally empty body.
// Custom transports must not independently replay a request ignoring this policy.
//
// Example:
//
//	client := &http.Client{Transport: mutationTransport{next: http.DefaultTransport}}
//	_ = client // installed on New's private client copy, never a borrowed client
func (mutationTransport mutationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	next := mutationTransport.next

	if next == nil {
		next = http.DefaultTransport
	}

	switch request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return next.RoundTrip(request)
	}

	outbound := request.Clone(request.Context())
	outbound.GetBody = nil

	if outbound.Body == nil || outbound.Body == http.NoBody {
		outbound.Body = io.NopCloser(strings.NewReader(""))
		outbound.ContentLength = -1
	}

	return next.RoundTrip(outbound)
}
