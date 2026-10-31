package server_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/client"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/server"
)

// tlsIdentity issues an in-memory ECDSA identity for template. A nil authority
// self-signs a root; otherwise authority signs it. No files or machine trust
// settings are used, and Leaf is populated for issuing further identities.
func tlsIdentity(t *testing.T, template *x509.Certificate, authority *tls.Certificate) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	template.NotBefore = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	template.NotAfter = time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)
	parent, signer := template, any(key)

	if authority != nil {
		parent, signer = authority.Leaf, authority.PrivateKey
	}

	encoded, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), signer)

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := x509.ParseCertificate(encoded)

	if err != nil {
		t.Fatal(err)
	}

	return tls.Certificate{Certificate: [][]byte{encoded}, PrivateKey: key, Leaf: leaf}
}

// tlsCredentials creates a private CA, an HTTPS identity valid for localhost
// and loopback, and an explicit trust pool. Tests may use the CA to issue a
// separate client identity without importing a certificate-management module.
func tlsCredentials(t *testing.T) (tls.Certificate, tls.Certificate, *x509.CertPool) {
	t.Helper()

	authority := tlsIdentity(t, &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "server test authority"},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, nil)
	identity := tlsIdentity(t, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, &authority)
	roots := x509.NewCertPool()
	roots.AddCert(authority.Leaf)

	return authority, identity, roots
}

// tlsHTTPClient uses the real Arkoren outgoing transport over native TLS with
// verification enabled. One attempt avoids retry delays on intentional TLS
// failures; the test owns and closes the native transport's idle connections.
func tlsHTTPClient(t *testing.T, config *tls.Config) *http.Client {
	t.Helper()

	native := &http.Transport{TLSClientConfig: config, ForceAttemptHTTP2: true}
	t.Cleanup(native.CloseIdleConnections)

	return &http.Client{
		Transport: client.Transport(native, func(transport *client.Builder) {
			transport.Attempts(1)
		}),
		Timeout: shutdownWait,
	}
}

// TestTLSVerifiedPrivateCA checks end-to-end verification and Request.TLS over
// the Arkoren client transport without disabling hostname or chain validation.
func TestTLSVerifiedPrivateCA(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	observed := make(chan bool, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed <- request.TLS != nil
		writer.WriteHeader(http.StatusNoContent)
	})

	srv, _, _ := startServer(t, handler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{identity}})
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = reply.Body.Close() // The empty response has no resources beyond its body.
	})

	if reply.StatusCode != http.StatusNoContent || len(reply.TLS.VerifiedChains) == 0 {
		t.Fatalf("response = %d, TLS = %+v", reply.StatusCode, reply.TLS)
	}

	if !receive(t, observed, "server TLS state") {
		t.Fatal("server request has no TLS state")
	}
}

// TestTLSWrongHostnameFails checks that the client does not accept a trusted
// private-CA certificate for a different hostname.
func TestTLSWrongHostnameFails(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	srv, _, _ := startServer(t, helloHandler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{identity}})
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots, ServerName: "wrong.example"})

	_, err := httpClient.Get("https://" + srv.Addr())
	var hostnameError x509.HostnameError

	if !errors.As(err, &hostnameError) {
		t.Fatalf("Get = %v, want hostname verification failure", err)
	}
}

// TestTLSUntrustedAuthorityFails checks that a private CA is not trusted unless
// the outgoing transport explicitly includes it in RootCAs.
func TestTLSUntrustedAuthorityFails(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	srv, _, _ := startServer(t, helloHandler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{identity}})
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: x509.NewCertPool()})

	_, err := httpClient.Get("https://" + srv.Addr())
	var authorityError x509.UnknownAuthorityError

	if !errors.As(err, &authorityError) {
		t.Fatalf("Get = %v, want unknown authority", err)
	}
}

// TestTLSMutualAuthentication checks that an existing client identity is sent
// through the native transport and verified by the server's explicit client CA.
func TestTLSMutualAuthentication(t *testing.T) {
	t.Parallel()

	authority, identity, roots := tlsCredentials(t)
	clientIdentity := tlsIdentity(t, &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test caller"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, &authority)
	observed := make(chan string, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed <- request.TLS.VerifiedChains[0][0].Subject.CommonName
		writer.WriteHeader(http.StatusNoContent)
	})

	srv, _, _ := startServer(t, handler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{
			Certificates: []tls.Certificate{identity},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    roots,
		})
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{clientIdentity}})

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // Only the authenticated request identity matters here.

	if got := receive(t, observed, "client identity"); got != "test caller" {
		t.Fatalf("client identity = %q", got)
	}
}

// TestTLSMutualAuthenticationRequiresIdentity checks that trusting the server
// alone cannot bypass RequireAndVerifyClientCert.
func TestTLSMutualAuthenticationRequiresIdentity(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	srv, _, _ := startServer(t, helloHandler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{
			Certificates: []tls.Certificate{identity},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    roots,
		})
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})

	if reply, err := httpClient.Get("https://" + srv.Addr()); err == nil {
		_ = reply.Body.Close() // Unexpected successful responses must not leak.
		t.Fatal("mutual TLS accepted a client without an identity")
	}
}

// TestTLSNegotiatesHTTP2 checks that using ServeTLS retains native HTTP/2 ALPN
// negotiation, rather than wrapping a TLS listener and serving HTTP/1 only.
func TestTLSNegotiatesHTTP2(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	srv, _, _ := startServer(t, helloHandler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{identity}})
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // Protocol negotiation does not require the body.

	if reply.ProtoMajor != 2 || reply.TLS.NegotiatedProtocol != "h2" {
		t.Fatalf("protocol = %q, ALPN = %q", reply.Proto, reply.TLS.NegotiatedProtocol)
	}
}

// TestTLSGetCertificateRunsOnlyAtHandshake checks callback-only credentials
// are accepted without executing application code during New or Start.
func TestTLSGetCertificateRunsOnlyAtHandshake(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	var calls atomic.Int64
	srv := server.New(helloHandler, func(listener *server.Builder) {
		listener.Addr("127.0.0.1:0").TLS(&tls.Config{
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				calls.Add(1)

				return &identity, nil
			},
		})
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = srv.Stop(context.Background()) // Covers failure before Serve starts.
	})

	if calls.Load() != 0 {
		t.Fatal("certificate callback ran before handshake")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- srv.Serve(ctx)
	}()

	t.Cleanup(func() {
		cancel()

		if err := receive(t, done, "TLS Serve"); err != nil {
			t.Error(err)
		}
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots, ServerName: "localhost"})

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // The completed handshake is the assertion's subject.

	if calls.Load() != 1 {
		t.Fatalf("certificate callback calls = %d, want 1", calls.Load())
	}
}

// TestTLSGetConfigForClientRunsOnlyAtHandshake checks that a dynamic full TLS
// configuration, including its ALPN settings, is honored at connection time.
func TestTLSGetConfigForClientRunsOnlyAtHandshake(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	var calls atomic.Int64
	srv, _, _ := startServer(t, helloHandler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{
			GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				calls.Add(1)

				return &tls.Config{
					Certificates: []tls.Certificate{identity},
					NextProtos:   []string{"h2", "http/1.1"},
				}, nil
			},
		})
	})

	if calls.Load() != 0 {
		t.Fatal("configuration callback ran before handshake")
	}

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // Callback execution and ALPN do not require the body.

	if calls.Load() != 1 || reply.ProtoMajor != 2 {
		t.Fatalf("configuration calls = %d, protocol = %q", calls.Load(), reply.Proto)
	}
}

// TestTLSNativeConfigurationRemainsShared checks the escape hatch selects HTTPS
// and the builder preserves the native tls.Config pointer rather than cloning.
func TestTLSNativeConfigurationRemainsShared(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	config := &tls.Config{Certificates: []tls.Certificate{identity}}
	srv, _, _ := startServer(t, helloHandler, func(listener *server.Builder) {
		listener.TLS(config)
	})

	if srv.HTTP().TLSConfig != config {
		t.Fatal("builder cloned native TLS configuration")
	}

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // A successful native TLS handshake is sufficient.
}

// TestTLSNativeEscapeHatchSelectsHTTPS checks setting HTTP().TLSConfig without
// the builder uses the same startup validation and TLS serving path.
func TestTLSNativeEscapeHatchSelectsHTTPS(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	srv := server.New(helloHandler, func(listener *server.Builder) {
		listener.Addr("127.0.0.1:0")
	})

	srv.HTTP().TLSConfig = &tls.Config{Certificates: []tls.Certificate{identity}}

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- srv.Serve(ctx)
	}()

	t.Cleanup(func() {
		cancel()

		if err := receive(t, done, "native TLS Serve"); err != nil {
			t.Error(err)
		}
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // A verified request proves native configuration works.
}

// TestTLSBuilderPublicationDetachesReference checks retained construction drafts
// cannot replace the published TLS pointer, while the native value stays shared.
func TestTLSBuilderPublicationDetachesReference(t *testing.T) {
	t.Parallel()

	config := &tls.Config{}
	var retained *server.Builder
	srv := server.New(helloHandler, func(listener *server.Builder) {
		retained = listener
		listener.TLS(config)
	})
	retained.TLS(nil)

	if srv.HTTP().TLSConfig != config {
		t.Fatal("retained construction draft replaced published TLS configuration")
	}
}

// TestTLSDefaultIsPlaintext checks omitting TLS preserves the existing HTTP
// listener and supplies nil Request.TLS to application handlers.
func TestTLSDefaultIsPlaintext(t *testing.T) {
	t.Parallel()

	observed := make(chan bool, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed <- request.TLS == nil
		writer.WriteHeader(http.StatusNoContent)
	})

	srv, _, _ := startServer(t, handler)
	httpClient := tlsHTTPClient(t, nil)

	reply, err := httpClient.Get("http://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // The plaintext response is deliberately empty.

	if !receive(t, observed, "default plaintext request") {
		t.Fatal("default listener unexpectedly enabled TLS")
	}
}

// TestTLSNilRestoresPlaintext checks that TLS is opt-in and a final nil setting
// restores HTTP, including a nil Request.TLS value in the handler.
func TestTLSNilRestoresPlaintext(t *testing.T) {
	t.Parallel()

	observed := make(chan bool, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed <- request.TLS == nil
		writer.WriteHeader(http.StatusNoContent)
	})

	srv, _, _ := startServer(t, handler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{}).TLS(nil)
	})

	httpClient := tlsHTTPClient(t, nil)

	reply, err := httpClient.Get("http://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // This HTTP response has no body.

	if !receive(t, observed, "plaintext request") {
		t.Fatal("nil TLS configuration did not restore plaintext")
	}
}

// TestTLSNeverFallsBackToPlaintext checks an HTTP request sent to an HTTPS
// listener is rejected by net/http before the application handler runs.
func TestTLSNeverFallsBackToPlaintext(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	var calls atomic.Int64
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	})

	srv, _, _ := startServer(t, handler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{identity}})
	})

	httpClient := tlsHTTPClient(t, nil)

	reply, err := httpClient.Get("http://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // net/http's diagnostic body is not part of our API.

	if reply.StatusCode != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("plaintext status = %d, handler calls = %d", reply.StatusCode, calls.Load())
	}
}

// TestTLSCallbackFailureDoesNotStopServing checks callback errors fail only the
// handshake, never dispatch plaintext, and permit a later dynamically supplied
// identity without changing the tls.Config after Start.
func TestTLSCallbackFailureDoesNotStopServing(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	var available atomic.Bool
	var calls atomic.Int64
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	})

	srv, _, _ := startServer(t, handler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				if !available.Load() {
					return nil, errors.New("identity unavailable")
				}

				return &identity, nil
			},
		})
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})

	if reply, err := httpClient.Get("https://" + srv.Addr()); err == nil {
		_ = reply.Body.Close() // Unexpected successful responses must not leak.
		t.Fatal("callback error did not fail the TLS handshake")
	}

	if calls.Load() != 0 {
		t.Fatal("failed handshake invoked the application handler")
	}

	available.Store(true)

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close() // The replacement runtime identity completed a request.

	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

// assertTLSStartRejected checks invalid static credentials fail without a bound
// listener or Listening event. It then repairs native configuration before a
// successful Start to prove the failure did not consume lifecycle resources.
func assertTLSStartRejected(t *testing.T, config *tls.Config, message string) {
	t.Helper()

	bus := event.New()
	var calls atomic.Int64

	if _, err := bus.Listen(server.Listening, func(context.Context, server.ListeningEvent) error {
		calls.Add(1)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	srv := server.New(helloHandler, func(listener *server.Builder) {
		listener.Addr("127.0.0.1:0").TLS(config).Events(bus)
	})

	if err := srv.Start(context.Background()); err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("Start = %v, want %q", err, message)
	}

	if srv.Addr() != "127.0.0.1:0" || calls.Load() != 0 {
		t.Fatalf("invalid Start bound %q or emitted %d Listening events", srv.Addr(), calls.Load())
	}

	if err := srv.Serve(context.Background()); err == nil {
		t.Fatal("Serve accepted an invalid Start")
	}

	_, identity, _ := tlsCredentials(t)
	srv.HTTP().TLSConfig = &tls.Config{Certificates: []tls.Certificate{identity}}

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if connection, err := net.DialTimeout("tcp", srv.Addr(), shutdownWait); err == nil {
		_ = connection.Close() // Unexpected connections must not leak.
		t.Fatal("Stop left the unserved TLS listener open")
	}
}

// TestTLSStartRejectsMissingCredentials checks opting into TLS cannot bind a
// listener with an empty configuration and silently serve plaintext.
func TestTLSStartRejectsMissingCredentials(t *testing.T) {
	t.Parallel()

	assertTLSStartRejected(t, &tls.Config{}, "certificates, GetCertificate, or GetConfigForClient required")
}

// TestTLSStartRejectsMissingChain checks a static private key alone cannot be
// advertised as a usable HTTPS identity during startup.
func TestTLSStartRejectsMissingChain(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	identity.Certificate = nil

	assertTLSStartRejected(t, &tls.Config{Certificates: []tls.Certificate{identity}}, "empty certificate chain")
}

// TestTLSStartRejectsMissingKey checks a static certificate alone fails startup
// instead of accepting connections whose every handshake must fail.
func TestTLSStartRejectsMissingKey(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	identity.PrivateKey = nil

	assertTLSStartRejected(t, &tls.Config{Certificates: []tls.Certificate{identity}}, "missing private key")
}

// TestTLSStartRejectsTypedNilKey checks a typed nil private key is rejected
// without attempting to call its cryptographic methods or panicking.
func TestTLSStartRejectsTypedNilKey(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	identity.PrivateKey = (*ecdsa.PrivateKey)(nil)

	assertTLSStartRejected(t, &tls.Config{Certificates: []tls.Certificate{identity}}, "missing private key")
}

// TestTLSStartRejectsNilEd25519Key checks a nil slice implementing crypto.Signer
// fails before binding, without calling Public (which would panic on this key).
func TestTLSStartRejectsNilEd25519Key(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	identity.PrivateKey = ed25519.PrivateKey(nil)

	assertTLSStartRejected(t, &tls.Config{Certificates: []tls.Certificate{identity}}, "missing private key")
}

// TestTLSStartRejectsUnsupportedKey checks a value without a native signing or
// decryption implementation is not accepted as a static TLS private key.
func TestTLSStartRejectsUnsupportedKey(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	identity.PrivateKey = "not a private key"

	assertTLSStartRejected(t, &tls.Config{Certificates: []tls.Certificate{identity}}, "private key must implement")
}

// TestTLSStartRejectsMalformedChain checks even a populated Leaf cache cannot
// hide malformed wire certificates in the static chain.
func TestTLSStartRejectsMalformedChain(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	identity.Certificate = [][]byte{[]byte("not a certificate")}

	assertTLSStartRejected(t, &tls.Config{Certificates: []tls.Certificate{identity}}, "invalid certificate chain")
}

// TestTLSShutdownEndsSSE checks verified HTTPS streaming still observes the
// server shutdown signal and completes before the graceful-drain budget.
func TestTLSShutdownEndsSSE(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values := make(chan []byte, 1)
		values <- []byte("hello")
		_ = response.SSEChan(writer, request, values) // Client/shutdown owns stream completion.
	})

	srv, cancel, done := startServer(t, handler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{identity}})
		listener.ShutdownTimeout(time.Minute)
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})

	reply, err := httpClient.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = reply.Body.Close() // Always release the streaming response after assertions.
	})

	reader := bufio.NewReader(reply.Body)

	line, err := reader.ReadString('\n')

	if err != nil || line != "data: hello\n" {
		t.Fatalf("first SSE line = %q, %v", line, err)
	}

	cancel()

	if err := receive(t, done, "TLS SSE shutdown"); err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("TLS SSE did not finish cleanly: %v", err)
	}
}

// TestTLSShutdownDrainsInFlightRequest checks cancellation signals HTTPS
// handlers without canceling request contexts or truncating normal responses.
func TestTLSShutdownDrainsInFlightRequest(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	entered := make(chan struct{})
	observed := make(chan error, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(entered)
		<-server.ShuttingDown(request.Context())
		observed <- request.Context().Err()
		_, _ = io.WriteString(writer, "drained") // The client verifies complete delivery.
	})

	srv, cancel, done := startServer(t, handler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{identity}})
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})
	result := make(chan string, 1)

	go func() {
		reply, err := httpClient.Get("https://" + srv.Addr())

		if err != nil {
			result <- err.Error()

			return
		}

		body, err := io.ReadAll(reply.Body)
		_ = reply.Body.Close() // The request goroutine owns its response body.

		if err != nil {
			result <- err.Error()

			return
		}

		result <- string(body)
	}()

	receive(t, entered, "in-flight TLS request")
	cancel()

	if err := receive(t, observed, "TLS request context"); err != nil {
		t.Fatalf("shutdown canceled the request: %v", err)
	}

	if body := receive(t, result, "drained TLS response"); body != "drained" {
		t.Fatalf("response = %q, want drained", body)
	}

	if err := receive(t, done, "TLS graceful shutdown"); err != nil {
		t.Fatal(err)
	}
}

// TestTLSShutdownForceClosesUndrainedRequest checks an HTTPS connection is
// force-closed when its handler cannot finish within the graceful-drain budget.
func TestTLSShutdownForceClosesUndrainedRequest(t *testing.T) {
	t.Parallel()

	_, identity, roots := tlsCredentials(t)
	entered := make(chan struct{})
	handlerDone := make(chan struct{})
	handler := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		<-request.Context().Done()
		close(handlerDone)
	})

	srv, cancel, done := startServer(t, handler, func(listener *server.Builder) {
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{identity}})
		listener.ShutdownTimeout(time.Millisecond)
	})

	httpClient := tlsHTTPClient(t, &tls.Config{RootCAs: roots})
	result := make(chan error, 1)

	go func() {
		reply, err := httpClient.Get("https://" + srv.Addr())

		if err == nil {
			_ = reply.Body.Close() // Unexpected successful responses must not leak.
		}

		result <- err
	}()

	receive(t, entered, "undrained TLS request")
	cancel()

	if err := receive(t, done, "TLS forced shutdown"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Serve = %v, want shutdown deadline exceeded", err)
	}

	receive(t, handlerDone, "force-closed TLS handler")

	if err := receive(t, result, "force-closed TLS response"); err == nil {
		t.Fatal("undrained HTTPS request unexpectedly completed successfully")
	}
}

// TestTLSServeSetupFailureClosesListener checks native HTTP/2 setup errors do
// not leak the TCP listener acquired by Start or retry as plaintext HTTP.
func TestTLSServeSetupFailureClosesListener(t *testing.T) {
	t.Parallel()

	_, identity, _ := tlsCredentials(t)
	srv := server.New(helloHandler, func(listener *server.Builder) {
		listener.Addr("127.0.0.1:0").TLS(&tls.Config{
			Certificates: []tls.Certificate{identity},
			CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA},
		})
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = srv.Stop(context.Background()) // Safe even after Serve's setup failure.
	})

	if err := srv.Serve(context.Background()); err == nil {
		t.Fatal("Serve accepted HTTP/2's prohibited-only cipher list")
	}

	if connection, err := net.DialTimeout("tcp", srv.Addr(), shutdownWait); err == nil {
		_ = connection.Close() // Unexpected accepted connections must not leak.
		t.Fatal("Serve setup failure left its listener open")
	}
}
