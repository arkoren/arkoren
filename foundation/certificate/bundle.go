package certificate

import (
	"crypto/x509"
	"errors"
	"fmt"
)

// ErrInvalid identifies malformed PEM, mismatched identities, invalid generation
// settings, and zero values. Native private-key parser diagnostics are redacted.
// File errors instead retain their OS causes for errors.Is and errors.As.
//
// Example:
//
//	_, err := certificate.ParseBundle(nil)
//	if errors.Is(err, certificate.ErrInvalid) {
//		// Supply at least one CERTIFICATE PEM block.
//	}
var ErrInvalid = errors.New("invalid certificate material")

// Bundle owns one or more public certificates in their supplied order. It can
// represent a chain or a set of trust anchors; ParseBundle does not verify trust
// or chain relationships. Its zero value is invalid. No private key is retained.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	pool, err := root.Bundle().Pool()
//	if err != nil {
//		return err
//	}
//	_ = pool
type Bundle struct {
	// certificates owns immutable DER strings, never caller-supplied buffers.
	certificates []string
}

// ParseBundle copies a nonempty sequence of CERTIFICATE PEM blocks, up to one
// MiB including whitespace. Junk, headers, unexpected blocks, and invalid DER
// return ErrInvalid. Ordering is preserved; expiry and trust are not checked.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	public, err := certificate.ParseBundle(root.Bundle().PEM())
//	if err != nil {
//		return err
//	}
//	_ = public.Certificates()
func ParseBundle(encoded []byte) (Bundle, error) {
	certs, err := parseCertificates(encoded)

	if err != nil {
		return Bundle{}, err
	}

	return bundleFrom(certs), nil
}

// parseCertificates validates bounded public PEM once and preserves native
// parsed certificates for identity key and chain checks. Decoding owns its DER;
// no input bytes are retained. Empty input and noncertificate blocks are invalid.
func parseCertificates(encoded []byte) ([]*x509.Certificate, error) {
	if len(encoded) > maxInputSize {
		return nil, ErrInvalid
	}

	var certs []*x509.Certificate

	for len(encoded) != 0 {
		block, rest, err := decodeBlock(encoded)

		if err != nil {
			return nil, err
		}

		if block.Type != "CERTIFICATE" {
			return nil, ErrInvalid
		}

		cert, err := x509.ParseCertificate(block.Bytes)

		if err != nil {
			return nil, ErrInvalid
		}

		certs = append(certs, cert)
		encoded = rest
	}

	if len(certs) == 0 {
		return nil, ErrInvalid
	}

	return certs, nil
}

// bundleFrom publishes immutable DER snapshots of a nonempty validated native
// certificate sequence. Neither the slice nor any mutable native field escapes
// through the returned Bundle; callers retain no mutation access to its strings.
func bundleFrom(certs []*x509.Certificate) Bundle {
	encoded := make([]string, len(certs))

	for index, cert := range certs {
		encoded[index] = string(cert.Raw)
	}

	return Bundle{certificates: encoded}
}

// LoadBundle reads a public PEM file without modifying it and calls ParseBundle.
// Reads are bounded to one MiB. OS error causes remain inspectable; a valid
// encoding says nothing about whether these certificates should be trusted.
//
// Example:
//
//	public, err := certificate.LoadBundle("development-ca.pem")
//	if err != nil {
//		return err
//	}
//	_ = public.Certificates()
func LoadBundle(path string) (Bundle, error) {
	raw, err := readFile(path)

	if err != nil {
		return Bundle{}, fmt.Errorf("load certificate bundle: %w", err)
	}

	return ParseBundle(raw)
}

// Err reports ErrInvalid for the zero value and nil for a constructed Bundle.
// It checks construction validity, not expiry, hostname coverage, or trust.
//
// Example:
//
//	var public certificate.Bundle
//	if err := public.Err(); err != nil {
//		// Load or parse certificates before configuring trust.
//	}
func (public Bundle) Err() error {
	if len(public.certificates) == 0 {
		return ErrInvalid
	}

	return nil
}

// PEM exports a newly allocated sequence of CERTIFICATE blocks in stored order.
// It never includes private material and returns nil for the zero value.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	publicPEM := root.Bundle().PEM()
//	_ = publicPEM
func (public Bundle) PEM() []byte {
	var encoded []byte

	for _, der := range public.certificates {
		encoded = append(encoded, encodePEM("CERTIFICATE", []byte(der))...)
	}

	return encoded
}

// Certificates reparses owned DER to return fully detached native certificates,
// including their nested fields, public keys, and raw bytes. It returns nil for
// zero. Mutating a result cannot change this Bundle or another export.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	certs := root.Bundle().Certificates()
//	_ = certs[0].NotAfter
func (public Bundle) Certificates() []*x509.Certificate {
	var certs []*x509.Certificate

	for _, der := range public.certificates {
		// Stored DER was parsed at construction and cannot subsequently change.
		cert, _ := x509.ParseCertificate([]byte(der))
		certs = append(certs, cert)
	}

	return certs
}

// Pool creates an independent, initially empty pool containing exactly this
// Bundle's certificates. It does not load or modify system roots. Choosing
// these certificates as trust anchors is the caller's security decision.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	pool, err := root.Bundle().Pool()
//	if err != nil {
//		return err
//	}
//	_ = tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
func (public Bundle) Pool() (*x509.CertPool, error) {
	if err := public.Err(); err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()

	for _, cert := range public.Certificates() {
		pool.AddCert(cert)
	}

	return pool, nil
}
