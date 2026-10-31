package client

import (
	"crypto/tls"
	"fmt"
	"net/http"
)

// NewTransport clones the current http.DefaultTransport and wraps it with
// Transport's retry and telemetry policy. The default must be a nonnil
// *http.Transport; any other value returns an error before callbacks run.
// Native proxy, connection pooling, and HTTP/2 settings are preserved.
//
// A nil config preserves the cloned default's TLS settings. A nonnil config
// replaces them with config.Clone(): the top-level configuration is independent,
// but native nested values (pools, certificates, callbacks, and their captures)
// remain shared. Configure those values before construction and do not mutate
// them while in use. Neither the supplied config nor the default is modified.
//
// Construction opens no connections. Callbacks run once, synchronously in order,
// through Transport; nil callbacks and application panics propagate. Use
// Transport directly when you already have a custom round tripper.
// Retire the owned connection pool with http.Client.CloseIdleConnections;
// active requests are not interrupted.
//
// Example:
//
//	roundTripper, err := client.NewTransport(&tls.Config{
//		MinVersion: tls.VersionTLS12,
//	}, func(transport *client.Builder) {
//		transport.Attempts(5)
//	})
//	if err != nil {
//		return err
//	}
//	httpClient := &http.Client{Transport: roundTripper}
//	_ = httpClient
func NewTransport(config *tls.Config, configure ...BuilderFunc) (http.RoundTripper, error) {
	base, ok := http.DefaultTransport.(*http.Transport)

	if !ok || base == nil {
		return nil, fmt.Errorf("new client transport: http.DefaultTransport must be a nonnil *http.Transport")
	}

	cloned := base.Clone()

	if config != nil {
		cloned.TLSClientConfig = config.Clone()
	}

	return &ownedTransport{RoundTripper: Transport(cloned, configure...), base: cloned}, nil
}
