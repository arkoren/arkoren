package client_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/http/client"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// ExampleNewTransport configures TLS without changing the shared default pool.
func ExampleNewTransport() {
	transport, err := client.NewTransport(&tls.Config{MinVersion: tls.VersionTLS12},
		func(transport *client.Builder) {
			transport.Attempts(5)
		},
	)

	if err != nil {
		fmt.Println(err)

		return
	}

	outgoing := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	outgoing.CloseIdleConnections()
	fmt.Println("configured")
	// Output: configured
}

// TestNewTransportVerifiedHTTPS exercises certificate verification and retries
// through the same telemetry composition as Transport, without real waits.
func TestNewTransportVerifiedHTTPS(t *testing.T) {
	t.Parallel()

	requests := atomic.Int32{}
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(endpoint.Close)

	pool := x509.NewCertPool()
	pool.AddCert(endpoint.Certificate())

	config := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	instance := telemetrytest.New().Telemetry(t)
	policies, hooks := 0, 0

	transport, err := client.NewTransport(config, func(transport *client.Builder) {
		policies++
		transport.Attempts(2)
		transport.Telemetry(instance)
		transport.Backoff(func(int) time.Duration {
			return 0
		})
		transport.Propagate(func(*http.Request) bool {
			hooks++

			return false
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	config.ServerName = "mutated.invalid"
	t.Cleanup((&http.Client{Transport: transport}).CloseIdleConnections)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK || requests.Load() != 2 {
		t.Fatalf("status = %d, requests = %d", response.StatusCode, requests.Load())
	}

	if policies != 1 || hooks != 2 {
		t.Fatalf("policy calls = %d, propagation calls = %d", policies, hooks)
	}
}

// TestNewTransportMutualTLS presents a client-purpose identity to a server that
// requires native certificate verification, while still verifying the server.
func TestNewTransportMutualTLS(t *testing.T) {
	t.Parallel()

	pair := clientIdentity(t)
	clients := x509.NewCertPool()
	clients.AddCert(pair.Leaf)

	endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if len(req.TLS.VerifiedChains) == 0 {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))

	endpoint.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clients}
	endpoint.StartTLS()
	t.Cleanup(endpoint.Close)

	pool := x509.NewCertPool()
	pool.AddCert(endpoint.Certificate())

	transport, err := client.NewTransport(&tls.Config{
		RootCAs: pool, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12,
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup((&http.Client{Transport: transport}).CloseIdleConnections)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

// clientIdentity issues an ephemeral self-signed client trust anchor solely for
// a local mTLS handshake. No files, system stores, or external issuers are used.
func clientIdentity(t *testing.T) tls.Certificate {
	t.Helper()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, private.Public(), private)

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := x509.ParseCertificate(der)

	if err != nil {
		t.Fatal(err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: private, Leaf: leaf}
}

// TestNewTransportPreservesDefaultTLS uses a deliberately replaced native
// default. It is nonparallel because http.DefaultTransport is process-wide.
func TestNewTransportPreservesDefaultTLS(t *testing.T) {
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(endpoint.Close)

	pool := x509.NewCertPool()
	pool.AddCert(endpoint.Certificate())

	config := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	proxies := 0
	base := &http.Transport{
		TLSClientConfig: config, MaxIdleConns: 27, ForceAttemptHTTP2: true,
		Proxy: func(*http.Request) (*url.URL, error) {
			proxies++

			return nil, nil
		},
	}

	original := http.DefaultTransport
	http.DefaultTransport = base
	t.Cleanup(func() {
		http.DefaultTransport = original
		base.CloseIdleConnections()
	})

	transport, err := client.NewTransport(nil)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup((&http.Client{Transport: transport}).CloseIdleConnections)

	if base.TLSClientConfig != config || config.ServerName != "" || base.MaxIdleConns != 27 {
		t.Fatal("construction mutated default transport or TLS configuration")
	}

	config.ServerName = "mutated.invalid"

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK || proxies != 1 {
		t.Fatalf("status = %d, proxy calls = %d", response.StatusCode, proxies)
	}
}

// TestNewTransportNilDefault rejects a missing default before executing user
// callbacks. Process-wide replacement is confined to a nonparallel test.
func TestNewTransportNilDefault(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = nil
	t.Cleanup(func() {
		http.DefaultTransport = original
	})

	transport, err := client.NewTransport(nil, func(*client.Builder) {
		t.Fatal("callback executed with invalid default")
	})

	if err == nil || transport != nil {
		t.Fatalf("transport = %v, error = %v", transport, err)
	}
}

// TestNewTransportTypedNilDefault rejects a typed nil without invoking Clone,
// which would panic. This process-wide replacement must not run in parallel.
func TestNewTransportTypedNilDefault(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = (*http.Transport)(nil)
	t.Cleanup(func() {
		http.DefaultTransport = original
	})

	transport, err := client.NewTransport(nil)

	if err == nil || transport != nil {
		t.Fatalf("transport = %v, error = %v", transport, err)
	}
}

// TestNewTransportCustomDefault requires callers with arbitrary round trippers
// to use Transport rather than silently replacing their custom implementation.
func TestNewTransportCustomDefault(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("custom default called during construction")

		return nil, nil
	})
	t.Cleanup(func() {
		http.DefaultTransport = original
	})

	transport, err := client.NewTransport(nil)

	if err == nil || transport != nil {
		t.Fatalf("transport = %v, error = %v", transport, err)
	}
}

// TestNewTransportCloseIdleConnections checks the standard http.Client cleanup
// hook against a live owned pool, rather than merely checking method presence.
func TestNewTransportCloseIdleConnections(t *testing.T) {
	t.Parallel()

	closed := make(chan struct{}, 1)
	endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	endpoint.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}

	endpoint.Start()
	t.Cleanup(endpoint.Close)

	transport, err := client.NewTransport(nil)

	if err != nil {
		t.Fatal(err)
	}

	outgoing := &http.Client{Transport: transport}
	t.Cleanup(outgoing.CloseIdleConnections)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := outgoing.Do(request)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}

	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}

	outgoing.CloseIdleConnections()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("standard client cleanup did not close owned idle connection")
	}
}
