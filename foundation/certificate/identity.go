package certificate

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
)

// Identity is an immutable leaf-first certificate chain and matching private
// key. The zero value is invalid. Formatting and structured logging are redacted;
// PrivatePEM and TLS explicitly export secrets and must not be logged.
//
// Example:
//
//	identity, err := certificate.LoadIdentity("client.pem", "client-key.pem")
//	if err != nil {
//		return err
//	}
//	pair, err := identity.TLS()
//	if err != nil {
//		return err
//	}
//	_ = tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
type Identity struct {
	// public owns the certificate DER; sharing it is safe because it is immutable.
	public Bundle

	// private owns canonical unencrypted PKCS #8 DER as an immutable string.
	private string
}

// ParseIdentity imports a matching RSA, ECDSA, or Ed25519 identity. Certificates
// must be leaf-first; adjacent supplied certificates must have matching issuer
// names and valid issuer signatures. An incomplete chain is allowed. Each input
// is bounded to one MiB and copied. Keys accept unencrypted PKCS #1, SEC 1, and
// PKCS #8 PEM. Junk, headers, extra key blocks, unsupported algorithms, and
// mismatches return ErrInvalid without private parser diagnostics.
//
// Parsing does not check time, trust, revocation, or hostname coverage. It accepts
// client identities and expired certificates; verify them separately as needed.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	identity, err := certificate.ParseIdentity(root.Bundle().PEM(), root.PrivatePEM())
//	if err != nil {
//		return err
//	}
//	_ = identity.Bundle()
func ParseIdentity(certPEM, keyPEM []byte) (Identity, error) {
	certs, err := parseCertificates(certPEM)

	if err != nil {
		return Identity{}, err
	}

	private, err := parsePrivate(keyPEM)

	if err != nil {
		return Identity{}, ErrInvalid
	}

	if err := validateKey(private, certs[0]); err != nil {
		return Identity{}, err
	}

	der, err := x509.MarshalPKCS8PrivateKey(private)

	if err != nil {
		return Identity{}, ErrInvalid
	}

	if err := validateChain(certs); err != nil {
		return Identity{}, err
	}

	return Identity{public: bundleFrom(certs), private: string(der)}, nil
}

// validateKey binds a parsed signing key to the first certificate using native
// public-key equality, not byte equality of potentially different SPKI encodings.
// RSA, ECDSA, and Ed25519 implement both interfaces; exchange-only keys do not.
// No private-key parsing or diagnostics are repeated at this boundary.
func validateKey(private any, leaf *x509.Certificate) error {
	signer, ok := private.(crypto.Signer)

	if !ok {
		return ErrInvalid
	}

	public, ok := signer.Public().(interface{ Equal(crypto.PublicKey) bool })

	if !ok {
		return ErrInvalid
	}

	if !public.Equal(leaf.PublicKey) {
		return ErrInvalid
	}

	return nil
}

// validateChain checks supplied order and cryptographic linkage, not trust or
// time. A final certificate need not be a self-signed root, allowing a partial
// TLS chain without the root that clients already have.
func validateChain(certs []*x509.Certificate) error {
	for index := 1; index < len(certs); index++ {
		child, parent := certs[index-1], certs[index]

		if !bytes.Equal(child.RawIssuer, parent.RawSubject) {
			return ErrInvalid
		}

		if err := child.CheckSignatureFrom(parent); err != nil {
			return ErrInvalid
		}
	}

	return nil
}

// LoadIdentity reads explicit certificate and private-key paths, then applies
// ParseIdentity. Each read is bounded to one MiB. It never writes or repairs
// missing files; OS causes are preserved under errors.Is and errors.As.
//
// Example:
//
//	identity, err := certificate.LoadIdentity("server.pem", "server-key.pem")
//	if err != nil {
//		return err
//	}
//	_ = identity.Bundle().Certificates()
func LoadIdentity(certPath, keyPath string) (Identity, error) {
	certPEM, err := readFile(certPath)

	if err != nil {
		return Identity{}, fmt.Errorf("load identity certificate: %w", err)
	}

	keyPEM, err := readFile(keyPath)

	if err != nil {
		return Identity{}, fmt.Errorf("load identity private key: %w", err)
	}

	return ParseIdentity(certPEM, keyPEM)
}

// Err rejects the zero Identity with ErrInvalid. A nil result means encoding
// and key matching were checked at construction, not that the identity is trusted
// or currently valid. Those properties require application verification.
//
// Example:
//
//	var identity certificate.Identity
//	if err := identity.Err(); err != nil {
//		// Provision or load the identity before using it.
//	}
func (identity Identity) Err() error {
	if identity.private == "" {
		return ErrInvalid
	}

	return identity.public.Err()
}

// TLS creates a fully detached native TLS identity, including the private key,
// leaf, and chain bytes. Mutating the result cannot affect later exports. Zero
// returns ErrInvalid; native private-key parser diagnostics never escape.
//
// Example:
//
//	identity, err := certificate.LoadIdentity("server.pem", "server-key.pem")
//	if err != nil {
//		return err
//	}
//	pair, err := identity.TLS()
//	if err != nil {
//		return err
//	}
//	_ = pair.Leaf.Subject
func (identity Identity) TLS() (tls.Certificate, error) {
	if err := identity.Err(); err != nil {
		return tls.Certificate{}, err
	}

	pair, err := tls.X509KeyPair(identity.public.PEM(), identity.PrivatePEM())

	if err != nil {
		return tls.Certificate{}, ErrInvalid
	}

	return pair, nil
}

// Bundle returns only the public chain in leaf-first order. The immutable value
// can be shared safely; all of its byte and native exports are detached. Zero
// returns a zero Bundle, whose Err reports ErrInvalid.
//
// Example:
//
//	identity, err := certificate.LoadIdentity("server.pem", "server-key.pem")
//	if err != nil {
//		return err
//	}
//	publicPEM := identity.Bundle().PEM()
//	_ = publicPEM
func (identity Identity) Bundle() Bundle {
	return identity.public
}

// PrivatePEM explicitly exports the secret as a new, unencrypted PKCS #8 PRIVATE
// KEY buffer, regardless of import format. Zero returns nil. Protect the result
// like the original key; ordinary formatting is not a substitute for this call.
//
// Example:
//
//	identity, err := certificate.LoadIdentity("server.pem", "server-key.pem")
//	if err != nil {
//		return err
//	}
//	secretPEM := identity.PrivatePEM()
//	_ = secretPEM // Pass only to protected key storage, not logging.
func (identity Identity) PrivatePEM() []byte {
	if identity.private == "" {
		return nil
	}

	return encodePEM("PRIVATE KEY", []byte(identity.private))
}

// String redacts all identity material, including public identifying names.
// The constant output is also used for zero identities.
//
// Example:
//
//	var identity certificate.Identity
//	fmt.Println(identity.String()) // certificate.Identity([REDACTED])
func (identity Identity) String() string {
	return "certificate.Identity([REDACTED])"
}

// GoString prevents %#v from exposing the private fields of Identity. It has
// the same constant redacted representation as String, including for zero.
//
// Example:
//
//	var identity certificate.Identity
//	fmt.Printf("%#v", identity) // certificate.Identity([REDACTED])
func (identity Identity) GoString() string {
	return identity.String()
}

// LogValue supplies a constant redacted slog value. It prevents structured
// logging from traversing the identity's private key or certificate names.
//
// Example:
//
//	var identity certificate.Identity
//	value := identity.LogValue()
//	_ = value.String() // certificate.Identity([REDACTED])
func (identity Identity) LogValue() slog.Value {
	return slog.StringValue(identity.String())
}
