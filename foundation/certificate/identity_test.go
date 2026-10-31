package certificate_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/certificate"
)

// ExampleIdentity_TLS adapts a provisioned identity to a native TLS server
// configuration. The exported private key and certificates are detached.
func ExampleIdentity_TLS() {
	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		log.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost"})

	if err != nil {
		log.Fatal(err)
	}

	pair, err := leaf.TLS()

	if err != nil {
		log.Fatal(err)
	}

	settings := &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	}

	fmt.Println(len(settings.Certificates), settings.MinVersion == tls.VersionTLS12)
	// Output: 1 true
}

// ExampleParseIdentity shows a round trip through public and private PEM without
// relying on files or implying that encoding validation establishes trust.
func ExampleParseIdentity() {
	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		log.Fatal(err)
	}

	identity, err := certificate.ParseIdentity(root.Bundle().PEM(), root.PrivatePEM())

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(identity)
	// Output: certificate.Identity([REDACTED])
}

// clientTemplate defines an expired client-only fixture to prove that import
// does not impose server use or current-validity policy.
func clientTemplate() *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Imported client"},
		NotBefore:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
}

// nativeSigner creates independent P-256 fixture material for import tests.
// Production generation is tested separately instead of supplying test internals.
func nativeSigner(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	return private
}

// nativePEM constructs externally encoded fixtures with the caller's certificate
// policy and signer. It returns separate public and PKCS #8 private buffers.
func nativePEM(t *testing.T, template *x509.Certificate, signer crypto.Signer) ([]byte, []byte) {
	t.Helper()

	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)

	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(signer)

	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// FuzzParseIdentity varies both public and private input. Every successful
// import must produce a usable detached native TLS export and round-trip PEM.
func FuzzParseIdentity(f *testing.F) {
	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		f.Fatal(err)
	}

	f.Add(root.Bundle().PEM(), root.PrivatePEM())
	f.Add([]byte("certificate"), []byte("private key"))

	f.Fuzz(func(t *testing.T, certPEM, keyPEM []byte) {
		identity, err := certificate.ParseIdentity(certPEM, keyPEM)

		if err != nil {
			if !errors.Is(err, certificate.ErrInvalid) {
				t.Fatal(err)
			}

			return
		}

		if _, err := identity.TLS(); err != nil {
			t.Fatal(err)
		}

		if _, err := certificate.ParseIdentity(identity.Bundle().PEM(), identity.PrivatePEM()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestParseIdentityWrongChainSignature rejects a matching-name parent signed
// with a different key, proving chain checks are not only name comparisons.
func TestParseIdentityWrongChainSignature(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost"})

	if err != nil {
		t.Fatal(err)
	}

	other, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leafDER := leaf.Bundle().Certificates()[0].Raw
	encoded := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), other.Bundle().PEM()...)

	if _, err := certificate.ParseIdentity(encoded, leaf.PrivatePEM()); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("wrong chain signer: %v", err)
	}
}

// TestParseIdentityExpiredClientECDSA demonstrates that import is independent
// from expiry and extended-key-usage policy, including for a non-CA leaf.
func TestParseIdentityExpiredClientECDSA(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := nativePEM(t, clientTemplate(), nativeSigner(t))
	identity, err := certificate.ParseIdentity(certPEM, keyPEM)

	if err != nil {
		t.Fatal(err)
	}

	pair, err := identity.TLS()

	if err != nil {
		t.Fatal(err)
	}

	if !pair.Leaf.NotAfter.Before(time.Now()) || pair.Leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatal("import changed client policy")
	}
}

// TestParseIdentityRSA imports a PKCS #1 RSA key paired with an expired client
// certificate and confirms that private export normalizes to PKCS #8.
func TestParseIdentityRSA(t *testing.T) {
	t.Parallel()

	private, err := rsa.GenerateKey(rand.Reader, 2048)

	if err != nil {
		t.Fatal(err)
	}

	certPEM, _ := nativePEM(t, clientTemplate(), private)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)})
	identity, err := certificate.ParseIdentity(certPEM, keyPEM)

	if err != nil {
		t.Fatal(err)
	}

	block, rest := pem.Decode(identity.PrivatePEM())

	if block.Type != "PRIVATE KEY" || len(rest) != 0 {
		t.Fatal("RSA key was not normalized to PKCS #8")
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)

	if err != nil {
		t.Fatal(err)
	}

	if _, ok := parsed.(*rsa.PrivateKey); !ok {
		t.Fatal("RSA key changed algorithm")
	}
}

// TestParseIdentitySEC1 imports the common EC PRIVATE KEY envelope without
// restricting imported ECDSA identities to the generation profile's curve.
func TestParseIdentitySEC1(t *testing.T) {
	t.Parallel()

	private, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	certPEM, _ := nativePEM(t, clientTemplate(), private)
	der, err := x509.MarshalECPrivateKey(private)

	if err != nil {
		t.Fatal(err)
	}

	identity, err := certificate.ParseIdentity(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))

	if err != nil {
		t.Fatal(err)
	}

	pair, err := identity.TLS()

	if err != nil {
		t.Fatal(err)
	}

	if pair.PrivateKey.(*ecdsa.PrivateKey).Curve != elliptic.P384() {
		t.Fatal("import changed the ECDSA curve")
	}
}

// TestParseIdentityEd25519 imports a PKCS #8 Ed25519 key and keeps its native
// type available through the detached TLS export.
func TestParseIdentityEd25519(t *testing.T) {
	t.Parallel()

	_, private, err := ed25519.GenerateKey(rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	certPEM, keyPEM := nativePEM(t, clientTemplate(), private)
	identity, err := certificate.ParseIdentity(certPEM, keyPEM)

	if err != nil {
		t.Fatal(err)
	}

	pair, err := identity.TLS()

	if err != nil {
		t.Fatal(err)
	}

	if _, ok := pair.PrivateKey.(ed25519.PrivateKey); !ok {
		t.Fatal("Ed25519 key changed algorithm")
	}
}

// TestParseIdentityRejectsExchangeKey rejects PKCS #8 exchange material even
// though it is a well-formed private-key format accepted by crypto/x509.
func TestParseIdentityRejectsExchangeKey(t *testing.T) {
	t.Parallel()

	certPEM, _ := nativePEM(t, clientTemplate(), nativeSigner(t))
	private, err := ecdh.X25519().GenerateKey(rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(private)

	if err != nil {
		t.Fatal(err)
	}

	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	if _, err := certificate.ParseIdentity(certPEM, encoded); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("exchange-only key: %v", err)
	}
}

// TestIdentityEd25519Detached checks byte-backed native private keys separately
// from ECDSA pointer-backed keys; neither representation may alias internal data.
func TestIdentityEd25519Detached(t *testing.T) {
	t.Parallel()

	_, private, err := ed25519.GenerateKey(rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	certPEM, keyPEM := nativePEM(t, clientTemplate(), private)
	identity, err := certificate.ParseIdentity(certPEM, keyPEM)

	if err != nil {
		t.Fatal(err)
	}

	pair, err := identity.TLS()

	if err != nil {
		t.Fatal(err)
	}

	clear(pair.PrivateKey.(ed25519.PrivateKey))

	if !bytes.Equal(identity.PrivatePEM(), keyPEM) {
		t.Fatal("native Ed25519 mutation reached identity")
	}

	if _, err := identity.TLS(); err != nil {
		t.Fatal(err)
	}
}

// TestParseIdentityMismatch rejects a well-formed key that does not belong to
// the first certificate, without exposing native TLS parser diagnostics.
func TestParseIdentityMismatch(t *testing.T) {
	t.Parallel()

	certPEM, _ := nativePEM(t, clientTemplate(), nativeSigner(t))
	_, keyPEM := nativePEM(t, clientTemplate(), nativeSigner(t))
	_, err := certificate.ParseIdentity(certPEM, keyPEM)

	if !errors.Is(err, certificate.ErrInvalid) || err.Error() != certificate.ErrInvalid.Error() {
		t.Fatalf("mismatch diagnostics: %v", err)
	}
}

// TestParseIdentityLeafFirst rejects a root-first chain, even with a matching
// first key; successful key matching alone must not validate chain order.
func TestParseIdentityLeafFirst(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost"})

	if err != nil {
		t.Fatal(err)
	}

	certs := leaf.Bundle().Certificates()
	encoded := append(root.Bundle().PEM(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs[0].Raw})...)

	if _, err := certificate.ParseIdentity(encoded, root.PrivatePEM()); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("root-first chain: %v", err)
	}
}

// TestParseIdentityIncompleteChain accepts a standalone leaf and matching key,
// since TLS deployments commonly omit a root that clients already possess.
func TestParseIdentityIncompleteChain(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost"})

	if err != nil {
		t.Fatal(err)
	}

	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Bundle().Certificates()[0].Raw})
	identity, err := certificate.ParseIdentity(encoded, leaf.PrivatePEM())

	if err != nil {
		t.Fatal(err)
	}

	if len(identity.Bundle().Certificates()) != 1 {
		t.Fatal("import inserted a root")
	}
}

// TestParseIdentityInvalidKeyPEM applies identical rejection expectations to
// strict-envelope violations, including a malformed block before a valid one.
func TestParseIdentityInvalidKeyPEM(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := nativePEM(t, clientTemplate(), nativeSigner(t))
	block, _ := pem.Decode(keyPEM)
	withHeaders := pem.EncodeToMemory(&pem.Block{
		Type: block.Type, Bytes: block.Bytes, Headers: map[string]string{"X-Header": "value"},
	})
	unexpected := pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: block.Bytes})
	invalidInputs := [][]byte{
		nil, []byte("secret garbage"), append([]byte("junk\n"), keyPEM...),
		append(keyPEM, keyPEM...), withHeaders, unexpected,
		append([]byte("-----BEGIN PRIVATE KEY-----\n!\n"), keyPEM...),
	}

	for _, encoded := range invalidInputs {
		if _, err := certificate.ParseIdentity(certPEM, encoded); !errors.Is(err, certificate.ErrInvalid) {
			t.Fatalf("malformed private PEM: %v", err)
		}
	}
}

// TestParseIdentityTrailingKeyDER rejects bytes after the outer ASN.1 value,
// including formats whose standard-library parser otherwise accepts them.
func TestParseIdentityTrailingKeyDER(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := nativePEM(t, clientTemplate(), nativeSigner(t))
	block, _ := pem.Decode(keyPEM)
	block.Bytes = append(block.Bytes, 0x05, 0x00)

	if _, err := certificate.ParseIdentity(certPEM, pem.EncodeToMemory(block)); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("trailing key DER: %v", err)
	}
}

// TestParseIdentityOwnsInputs mutates both caller buffers after construction.
// Subsequent exports must still represent the original certificates and key.
func TestParseIdentityOwnsInputs(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := nativePEM(t, clientTemplate(), nativeSigner(t))
	identity, err := certificate.ParseIdentity(certPEM, keyPEM)

	if err != nil {
		t.Fatal(err)
	}

	wantCert, wantKey := bytes.Clone(certPEM), bytes.Clone(keyPEM)

	clear(certPEM)
	clear(keyPEM)

	if !bytes.Equal(identity.Bundle().PEM(), wantCert) || !bytes.Equal(identity.PrivatePEM(), wantKey) {
		t.Fatal("identity retained caller buffers")
	}
}

// TestIdentityTLSDetached mutates the exported chain, leaf public key, and
// private key. Reparsing must produce a fresh usable identity afterward.
func TestIdentityTLSDetached(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	pair, err := root.TLS()

	if err != nil {
		t.Fatal(err)
	}

	wantKey := root.PrivatePEM()

	clear(pair.Certificate[0])
	pair.Leaf.Subject.CommonName = "mutated"
	*pair.Leaf.PublicKey.(*ecdsa.PublicKey) = nativeSigner(t).PublicKey
	*pair.PrivateKey.(*ecdsa.PrivateKey) = *nativeSigner(t)

	fresh, err := root.TLS()

	if err != nil {
		t.Fatal(err)
	}

	if fresh.Leaf.Subject.CommonName != "Example development" || !bytes.Equal(root.PrivatePEM(), wantKey) {
		t.Fatal("native mutation reached the identity")
	}

	if err := fresh.Leaf.CheckSignatureFrom(fresh.Leaf); err != nil {
		t.Fatal(err)
	}
}

// TestIdentityPrivatePEMDetached proves explicit secret exports can be modified
// independently, without changing future secret or native exports.
func TestIdentityPrivatePEMDetached(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	encoded := root.PrivatePEM()
	want := bytes.Clone(encoded)

	clear(encoded)

	if !bytes.Equal(root.PrivatePEM(), want) {
		t.Fatal("private PEM aliases internal storage")
	}
}

// TestIdentityRedaction covers ordinary and Go formatting plus structured logs.
// The certificate name is checked as well as the private PEM envelope.
func TestIdentityRedaction(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("sensitive development name")

	if err != nil {
		t.Fatal(err)
	}

	formatted := fmt.Sprintf("%v %+v %#v %s", root, root, root, root)

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.LogAttrs(context.Background(), slog.LevelInfo, "identity", slog.Any("identity", root))
	formatted += output.String()

	if strings.Contains(formatted, "sensitive development name") || strings.Contains(formatted, "PRIVATE KEY") {
		t.Fatal("identity formatting leaked material")
	}

	if !strings.Contains(formatted, "[REDACTED]") || root.LogValue().String() != root.String() {
		t.Fatal("missing redacted representation")
	}
}

// TestIdentityZero rejects all operations that require a usable identity and
// keeps secret export nil, without making zero-value formatting unsafe.
func TestIdentityZero(t *testing.T) {
	t.Parallel()

	var identity certificate.Identity

	if !errors.Is(identity.Err(), certificate.ErrInvalid) || identity.PrivatePEM() != nil {
		t.Fatal("zero identity accepted")
	}

	if _, err := identity.TLS(); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("zero TLS: %v", err)
	}

	if !errors.Is(identity.Bundle().Err(), certificate.ErrInvalid) {
		t.Fatal("zero identity has a valid bundle")
	}

	if _, err := certificate.GenerateServer(identity, []string{"localhost"}); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("zero issuer: %v", err)
	}
}

// TestLoadIdentity reads explicit paired files created by the application.
// Their bytes remain unchanged; persistence is not the library's responsibility.
func TestLoadIdentity(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := nativePEM(t, clientTemplate(), nativeSigner(t))
	directory := t.TempDir()
	certPath, keyPath := filepath.Join(directory, "client.pem"), filepath.Join(directory, "client-key.pem")

	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	identity, err := certificate.LoadIdentity(certPath, keyPath)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(identity.PrivatePEM(), keyPEM) {
		t.Fatal("loaded key changed")
	}

	stored, err := os.ReadFile(keyPath)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(stored, keyPEM) {
		t.Fatal("loading modified the key file")
	}
}

// TestLoadIdentityMissingCertificate preserves the operating-system not-found
// cause, instead of reporting an encoding failure or provisioning replacement.
func TestLoadIdentityMissingCertificate(t *testing.T) {
	t.Parallel()

	_, err := certificate.LoadIdentity(filepath.Join(t.TempDir(), "missing.pem"), "unused")

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing certificate cause: %v", err)
	}
}

// TestLoadIdentityMissingKey distinguishes key-file failures after a successful
// bounded certificate read and preserves their OS error cause.
func TestLoadIdentityMissingKey(t *testing.T) {
	t.Parallel()

	certPEM, _ := nativePEM(t, clientTemplate(), nativeSigner(t))
	path := filepath.Join(t.TempDir(), "client.pem")

	if err := os.WriteFile(path, certPEM, 0600); err != nil {
		t.Fatal(err)
	}

	_, err := certificate.LoadIdentity(path, filepath.Join(t.TempDir(), "missing-key.pem"))

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key cause: %v", err)
	}
}

// TestParseIdentityOversizedKey bounds private input independently of the public
// input and rejects before attempting to parse a large padded PEM envelope.
func TestParseIdentityOversizedKey(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := nativePEM(t, clientTemplate(), nativeSigner(t))
	keyPEM = append(keyPEM, bytes.Repeat([]byte(" "), 1024*1024)...)

	if _, err := certificate.ParseIdentity(certPEM, keyPEM); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("oversized key: %v", err)
	}
}
