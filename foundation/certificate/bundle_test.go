package certificate_test

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"

	"arkoren.dev/foundation/certificate"
)

// ExampleBundle_SystemPool retains system roots while adding an explicitly
// selected local authority, without installing that authority on the machine.
func ExampleBundle_SystemPool() {
	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		log.Fatal(err)
	}

	pool, err := root.Bundle().SystemPool()

	if err != nil {
		log.Fatal(err)
	}

	settings := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	fmt.Println(settings.RootCAs != nil, settings.MinVersion == tls.VersionTLS12)
	// Output: true true
}

// FuzzParseBundle covers strict PEM framing and arbitrary DER without relying
// on private implementation details. Successful values must be exportable.
func FuzzParseBundle(f *testing.F) {
	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		f.Fatal(err)
	}

	f.Add(root.Bundle().PEM())
	f.Add([]byte("-----BEGIN CERTIFICATE-----\n!\n-----END CERTIFICATE-----"))

	f.Fuzz(func(t *testing.T, encoded []byte) {
		public, err := certificate.ParseBundle(encoded)

		if err != nil {
			if !errors.Is(err, certificate.ErrInvalid) {
				t.Fatal(err)
			}

			return
		}

		if _, err := public.Pool(); err != nil {
			t.Fatal(err)
		}

		if _, err := certificate.ParseBundle(public.PEM()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestParseBundlePublicOnly proves that a public bundle rejects a private-key
// block rather than silently discarding it as x509.CertPool appenders can do.
func TestParseBundlePublicOnly(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	encoded := append(root.Bundle().PEM(), root.PrivatePEM()...)

	if _, err := certificate.ParseBundle(encoded); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("private material in bundle: %v", err)
	}
}

// TestParseBundleIndependentAnchors accepts unrelated public roots in order.
// Bundle is not constrained to be a single verifiable certificate chain.
func TestParseBundleIndependentAnchors(t *testing.T) {
	t.Parallel()

	first, err := certificate.GenerateRoot("First development CA")

	if err != nil {
		t.Fatal(err)
	}

	second, err := certificate.GenerateRoot("Second development CA")

	if err != nil {
		t.Fatal(err)
	}

	encoded := append(first.Bundle().PEM(), second.Bundle().PEM()...)
	public, err := certificate.ParseBundle(encoded)

	if err != nil {
		t.Fatal(err)
	}

	certs := public.Certificates()

	if len(certs) != 2 || certs[0].Subject.CommonName != "First development CA" {
		t.Fatal("bundle reordered anchors")
	}

	if certs[1].Subject.CommonName != "Second development CA" || !bytes.Equal(public.PEM(), encoded) {
		t.Fatal("bundle dropped an anchor")
	}
}

// TestParseBundleWhitespace permits whitespace around and between PEM blocks.
// Formatting is normalized on public export without changing DER contents.
func TestParseBundleWhitespace(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	encoded := append([]byte(" \n\t"), root.Bundle().PEM()...)
	encoded = append(encoded, []byte("\r\n  ")...)

	public, err := certificate.ParseBundle(encoded)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(public.PEM(), root.Bundle().PEM()) {
		t.Fatal("whitespace changed certificate content")
	}
}

// TestParseBundleMalformedPEM checks strict envelope and DER validation with
// identical rejection expectations, including malformed PEM before valid PEM.
func TestParseBundleMalformedPEM(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	encoded := root.Bundle().PEM()
	block, _ := pem.Decode(encoded)
	withHeaders := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: block.Bytes, Headers: map[string]string{"X-Header": "value"},
	})
	invalidDER := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})
	invalidInputs := [][]byte{
		nil, []byte(" \n"), append([]byte("junk\n"), encoded...),
		append(bytes.Clone(encoded), []byte("junk")...), withHeaders, invalidDER,
		append([]byte("-----BEGIN CERTIFICATE-----\n!\n"), encoded...),
	}

	for _, input := range invalidInputs {
		if _, err := certificate.ParseBundle(input); !errors.Is(err, certificate.ErrInvalid) {
			t.Fatalf("malformed certificate PEM: %v", err)
		}
	}
}

// TestParseBundleTrailingDER ensures a certificate envelope cannot conceal
// additional ASN.1 values after the certificate's outer sequence.
func TestParseBundleTrailingDER(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(root.Bundle().PEM())
	block.Bytes = append(block.Bytes, 0x05, 0x00)

	if _, err := certificate.ParseBundle(pem.EncodeToMemory(block)); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("trailing certificate DER: %v", err)
	}
}

// TestBundleOwnsInput proves input buffers are not retained, while successive
// PEM exports are detached from each other and the immutable bundle.
func TestBundleOwnsInput(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	encoded := root.Bundle().PEM()
	want := bytes.Clone(encoded)

	public, err := certificate.ParseBundle(encoded)

	if err != nil {
		t.Fatal(err)
	}

	clear(encoded)

	exported := public.PEM()
	clear(exported)

	if !bytes.Equal(public.PEM(), want) {
		t.Fatal("bundle retained or exported aliased bytes")
	}
}

// TestBundleCertificatesDetached modifies nested SAN containers and raw DER.
// All nested native fields must be reparsed independently on the next export.
func TestBundleCertificatesDetached(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost", "127.0.0.1"})

	if err != nil {
		t.Fatal(err)
	}

	public := leaf.Bundle()
	certs := public.Certificates()

	certs[0].DNSNames[0] = "mutated.test"
	clear(certs[0].IPAddresses[0])
	clear(certs[0].Raw)

	fresh := public.Certificates()

	if fresh[0].DNSNames[0] != "localhost" || fresh[0].IPAddresses[0].String() != "127.0.0.1" {
		t.Fatal("native certificate mutation reached bundle")
	}

	if err := fresh[0].CheckSignatureFrom(fresh[1]); err != nil {
		t.Fatal(err)
	}
}

// TestBundlePoolDetached adds a second root to one export and checks that a
// fresh pool still contains exactly the original public root.
func TestBundlePoolDetached(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("First development CA")

	if err != nil {
		t.Fatal(err)
	}

	other, err := certificate.GenerateRoot("Second development CA")

	if err != nil {
		t.Fatal(err)
	}

	pool, err := root.Bundle().Pool()

	if err != nil {
		t.Fatal(err)
	}

	pool.AddCert(other.Bundle().Certificates()[0])

	fresh, err := root.Bundle().Pool()

	if err != nil {
		t.Fatal(err)
	}

	if pool.Equal(fresh) {
		t.Fatal("pool mutation affected subsequent export")
	}
}

// TestBundleZero rejects trust-pool creation from an uninitialized bundle.
// Non-error exports remain nil rather than inventing an empty valid identity.
func TestBundleZero(t *testing.T) {
	t.Parallel()

	var public certificate.Bundle

	if !errors.Is(public.Err(), certificate.ErrInvalid) {
		t.Fatal("zero bundle accepted")
	}

	if public.PEM() != nil || public.Certificates() != nil {
		t.Fatal("zero bundle exported data")
	}

	if _, err := public.Pool(); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("zero pool: %v", err)
	}
}

// TestLoadBundle reads an explicitly chosen public file and preserves its data.
// No implicit root-store path or platform trust is involved.
func TestLoadBundle(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "root.pem")

	if err := os.WriteFile(path, root.Bundle().PEM(), 0600); err != nil {
		t.Fatal(err)
	}

	public, err := certificate.LoadBundle(path)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(public.PEM(), root.Bundle().PEM()) {
		t.Fatal("loaded bundle differs")
	}
}

// TestLoadBundleMissing preserves OS failure identity so callers can distinguish
// an absent provisioning file from a malformed certificate bundle.
func TestLoadBundleMissing(t *testing.T) {
	t.Parallel()

	_, err := certificate.LoadBundle(filepath.Join(t.TempDir(), "missing.pem"))

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bundle cause: %v", err)
	}
}

// TestParseBundleOversized bounds even valid PEM followed by excessive whitespace.
// The limit includes framing and padding, not only decoded DER.
func TestParseBundleOversized(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	encoded := append(root.Bundle().PEM(), bytes.Repeat([]byte(" "), 1024*1024)...)

	if _, err := certificate.ParseBundle(encoded); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("oversized bundle: %v", err)
	}
}

// TestLoadBundleOversized checks the file-read bound independently of parsing.
// A file that exceeds one MiB returns ErrInvalid without unbounded allocation.
func TestLoadBundleOversized(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "oversized.pem")

	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), 1024*1024+1), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := certificate.LoadBundle(path); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("oversized bundle file: %v", err)
	}
}
