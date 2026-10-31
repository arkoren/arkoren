package client

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
	"time"
)

// TestNewTransportNativeSettings verifies native cloning rather than rebuilding
// a partial transport. It is nonparallel because the default is process-wide.
func TestNewTransportNativeSettings(t *testing.T) {
	original := http.DefaultTransport
	defaults := &tls.Config{ServerName: "default.example"}
	base := &http.Transport{
		TLSClientConfig: defaults, MaxIdleConns: 27, MaxIdleConnsPerHost: 11,
		IdleConnTimeout: 37 * time.Second, ForceAttemptHTTP2: true,
	}
	http.DefaultTransport = base
	t.Cleanup(func() {
		http.DefaultTransport = original
	})

	config := &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "private.example"}

	roundTripper, err := NewTransport(config)

	if err != nil {
		t.Fatal(err)
	}

	cloned := roundTripper.(*ownedTransport).base

	if cloned == base || cloned.TLSClientConfig == config {
		t.Fatal("transport or TLS configuration not cloned")
	}

	if cloned.MaxIdleConns != 27 || cloned.MaxIdleConnsPerHost != 11 {
		t.Fatal("connection pool settings discarded")
	}

	if cloned.IdleConnTimeout != 37*time.Second || !cloned.ForceAttemptHTTP2 {
		t.Fatal("idle timeout or HTTP/2 settings discarded")
	}
}

// TestNewTransportTLSOwnership checks top-level isolation without imposing deep
// ownership on native pools. The nonparallel default replacement is restored.
func TestNewTransportTLSOwnership(t *testing.T) {
	original := http.DefaultTransport
	defaults := &tls.Config{ServerName: "default.example"}
	base := &http.Transport{TLSClientConfig: defaults}
	http.DefaultTransport = base
	t.Cleanup(func() {
		http.DefaultTransport = original
	})

	config := &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "private.example"}

	roundTripper, err := NewTransport(config)

	if err != nil {
		t.Fatal(err)
	}

	cloned := roundTripper.(*ownedTransport).base

	if cloned.TLSClientConfig.RootCAs != config.RootCAs {
		t.Fatal("native TLS internals unexpectedly deep copied")
	}

	if base.TLSClientConfig != defaults || defaults.ServerName != "default.example" {
		t.Fatal("default TLS configuration mutated")
	}

	if config.ServerName != "private.example" {
		t.Fatal("caller TLS configuration mutated")
	}
}
