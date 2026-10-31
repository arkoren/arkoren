package client

import "net/http"

// ownedTransport keeps the cloned connection pool reachable for the standard
// http.Client.CloseIdleConnections hook while RoundTrip uses retry telemetry.
type ownedTransport struct {
	// RoundTripper is the completed retry and telemetry composition.
	http.RoundTripper
	// base owns the native connection pool created by NewTransport.
	base *http.Transport
}

// CloseIdleConnections retires unused connections without interrupting active
// requests. It forwards directly to the owned pool, not through policy wrappers.
//
// Example:
//
//	// outgoing is the http.Client using NewTransport's result.
//	outgoing.CloseIdleConnections()
func (transport *ownedTransport) CloseIdleConnections() {
	transport.base.CloseIdleConnections()
}
