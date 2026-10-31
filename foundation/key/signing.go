package key

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"log/slog"
)

// Signing is an immutable ECDSA P-256 signing identity. Its zero value is
// invalid. Provision independent identities for each use, even on the same
// curve. Generated material cannot replace provider-issued Apple credentials.
//
// Example:
//
//	identity, err := key.GenerateSigning()
//	if err != nil {
//		return err
//	}
//	public := identity.PublicKey()
//	_ = public
type Signing struct {
	// scalar owns a validated P-256 scalar independently of native inputs.
	scalar string
}

// NewSigning validates and snapshots a native ECDSA P-256 private key. It
// checks public/private consistency and rejects malformed manually assembled
// keys with ErrInvalid, without exposing material or propagating encoding
// panics. Mutating the input afterward cannot change the result.
//
// Example:
//
//	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
//	if err != nil {
//		return err
//	}
//	identity, err := key.NewSigning(private)
//	if err != nil {
//		return err
//	}
//	_ = identity
func NewSigning(private *ecdsa.PrivateKey) (identity Signing, err error) {
	if private == nil || private.Curve != elliptic.P256() {
		return Signing{}, ErrInvalid
	}

	defer func() {
		if recover() != nil {
			identity, err = Signing{}, ErrInvalid
		}
	}()

	raw, err := private.Bytes()

	if err != nil {
		return Signing{}, ErrInvalid
	}

	derived, err := ecdh.P256().NewPrivateKey(raw)

	if err != nil {
		return Signing{}, ErrInvalid
	}

	point, err := private.PublicKey.Bytes()

	if err != nil || !bytes.Equal(derived.PublicKey().Bytes(), point) {
		return Signing{}, ErrInvalid
	}

	return Signing{scalar: string(raw)}, nil
}

// GenerateSigning provisions an independent ECDSA P-256 signing identity
// using crypto/rand. It does not establish provider provenance or persist it.
//
// Example:
//
//	identity, err := key.GenerateSigning()
//	if err != nil {
//		return err
//	}
//	_ = identity
func GenerateSigning() (Signing, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		return Signing{}, err
	}

	return NewSigning(private)
}

// ParseSigning imports exactly one unencrypted PKCS #8 PRIVATE KEY PEM
// block containing ECDSA P-256. Surrounding whitespace is permitted; junk,
// extra blocks, headers, SEC 1, other curves, and trailing DER are rejected
// with ErrInvalid. Input is bounded to 64 KiB and is not retained.
//
// Example:
//
//	provisioned, err := key.GenerateSigning()
//	if err != nil {
//		return err
//	}
//	identity, err := key.ParseSigning(provisioned.PEM())
//	if err != nil {
//		return err
//	}
//	_ = identity.PublicPEM()
func ParseSigning(encoded []byte) (Signing, error) {
	parsed, err := parsePrivate(encoded)

	if err != nil {
		return Signing{}, err
	}

	private, ok := parsed.(*ecdsa.PrivateKey)

	if !ok {
		return Signing{}, ErrInvalid
	}

	return NewSigning(private)
}

// LoadSigning reads an explicit PKCS #8 file without modifying it and applies
// ParseSigning validation. OS errors retain their causes; parser errors never
// include private material. File reads are bounded to 64 KiB.
//
// Example:
//
//	identity, err := key.LoadSigning("AuthKey_ABC123DEFG.p8")
//	if err != nil {
//		return err
//	}
//	_ = identity
func LoadSigning(path string) (Signing, error) {
	raw, err := readFile(path)

	if err != nil {
		return Signing{}, err
	}

	return ParseSigning(raw)
}

// Err reports ErrInvalid for the zero identity, otherwise nil. Consumers use
// this check to reject missing configuration instead of provisioning a key.
//
// Example:
//
//	if err := (key.Signing{}).Err(); errors.Is(err, key.ErrInvalid) {
//		fmt.Println("configure a signing identity")
//	}
func (identity Signing) Err() error {
	if identity.scalar == "" {
		return ErrInvalid
	}

	return nil
}

// PrivateKey exports an independent native private key, or nil for a zero
// identity. Changes to it cannot affect the identity or later exports.
//
// Example:
//
//	identity, err := key.LoadSigning("signing.pem")
//	if err != nil {
//		return err
//	}
//	private := identity.PrivateKey()
//	_ = private
func (identity Signing) PrivateKey() *ecdsa.PrivateKey {
	if identity.Err() != nil {
		return nil
	}

	private, _ := ecdsa.ParseRawPrivateKey(elliptic.P256(), []byte(identity.scalar))

	// Only validated scalars reach this parser, so it cannot fail.
	return private
}

// PublicKey exports an independent ECDSA P-256 verification key. The zero
// identity returns nil; modifying its point cannot change later exports.
//
// Example:
//
//	identity, err := key.LoadSigning("signing.pem")
//	if err != nil {
//		return err
//	}
//	public := identity.PublicKey()
//	_ = public
func (identity Signing) PublicKey() *ecdsa.PublicKey {
	private := identity.PrivateKey()

	if private == nil {
		return nil
	}

	return &private.PublicKey
}

// PEM exports unencrypted PKCS #8 private PEM, or nil for a zero identity.
// Protect the detached output as a private credential when persisting it.
//
// Example:
//
//	identity, err := key.GenerateSigning()
//	if err != nil {
//		return err
//	}
//	return os.WriteFile("signing.pem", identity.PEM(), 0o600)
func (identity Signing) PEM() []byte {
	if identity.Err() != nil {
		return nil
	}

	encoded, _ := x509.MarshalPKCS8PrivateKey(identity.PrivateKey())

	// The validated standard-library key is always encodable.
	return encodePEM("PRIVATE KEY", encoded)
}

// PublicPEM exports standard SPKI PUBLIC KEY PEM without private material.
// It returns nil for a zero identity and detached bytes for a valid identity.
//
// Example:
//
//	identity, err := key.LoadSigning("signing.pem")
//	if err != nil {
//		return err
//	}
//	fmt.Print(string(identity.PublicPEM()))
func (identity Signing) PublicPEM() []byte {
	if identity.Err() != nil {
		return nil
	}

	encoded, _ := x509.MarshalPKIXPublicKey(identity.PublicKey())

	// Standard-library P-256 public keys are always encodable.
	return encodePEM("PUBLIC KEY", encoded)
}

// VAPIDPublicKey exports the uncompressed P-256 public point as unpadded
// base64url for a browser's applicationServerKey. This is not PEM or DER.
// A zero identity returns an empty string. No private material is exported.
//
// Example:
//
//	identity, err := key.LoadSigning("vapid.pem")
//	if err != nil {
//		return err
//	}
//	fmt.Println(identity.VAPIDPublicKey())
func (identity Signing) VAPIDPublicKey() string {
	public := identity.PublicKey()

	if public == nil {
		return ""
	}

	point, _ := public.Bytes()

	// The public point comes from a validated private scalar.
	return base64.RawURLEncoding.EncodeToString(point)
}

// String redacts the identity for ordinary formatting, including zero values.
// Use explicit exports when material is needed, not formatting.
//
// Example:
//
//	fmt.Println(key.Signing{}.String()) // [REDACTED signing key]
func (identity Signing) String() string {
	return "[REDACTED signing key]"
}

// GoString redacts the identity for Go-syntax formatting, including zero
// values. It never reveals the underlying scalar.
//
// Example:
//
//	fmt.Printf("%#v", key.Signing{}) // [REDACTED signing key]
func (identity Signing) GoString() string {
	return identity.String()
}

// LogValue redacts the identity in structured logs. Logging is not a public
// key export operation and never reveals private or public material.
//
// Example:
//
//	slog.Info("configuration", "identity", key.Signing{})
func (identity Signing) LogValue() slog.Value {
	return slog.StringValue(identity.String())
}
