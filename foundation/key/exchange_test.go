package key_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/foundation/key"
)

// exchangeFixture provisions a key for format, ownership, and agreement tests.
// Tests never persist it outside their own temporary directory.
func exchangeFixture(t *testing.T) key.Exchange {
	t.Helper()

	identity, err := key.GenerateExchange()

	if err != nil {
		t.Fatal(err)
	}

	return identity
}

// TestExchangeRoundTrip verifies private PKCS #8, public SPKI, and native
// interoperability independently of this package's encoders.
func TestExchangeRoundTrip(t *testing.T) {
	t.Parallel()

	identity := exchangeFixture(t)
	encoded := append([]byte("\n\t"), identity.PEM()...)

	parsed, err := key.ParseExchange(encoded)

	if err != nil {
		t.Fatal(err)
	}

	if !parsed.PrivateKey().Equal(identity.PrivateKey()) {
		t.Fatal("private round trip changed identity")
	}

	block, _ := pem.Decode(identity.PEM())
	native, err := x509.ParsePKCS8PrivateKey(block.Bytes)

	if err != nil || !native.(*ecdh.PrivateKey).Equal(identity.PrivateKey()) {
		t.Fatalf("PKCS #8 interoperability: %v", err)
	}

	block, _ = pem.Decode(identity.PublicPEM())
	public, err := x509.ParsePKIXPublicKey(block.Bytes)

	if err != nil || !public.(*ecdh.PublicKey).Equal(identity.PublicKey()) {
		t.Fatalf("SPKI interoperability: %v", err)
	}
}

// TestExchangeAgreement uses imported peer public PEM with native ECDH on
// both sides. The result is protocol input, not an encryption key.
func TestExchangeAgreement(t *testing.T) {
	t.Parallel()

	alice := exchangeFixture(t)
	bob := exchangeFixture(t)

	if alice.PrivateKey().Equal(bob.PrivateKey()) {
		t.Fatal("independent generations repeated")
	}

	peer, err := key.ParseExchangePublic(bob.PublicPEM())

	if err != nil {
		t.Fatal(err)
	}

	first, err := alice.PrivateKey().ECDH(peer)

	if err != nil {
		t.Fatal(err)
	}

	second, err := bob.PrivateKey().ECDH(alice.PublicKey())

	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("agreement failed: %v", err)
	}
}

// TestExchangeOwnership proves input native keys and every explicit export
// are detached from the immutable identity.
func TestExchangeOwnership(t *testing.T) {
	t.Parallel()

	raw := bytes.Repeat([]byte{42}, 32)
	native, err := ecdh.X25519().NewPrivateKey(raw)

	if err != nil {
		t.Fatal(err)
	}

	identity, err := key.NewExchange(native)

	if err != nil {
		t.Fatal(err)
	}

	before := identity.PEM()
	public := identity.PublicPEM()

	clear(raw)
	*native = ecdh.PrivateKey{}
	*identity.PrivateKey() = ecdh.PrivateKey{}
	*identity.PublicKey() = ecdh.PublicKey{}
	clear(identity.PEM())
	clear(identity.PublicPEM())

	if !bytes.Equal(before, identity.PEM()) || !bytes.Equal(public, identity.PublicPEM()) {
		t.Fatal("identity retained mutable input or export")
	}
}

// TestExchangeRejectsOtherCurves verifies native and PEM imports reject
// P-256 ECDH material even though the native types are identical.
func TestExchangeRejectsOtherCurves(t *testing.T) {
	t.Parallel()

	private, err := ecdh.P256().GenerateKey(rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := key.NewExchange(private); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("wrong native curve: %v", err)
	}

	encoded, err := x509.MarshalPKCS8PrivateKey(private)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := key.ParseExchange(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("wrong private curve: %v", err)
	}

	encoded, err = x509.MarshalPKIXPublicKey(private.PublicKey())

	if err != nil {
		t.Fatal(err)
	}

	if _, err := key.ParseExchangePublic(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded})); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("wrong public curve: %v", err)
	}
}

// TestExchangeRejectsSigning prevents accidental category interchange through
// private or public PEM, rather than accepting an arbitrary x509 key.
func TestExchangeRejectsSigning(t *testing.T) {
	t.Parallel()

	identity, err := key.GenerateSigning()

	if err != nil {
		t.Fatal(err)
	}

	if _, err := key.ParseExchange(identity.PEM()); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("signing private accepted: %v", err)
	}

	if _, err := key.ParseExchangePublic(identity.PublicPEM()); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("signing public accepted: %v", err)
	}

	if _, err := key.ParseSigning(exchangeFixture(t).PEM()); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("exchange accepted as signing: %v", err)
	}
}

// TestExchangeRejectsInvalidValues ensures nil and zero identity boundaries
// fail without exporting any usable private or public material.
func TestExchangeRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, private := range []*ecdh.PrivateKey{nil, {}} {
		if _, err := key.NewExchange(private); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("invalid native key: %v", err)
		}
	}

	identity := key.Exchange{}

	if !errors.Is(identity.Err(), key.ErrInvalid) {
		t.Fatal("zero exchange accepted")
	}

	if identity.PrivateKey() != nil || identity.PublicKey() != nil {
		t.Fatal("zero identity exported native material")
	}

	if identity.PEM() != nil || identity.PublicPEM() != nil {
		t.Fatal("zero identity exported PEM")
	}
}

// TestExchangeRejectsMalformedPrivatePEM exercises the strict bounded
// envelope and DER boundary, including encrypted/alternate PEM forms.
func TestExchangeRejectsMalformedPrivatePEM(t *testing.T) {
	t.Parallel()

	identity := exchangeFixture(t)
	block, _ := pem.Decode(identity.PEM())

	for _, encoded := range [][]byte{
		nil, []byte("private junk"), bytes.Repeat([]byte{' '}, 65537),
		append([]byte("junk\n"), identity.PEM()...),
		append(identity.PEM(), []byte("junk")...),
		append(identity.PEM(), identity.PEM()...),
		[]byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: block.Bytes}),
		pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: block.Bytes}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: append(block.Bytes, 0)}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: block.Bytes, Headers: map[string]string{"X": "Y"}}),
	} {
		if _, err := key.ParseExchange(encoded); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("malformed private accepted: %v", err)
		}
	}
}

// TestExchangeRejectsMalformedPublicPEM keeps peer import as strict as
// private import, including trailing DER inside the PEM envelope.
func TestExchangeRejectsMalformedPublicPEM(t *testing.T) {
	t.Parallel()

	identity := exchangeFixture(t)
	block, _ := pem.Decode(identity.PublicPEM())

	for _, encoded := range [][]byte{
		nil, []byte("public junk"), bytes.Repeat([]byte{' '}, 65537),
		append([]byte("junk\n"), identity.PublicPEM()...),
		append(identity.PublicPEM(), []byte("junk")...),
		append(identity.PublicPEM(), identity.PublicPEM()...), identity.PEM(),
		[]byte("-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----"),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: append(block.Bytes, 0)}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: block.Bytes, Headers: map[string]string{"X": "Y"}}),
	} {
		if _, err := key.ParseExchangePublic(encoded); !errors.Is(err, key.ErrInvalid) {
			t.Fatalf("malformed public accepted: %v", err)
		}
	}
}

// TestExchangeNativeAgreementRejectsLowOrder verifies low-order peers fail
// at the native ECDH boundary rather than yielding an all-zero shared value.
func TestExchangeNativeAgreementRejectsLowOrder(t *testing.T) {
	t.Parallel()

	identity := exchangeFixture(t)
	public, err := ecdh.X25519().NewPublicKey(make([]byte, 32))

	if err != nil {
		t.Fatal(err)
	}

	der, err := x509.MarshalPKIXPublicKey(public)

	if err != nil {
		t.Fatal(err)
	}

	peer, err := key.ParseExchangePublic(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	if err != nil {
		t.Fatal(err)
	}

	if raw, err := identity.PrivateKey().ECDH(peer); err == nil || len(raw) != 0 {
		t.Fatal("low-order agreement succeeded")
	}
}

// TestExchangeLoad covers bounded persistence and missing-file causes;
// loading never replaces absent or malformed keys with a generated identity.
func TestExchangeLoad(t *testing.T) {
	t.Parallel()

	identity := exchangeFixture(t)
	path := filepath.Join(t.TempDir(), "exchange.pem")

	if err := os.WriteFile(path, identity.PEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := key.LoadExchange(path)

	if err != nil || !loaded.PrivateKey().Equal(identity.PrivateKey()) {
		t.Fatalf("load: %v", err)
	}

	if _, err := key.LoadExchange(path + ".missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}

	if err := os.WriteFile(path, bytes.Repeat([]byte{'a'}, 65537), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := key.LoadExchange(path); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("oversize: %v", err)
	}
}

// TestExchangeFormattingRedacts ensures formatting and structured logging
// expose only a fixed marker, not private bytes or material-derived labels.
func TestExchangeFormattingRedacts(t *testing.T) {
	t.Parallel()

	identity := exchangeFixture(t)
	var output strings.Builder

	slog.New(slog.NewJSONHandler(&output, nil)).InfoContext(t.Context(), "key", "value", identity)

	want := "[REDACTED exchange key]"

	if got := fmt.Sprintf("%v %+v %#v", identity, identity, identity); got != want+" "+want+" "+want {
		t.Fatalf("unexpected format: %s", got)
	}

	if !strings.Contains(output.String(), want) {
		t.Fatal("logging did not use redaction marker")
	}
}
