package key_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"arkoren.dev/foundation/key"
)

// ExampleGenerateSigning exports only a browser public key from a private identity.
func ExampleGenerateSigning() {
	identity, err := key.GenerateSigning()

	if err != nil {
		fmt.Println("generation failed")

		return
	}

	restored, err := key.ParseSigning(identity.PEM())

	if err != nil {
		fmt.Println("parsing failed")

		return
	}

	fmt.Println(identity.VAPIDPublicKey() == restored.VAPIDPublicKey())
	fmt.Println(identity)
	// Output:
	// true
	// [REDACTED signing key]
}

// TestSigningRoundTrip checks PEM persistence and independently decodes browser keys.
func TestSigningRoundTrip(t *testing.T) {
	t.Parallel()

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "identity.pem")

	if err := os.WriteFile(path, identity.PEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := key.LoadSigning(path)

	if err != nil || loaded.VAPIDPublicKey() != identity.VAPIDPublicKey() {
		t.Fatalf("load failed: %v", err)
	}

	point, err := base64.RawURLEncoding.DecodeString(loaded.VAPIDPublicKey())

	if err != nil || len(point) != 65 {
		t.Fatalf("public format: %v", err)
	}

	if _, err := ecdh.P256().NewPublicKey(point); err != nil {
		t.Fatal(err)
	}
}

// TestSigningOwnership proves input and native exports cannot mutate the identity.
func TestSigningOwnership(t *testing.T) {
	t.Parallel()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	identity, err := key.NewSigning(private)

	if err != nil {
		t.Fatal(err)
	}

	before := identity.PEM()

	*private = ecdsa.PrivateKey{}
	*identity.PrivateKey() = ecdsa.PrivateKey{}
	*identity.PublicKey() = ecdsa.PublicKey{}
	clear(identity.PEM())

	if !bytes.Equal(before, identity.PEM()) {
		t.Fatal("identity aliases caller or exported state")
	}
}

// TestSigningPublicPEM independently decodes SPKI and verifies both the
// public point and export ownership without exposing private material.
func TestSigningPublicPEM(t *testing.T) {
	t.Parallel()

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	before := identity.PublicPEM()
	block, _ := pem.Decode(before)

	if block == nil || block.Type != "PUBLIC KEY" {
		t.Fatal("expected SPKI PUBLIC KEY PEM")
	}

	public, err := x509.ParsePKIXPublicKey(block.Bytes)

	if err != nil || !public.(*ecdsa.PublicKey).Equal(identity.PublicKey()) {
		t.Fatalf("SPKI round trip: %v", err)
	}

	clear(identity.PublicPEM())
	*identity.PublicKey() = ecdsa.PublicKey{}

	if !bytes.Equal(before, identity.PublicPEM()) {
		t.Fatal("public exports alias internal state")
	}
}

// TestSigningZeroPublicExports ensures no public material is synthesized
// when an identity is missing from application configuration.
func TestSigningZeroPublicExports(t *testing.T) {
	t.Parallel()

	if (key.Signing{}).PublicKey() != nil || (key.Signing{}).PublicPEM() != nil {
		t.Fatal("zero identity exported public material")
	}
}

// TestSigningRejectsMalformedNative checks nil, missing coordinates, and wrong curves.
func TestSigningRejectsMalformedNative(t *testing.T) {
	t.Parallel()

	wrong, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	for _, private := range []*ecdsa.PrivateKey{nil, {}, {PublicKey: ecdsa.PublicKey{Curve: elliptic.P256()}}, wrong} {
		if _, err := key.NewSigning(private); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("malformed key accepted: %v", err)
		}
	}
}

// TestSigningRejectsMismatchedPair verifies private/public consistency centrally.
func TestSigningRejectsMismatchedPair(t *testing.T) {
	t.Parallel()

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	private := identity.PrivateKey()
	other, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	private.PublicKey = other.PrivateKey().PublicKey

	if _, err := key.NewSigning(private); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("mismatched key accepted: %v", err)
	}
}

// TestSigningRejectsPEMJunk prevents permissive decoding from discarding input.
func TestSigningRejectsPEMJunk(t *testing.T) {
	t.Parallel()

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	for _, encoded := range [][]byte{
		nil,
		[]byte("secret-junk"),
		append([]byte("junk\n"), identity.PEM()...),
		append(identity.PEM(), []byte("junk")...),
		append(identity.PEM(), identity.PEM()...),
		append([]byte("-----BEGIN PRIVATE KEY-----\nmalformed\n"), identity.PEM()...),
		[]byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----"),
	} {
		if _, err := key.ParseSigning(encoded); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("malformed PEM accepted: %v", err)
		}
	}
}

// TestSigningRejectsOtherCurvesAndPEMForms checks the fixed PKCS #8 format boundary.
func TestSigningRejectsOtherCurvesAndPEMForms(t *testing.T) {
	t.Parallel()

	private, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	encoded, err := x509.MarshalPKCS8PrivateKey(private)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := key.ParseSigning(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})); !errors.Is(err, key.ErrInvalid) {
		t.Fatal("P-384 accepted")
	}

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(identity.PEM())
	encoded = block.Bytes

	for _, block := range []*pem.Block{
		{Type: "EC PRIVATE KEY", Bytes: encoded},
		{Type: "ENCRYPTED PRIVATE KEY", Bytes: encoded},
		{Type: "PRIVATE KEY", Bytes: encoded, Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}},
	} {
		if _, err := key.ParseSigning(pem.EncodeToMemory(block)); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("unsupported PEM accepted: %v", err)
		}
	}
}

// TestSigningRejectsOtherAlgorithms verifies PKCS #8 alone is not enough.
func TestSigningRejectsOtherAlgorithms(t *testing.T) {
	t.Parallel()

	_, private, err := ed25519.GenerateKey(rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	encoded, err := x509.MarshalPKCS8PrivateKey(private)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := key.ParseSigning(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})); !errors.Is(err, key.ErrInvalid) {
		t.Fatal("Ed25519 accepted as P-256")
	}
}

// TestSigningRejectsTrailingDER rejects ignored bytes inside an otherwise valid PEM.
func TestSigningRejectsTrailingDER(t *testing.T) {
	t.Parallel()

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(identity.PEM())
	block.Bytes = append(block.Bytes, 0)

	if _, err := key.ParseSigning(pem.EncodeToMemory(block)); !errors.Is(err, key.ErrInvalid) {
		t.Fatal("trailing DER accepted")
	}
}
