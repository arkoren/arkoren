package certificate

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// rootLifetime is five fixed 365-day years, not calendar-year arithmetic.
const rootLifetime = 5 * 365 * 24 * time.Hour

// serverLifetime limits the default leaf lifetime to ninety fixed days.
const serverLifetime = 90 * 24 * time.Hour

// clockSkew backdates NotBefore by five minutes, without extending NotAfter.
// A leaf's NotBefore is additionally bounded by its issuer's NotBefore.
const clockSkew = 5 * time.Minute

// GenerateRoot provisions a self-signed ECDSA P-256 local-development root.
// name is a nonblank common name of at most 64 bytes without control characters.
// The default expiry is generation time plus 5*365 days. NotBefore is backdated
// five minutes for clock skew; all certificate timestamps use whole seconds.
// The root has certificate-signing usage, valid CA constraints, and pathLen=0,
// prohibiting intermediate CAs. No files or OS trust stores are changed.
//
// Callbacks run once in order before validation, including for invalid names;
// nil entries panic and application panics propagate. Invalid settings return
// ErrInvalid; cryptographic generation errors are returned to the caller.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	_ = root.Bundle().PEM() // Public material only; protect root.PrivatePEM().
func GenerateRoot(name string, configure ...RootBuilderFunc) (Identity, error) {
	authority := &RootBuilder{}

	for _, apply := range configure {
		apply(authority)
	}

	if authority.err != nil {
		return Identity{}, authority.err
	}

	if err := validateName(name); err != nil {
		return Identity{}, err
	}

	lifetime := authority.validity

	if lifetime == 0 {
		lifetime = rootLifetime
	}

	now := time.Now().UTC().Truncate(time.Second)
	template, err := newTemplate(now, lifetime)

	if err != nil {
		return Identity{}, err
	}

	template.Subject = pkix.Name{CommonName: name}
	template.IsCA = true
	template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	template.MaxPathLen = 0
	template.MaxPathLenZero = true

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		return Identity{}, err
	}

	return issue(template, template, private, private)
}

// GenerateServer provisions an ECDSA P-256 server-auth-only leaf for explicit
// DNS names or IP addresses. Hosts must be nonempty bare names/addresses, not
// URLs or host:port values. DNS names use ASCII labels; a leading *. wildcard
// is allowed. No localhost, loopback, or other SAN is added implicitly.
//
// issuer must be one currently valid, self-signed root with matching key,
// certificate-signing usage, and CA pathLen=0 constraints. The default expiry
// is generation time plus 90 days, clamped to issuer expiry. Explicit excessive
// validity fails with ErrInvalid. NotBefore is backdated five minutes but never
// precedes the issuer. The returned chain is leaf then root. Issuance does not
// install trust, write files, renew certificates, or validate DNS ownership.
//
// Callbacks run once in order before validation; nil entries and application
// panics propagate. Invalid inputs return ErrInvalid; crypto errors propagate.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	leaf, err := certificate.GenerateServer(root, []string{"localhost", "127.0.0.1", "::1"})
//	if err != nil {
//		return err
//	}
//	_ = leaf.Bundle().Certificates()[0].DNSNames
func GenerateServer(issuer Identity, hosts []string, configure ...ServerBuilderFunc) (Identity, error) {
	leaf := &ServerBuilder{}

	for _, apply := range configure {
		apply(leaf)
	}

	if leaf.err != nil {
		return Identity{}, leaf.err
	}

	now := time.Now().UTC().Truncate(time.Second)
	parent, signer, err := signingRoot(issuer, now)

	if err != nil {
		return Identity{}, err
	}

	template, err := serverTemplate(parent, now, leaf.validity)

	if err != nil {
		return Identity{}, err
	}

	if err := setHosts(template, hosts); err != nil {
		return Identity{}, err
	}

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		return Identity{}, err
	}

	return issue(template, parent, signer, private)
}

// validateName prevents empty or control-bearing local root labels. Names are
// display labels, not DNS authority assertions or a source of implicit SANs.
func validateName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 64 {
		return ErrInvalid
	}

	if !utf8.ValidString(name) {
		return ErrInvalid
	}

	for _, character := range name {
		if unicode.IsControl(character) {
			return ErrInvalid
		}
	}

	return nil
}

// newTemplate creates common certificate fields and a uniformly random positive
// serial of at most 159 bits (twenty DER bytes without a sign-padding byte).
// lifetime is already validated; now is whole-second UTC generation time.
func newTemplate(now time.Time, lifetime time.Duration) (*x509.Certificate, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 159)
	limit.Sub(limit, big.NewInt(1))

	serial, err := rand.Int(rand.Reader, limit)

	if err != nil {
		return nil, err
	}

	serial.Add(serial, big.NewInt(1))

	return &x509.Certificate{
		SerialNumber:          serial,
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(lifetime).Truncate(time.Second),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}, nil
}

// signingRoot checks the narrow local-issuance profile, unlike ParseIdentity,
// which intentionally permits expired and client certificates. Native exports
// already check key matching; no trust-store lookup occurs here.
func signingRoot(issuer Identity, now time.Time) (*x509.Certificate, crypto.Signer, error) {
	pair, err := issuer.TLS()

	if err != nil {
		return nil, nil, err
	}

	if len(pair.Certificate) != 1 {
		return nil, nil, ErrInvalid
	}

	parent := pair.Leaf

	if err := validateRoot(parent, now); err != nil {
		return nil, nil, err
	}

	signer, ok := pair.PrivateKey.(crypto.Signer)

	if !ok {
		return nil, nil, ErrInvalid
	}

	return parent, signer, nil
}

// validateRoot enforces CA authorization, path length, current validity, and
// self-signature before permitting the local root to issue a server leaf.
func validateRoot(parent *x509.Certificate, now time.Time) error {
	if !parent.IsCA || !parent.BasicConstraintsValid {
		return ErrInvalid
	}

	if !parent.MaxPathLenZero || parent.MaxPathLen != 0 {
		return ErrInvalid
	}

	if parent.KeyUsage&x509.KeyUsageCertSign == 0 {
		return ErrInvalid
	}

	if now.Before(parent.NotBefore) || !now.Before(parent.NotAfter) {
		return ErrInvalid
	}

	if !bytes.Equal(parent.RawIssuer, parent.RawSubject) {
		return ErrInvalid
	}

	if err := parent.CheckSignatureFrom(parent); err != nil {
		return ErrInvalid
	}

	return nil
}

// serverTemplate limits leaf times to the parent. Zero lifetime alone permits
// default clamping; any explicit duration extending past issuer expiry fails.
func serverTemplate(parent *x509.Certificate, now time.Time, lifetime time.Duration) (*x509.Certificate, error) {
	if lifetime > parent.NotAfter.Sub(now) {
		return nil, ErrInvalid
	}

	if lifetime == 0 {
		lifetime = min(serverLifetime, parent.NotAfter.Sub(now))
	}

	template, err := newTemplate(now, lifetime)

	if err != nil {
		return nil, err
	}

	if template.NotBefore.Before(parent.NotBefore) {
		template.NotBefore = parent.NotBefore
	}

	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}

	return template, nil
}

// setHosts classifies explicit IP literals and validates DNS SANs. It copies
// input values into the native template and does not retain the caller's slice.
func setHosts(template *x509.Certificate, hosts []string) error {
	if len(hosts) == 0 {
		return ErrInvalid
	}

	for _, host := range hosts {
		if address := net.ParseIP(host); address != nil {
			template.IPAddresses = append(template.IPAddresses, address)

			continue
		}

		if err := validateDNS(host); err != nil {
			return err
		}

		template.DNSNames = append(template.DNSNames, host)
	}

	return nil
}

// validateDNS accepts ASCII hostname labels and one optional leading wildcard.
// Unicode domains must be supplied in their ASCII (punycode) representation.
func validateDNS(host string) error {
	if len(host) == 0 || len(host) > 253 {
		return ErrInvalid
	}

	host = strings.TrimPrefix(host, "*.")

	for label := range strings.SplitSeq(host, ".") {
		if err := validateLabel(label); err != nil {
			return err
		}
	}

	return nil
}

// validateLabel rejects empty, oversized, edge-hyphen, and non-ASCII labels.
// Its character test follows the DNS hostname alphabet, not general DNS records.
func validateLabel(label string) error {
	if len(label) == 0 || len(label) > 63 {
		return ErrInvalid
	}

	if label[0] == '-' || label[len(label)-1] == '-' {
		return ErrInvalid
	}

	if strings.Trim(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
		return ErrInvalid
	}

	return nil
}

// issue signs one generated certificate and imports its canonical identity.
// A root is exported once; a server chain includes its issuer after the leaf.
func issue(template, parent *x509.Certificate, signer crypto.Signer, private *ecdsa.PrivateKey) (Identity, error) {
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &private.PublicKey, signer)

	if err != nil {
		return Identity{}, err
	}

	certPEM := encodePEM("CERTIFICATE", der)

	if template != parent {
		certPEM = append(certPEM, encodePEM("CERTIFICATE", parent.Raw)...)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(private)

	if err != nil {
		return Identity{}, ErrInvalid
	}

	return ParseIdentity(certPEM, encodePEM("PRIVATE KEY", keyDER))
}
