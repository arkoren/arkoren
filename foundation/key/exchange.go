package key

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/x509"
	"log/slog"
)

// Exchange is an immutable X25519 key-agreement identity. The zero value is
// invalid. It is not a signing identity or an encryption protocol: callers
// must authenticate peers and apply their protocol's KDF to native ECDH output.
// Generate independent identities for unrelated protocols.
//
// Example:
//
//	identity, err := key.GenerateExchange()
//	if err != nil {
//		return err
//	}
//	_ = identity.PublicPEM()
type Exchange struct {
	// material owns validated X25519 private bytes, never a caller's buffer.
	material string
}

// NewExchange snapshots a native X25519 private key. Nil and other curves
// return ErrInvalid; the native input is not retained. Validation establishes
// representation, not entropy, provenance, or peer authentication.
//
// Example:
//
//	private, err := ecdh.X25519().GenerateKey(rand.Reader)
//	if err != nil {
//		return err
//	}
//	identity, err := key.NewExchange(private)
//	if err != nil {
//		return err
//	}
//	_ = identity
func NewExchange(private *ecdh.PrivateKey) (Exchange, error) {
	if private == nil || private.Curve() != ecdh.X25519() {
		return Exchange{}, ErrInvalid
	}

	return Exchange{material: string(private.Bytes())}, nil
}

// GenerateExchange provisions an independent X25519 identity using
// crypto/rand. It does not persist keys or establish trust in a peer.
//
// Example:
//
//	identity, err := key.GenerateExchange()
//	if err != nil {
//		return err
//	}
//	_ = identity.PublicKey()
func GenerateExchange() (Exchange, error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)

	if err != nil {
		return Exchange{}, err
	}

	return NewExchange(private)
}

// ParseExchange imports one unencrypted PKCS #8 PRIVATE KEY PEM containing
// X25519. It rejects other algorithms/curves, headers, junk, extra blocks,
// trailing DER, and input over 64 KiB with ErrInvalid. Surrounding whitespace
// is accepted and input is not retained.
//
// Example:
//
//	provisioned, err := key.GenerateExchange()
//	if err != nil {
//		return err
//	}
//	identity, err := key.ParseExchange(provisioned.PEM())
//	if err != nil {
//		return err
//	}
//	_ = identity.PublicPEM()
func ParseExchange(encoded []byte) (Exchange, error) {
	parsed, err := parsePrivate(encoded)

	if err != nil {
		return Exchange{}, err
	}

	private, ok := parsed.(*ecdh.PrivateKey)

	if !ok {
		return Exchange{}, ErrInvalid
	}

	return NewExchange(private)
}

// LoadExchange reads an explicit file and applies ParseExchange validation.
// It bounds reads to 64 KiB, preserves OS error causes, and never modifies
// the file. Parser failures never expose contents.
//
// Example:
//
//	identity, err := key.LoadExchange("exchange.pem")
//	if err != nil {
//		return err
//	}
//	_ = identity
func LoadExchange(path string) (Exchange, error) {
	raw, err := readFile(path)

	if err != nil {
		return Exchange{}, err
	}

	return ParseExchange(raw)
}

// ParseExchangePublic imports one X25519 SPKI PUBLIC KEY PEM for a peer.
// The same strict envelope, DER, and 64 KiB bounds as ParseExchange apply.
// Other curves/algorithms return ErrInvalid. This does not authenticate the
// peer or reject every low-order point: native ECDH rejects low-order inputs
// when agreement is attempted. Never use raw ECDH output as an encryption key.
//
// Example:
//
//	identity, err := key.GenerateExchange()
//	if err != nil {
//		return err
//	}
//	peer, err := key.ParseExchangePublic(identity.PublicPEM())
//	if err != nil {
//		return err
//	}
//	// Authenticate peer before passing it to the protocol's ECDH/KDF flow.
//	_ = peer
func ParseExchangePublic(encoded []byte) (*ecdh.PublicKey, error) {
	der, err := decodePEM(encoded, "PUBLIC KEY")

	if err != nil {
		return nil, err
	}

	parsed, err := x509.ParsePKIXPublicKey(der)

	if err != nil {
		return nil, ErrInvalid
	}

	public, ok := parsed.(*ecdh.PublicKey)

	if !ok {
		return nil, ErrInvalid
	}

	if public.Curve() != ecdh.X25519() {
		return nil, ErrInvalid
	}

	return public, nil
}

// Err reports ErrInvalid for a zero identity and nil for validated material.
// Consumers must reject missing configuration instead of generating a key.
//
// Example:
//
//	if err := (key.Exchange{}).Err(); errors.Is(err, key.ErrInvalid) {
//		fmt.Println("configure an exchange identity")
//	}
func (identity Exchange) Err() error {
	if identity.material == "" {
		return ErrInvalid
	}

	return nil
}

// PrivateKey exports a detached native X25519 key, or nil for a zero
// identity. Use it only inside an authenticated protocol with a specified
// KDF; this package deliberately does not invent an encryption protocol.
//
// Example:
//
//	identity, err := key.LoadExchange("exchange.pem")
//	if err != nil {
//		return err
//	}
//	private := identity.PrivateKey()
//	_ = private // Supply to the protocol's agreement implementation.
func (identity Exchange) PrivateKey() *ecdh.PrivateKey {
	if identity.Err() != nil {
		return nil
	}

	private, _ := ecdh.X25519().NewPrivateKey([]byte(identity.material))

	// Only validated native material can enter a nonzero identity.
	return private
}

// PublicKey exports a detached native X25519 public key, or nil for a zero
// identity. No private bytes are included; this does not authenticate a peer.
//
// Example:
//
//	identity, err := key.LoadExchange("exchange.pem")
//	if err != nil {
//		return err
//	}
//	public := identity.PublicKey()
//	_ = public
func (identity Exchange) PublicKey() *ecdh.PublicKey {
	private := identity.PrivateKey()

	if private == nil {
		return nil
	}

	return private.PublicKey()
}

// PEM exports unencrypted PKCS #8 private PEM, or nil for a zero identity.
// The returned bytes are detached; protect persisted output as a credential.
//
// Example:
//
//	identity, err := key.GenerateExchange()
//	if err != nil {
//		return err
//	}
//	return os.WriteFile("exchange.pem", identity.PEM(), 0o600)
func (identity Exchange) PEM() []byte {
	if identity.Err() != nil {
		return nil
	}

	encoded, _ := x509.MarshalPKCS8PrivateKey(identity.PrivateKey())

	// Validated standard-library X25519 private keys are always encodable.
	return encodePEM("PRIVATE KEY", encoded)
}

// PublicPEM exports detached standard SPKI PUBLIC KEY PEM without private
// material. A zero identity returns nil. ParseExchangePublic imports it.
//
// Example:
//
//	identity, err := key.LoadExchange("exchange.pem")
//	if err != nil {
//		return err
//	}
//	fmt.Print(string(identity.PublicPEM()))
func (identity Exchange) PublicPEM() []byte {
	if identity.Err() != nil {
		return nil
	}

	encoded, _ := x509.MarshalPKIXPublicKey(identity.PublicKey())

	// Validated standard-library X25519 public keys are always encodable.
	return encodePEM("PUBLIC KEY", encoded)
}

// String redacts the private identity in ordinary formatting, even when zero.
// Material is available only through explicit native or PEM exports.
//
// Example:
//
//	fmt.Println(key.Exchange{}) // [REDACTED exchange key]
func (identity Exchange) String() string {
	return "[REDACTED exchange key]"
}

// GoString redacts the identity in Go-syntax formatting. It never reveals
// private or public material, including for zero values.
//
// Example:
//
//	fmt.Printf("%#v", key.Exchange{}) // [REDACTED exchange key]
func (identity Exchange) GoString() string {
	return identity.String()
}

// LogValue redacts the identity in structured logs. Use PublicPEM to export
// public material explicitly instead of logging the identity itself.
//
// Example:
//
//	slog.Info("configuration", "identity", key.Exchange{})
func (identity Exchange) LogValue() slog.Value {
	return slog.StringValue(identity.String())
}
