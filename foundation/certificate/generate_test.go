package certificate_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/foundation/certificate"
)

// rootTemplate supplies a valid external local-root profile so each test can
// vary one issuance precondition without coupling to production generation.
func rootTemplate() *x509.Certificate {
	now := time.Now().UTC().Truncate(time.Second)

	return &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "External development root"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
}

// importedRoot creates an external self-signed fixture and checks that import
// itself accepts it. Individual tests then exercise the stricter issuer policy.
func importedRoot(t *testing.T, template *x509.Certificate) certificate.Identity {
	t.Helper()

	certPEM, keyPEM := nativePEM(t, template, nativeSigner(t))
	identity, err := certificate.ParseIdentity(certPEM, keyPEM)

	if err != nil {
		t.Fatal(err)
	}

	return identity
}

// TestGenerateServerExternalConstrainedRoot permits imported roots that satisfy
// the local issuance profile; no hidden provenance or generation marker exists.
func TestGenerateServerExternalConstrainedRoot(t *testing.T) {
	t.Parallel()

	root := importedRoot(t, rootTemplate())
	leaf, err := certificate.GenerateServer(root, []string{"localhost"})

	if err != nil {
		t.Fatal(err)
	}

	certs := leaf.Bundle().Certificates()

	if err := certs[0].CheckSignatureFrom(certs[1]); err != nil {
		t.Fatal(err)
	}

	if certs[0].NotBefore.Before(certs[1].NotBefore) {
		t.Fatal("leaf backdating exceeded issuer interval")
	}
}

// TestGenerateServerNotYetValidIssuer rejects future-dated roots without
// imposing the same current-validity restriction on ParseIdentity.
func TestGenerateServerNotYetValidIssuer(t *testing.T) {
	t.Parallel()

	template := rootTemplate()
	template.NotBefore = time.Now().Add(time.Hour)
	root := importedRoot(t, template)

	if _, err := certificate.GenerateServer(root, []string{"localhost"}); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("future issuer: %v", err)
	}
}

// TestGenerateServerNonCAIssuer checks authorization independently of chain
// length: this fixture is a single certificate with a matching private key.
func TestGenerateServerNonCAIssuer(t *testing.T) {
	t.Parallel()

	root := importedRoot(t, clientTemplate())

	if _, err := certificate.GenerateServer(root, []string{"localhost"}); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("non-CA issuer: %v", err)
	}
}

// TestGenerateServerIssuerRequiresCertificateSigning rejects a self-signed CA
// whose key usage explicitly does not permit signing certificates.
func TestGenerateServerIssuerRequiresCertificateSigning(t *testing.T) {
	t.Parallel()

	template := rootTemplate()
	template.KeyUsage = x509.KeyUsageDigitalSignature
	root := importedRoot(t, template)

	if _, err := certificate.GenerateServer(root, []string{"localhost"}); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("unauthorized CA key usage: %v", err)
	}
}

// TestGenerateServerIssuerRequiresPathLengthZero rejects an otherwise valid
// unconstrained CA rather than making this helper an intermediate-issuance API.
func TestGenerateServerIssuerRequiresPathLengthZero(t *testing.T) {
	t.Parallel()

	template := rootTemplate()
	template.MaxPathLenZero = false
	template.MaxPathLen = -1
	root := importedRoot(t, template)

	if _, err := certificate.GenerateServer(root, []string{"localhost"}); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("unconstrained CA issuer: %v", err)
	}
}

// TestGenerateServerIssuerRequiresSelfSignature imports a certificate whose
// names claim self-issuance but whose signature was made by another key.
func TestGenerateServerIssuerRequiresSelfSignature(t *testing.T) {
	t.Parallel()

	private, other := nativeSigner(t), nativeSigner(t)
	template := rootTemplate()
	der, err := x509.CreateCertificate(rand.Reader, template, template, private.Public(), other)

	if err != nil {
		t.Fatal(err)
	}

	_, keyPEM := nativePEM(t, template, private)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	root, err := certificate.ParseIdentity(certPEM, keyPEM)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := certificate.GenerateServer(root, []string{"localhost"}); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("invalid self-signature: %v", err)
	}
}

// ExampleGenerateServer shows local provisioning and explicit client trust.
// It verifies the leaf without opening a listener or changing system roots.
func ExampleGenerateServer() {
	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		log.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost", "127.0.0.1"})

	if err != nil {
		log.Fatal(err)
	}

	pool, err := root.Bundle().Pool()

	if err != nil {
		log.Fatal(err)
	}

	cert := leaf.Bundle().Certificates()[0]

	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err != nil {
		log.Fatal(err)
	}

	fmt.Println(cert.DNSNames[0])
	// Output: localhost
}

// TestGenerateRootProfile checks root validity, serial bounds, and self-signing.
// Fake time makes the validity assertions independent of wall-clock boundaries.
func TestGenerateRootProfile(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		root, err := certificate.GenerateRoot("Example development")

		if err != nil {
			t.Fatal(err)
		}

		cert := root.Bundle().Certificates()[0]

		if !cert.NotBefore.Equal(time.Now().Add(-5*time.Minute)) ||
			!cert.NotAfter.Equal(time.Now().Add(5*365*24*time.Hour)) {
			t.Fatal("unexpected root validity")
		}

		if cert.SerialNumber.Sign() != 1 || len(cert.SerialNumber.Bytes()) > 20 {
			t.Fatal("invalid serial number")
		}

		if err := cert.CheckSignatureFrom(cert); err != nil {
			t.Fatal(err)
		}
	})
}

// TestGenerateRootUsesP256 checks the generated private algorithm independently
// of the certificate profile, so algorithm regressions are reported directly.
func TestGenerateRootUsesP256(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	pair, err := root.TLS()

	if err != nil {
		t.Fatal(err)
	}

	private, ok := pair.PrivateKey.(*ecdsa.PrivateKey)

	if !ok || private.Curve != elliptic.P256() {
		t.Fatal("root is not ECDSA P-256")
	}
}

// TestGenerateServerProfile verifies explicit SANs, server-only use, and expiry.
// Fake time makes the default lifetime check exact without real waits.
func TestGenerateServerProfile(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		root, err := certificate.GenerateRoot("Example development")

		if err != nil {
			t.Fatal(err)
		}

		leaf, err := certificate.GenerateServer(root, []string{"localhost", "127.0.0.1", "::1"})

		if err != nil {
			t.Fatal(err)
		}

		chain := leaf.Bundle().Certificates()

		if len(chain) != 2 || chain[0].IsCA || chain[0].KeyUsage != x509.KeyUsageDigitalSignature {
			t.Fatal("invalid leaf profile")
		}

		if !reflect.DeepEqual(chain[0].ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
			t.Fatal("leaf is not server-auth only")
		}

		if !reflect.DeepEqual(chain[0].DNSNames, []string{"localhost"}) || len(chain[0].IPAddresses) != 2 {
			t.Fatal("unexpected SANs")
		}

		if !chain[0].NotAfter.Equal(time.Now().Add(90 * 24 * time.Hour)) {
			t.Fatal("unexpected leaf expiry")
		}
	})
}

// TestGenerateRootConstraints checks that the root can sign certificates but
// cannot authorize intermediate CAs or claim application extended key usages.
func TestGenerateRootConstraints(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	cert := root.Bundle().Certificates()[0]

	if !cert.IsCA || !cert.BasicConstraintsValid || !cert.MaxPathLenZero || cert.MaxPathLen != 0 {
		t.Fatalf("invalid CA constraints: %+v", cert)
	}

	if cert.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign || len(cert.ExtKeyUsage) != 0 {
		t.Fatal("unexpected root key usage")
	}
}

// TestGenerateServerVerification validates a leaf against an explicitly chosen
// root pool, confirming that the generated chain is usable by crypto/x509.
func TestGenerateServerVerification(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost"})

	if err != nil {
		t.Fatal(err)
	}

	pool, err := root.Bundle().Pool()

	if err != nil {
		t.Fatal(err)
	}

	_, err = leaf.Bundle().Certificates()[0].Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"})

	if err != nil {
		t.Fatal(err)
	}
}

// TestGenerateServerOnlyRequestedHosts ensures generation never silently adds
// loopback SANs or copies DNS names from the issuer's common name.
func TestGenerateServerOnlyRequestedHosts(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("localhost")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"api.example.test"})

	if err != nil {
		t.Fatal(err)
	}

	cert := leaf.Bundle().Certificates()[0]

	if !reflect.DeepEqual(cert.DNSNames, []string{"api.example.test"}) || len(cert.IPAddresses) != 0 {
		t.Fatal("generation added hosts")
	}

	if err := cert.VerifyHostname("localhost"); err == nil {
		t.Fatal("unexpected localhost coverage")
	}
}

// TestGenerateServerWildcard checks the documented single leading wildcard
// syntax without accepting arbitrary wildcard placement inside labels.
func TestGenerateServerWildcard(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"*.example.test"})

	if err != nil {
		t.Fatal(err)
	}

	if err := leaf.Bundle().Certificates()[0].VerifyHostname("api.example.test"); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateServerIPOnly checks that IP literals are encoded as IP SANs, not
// DNS SAN strings, including the IPv6 loopback literal.
func TestGenerateServerIPOnly(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"::1"})

	if err != nil {
		t.Fatal(err)
	}

	cert := leaf.Bundle().Certificates()[0]

	if len(cert.DNSNames) != 0 || !cert.IPAddresses[0].Equal(net.ParseIP("::1")) {
		t.Fatal("IP literal was not an IP SAN")
	}
}

// TestGenerateServerDefaultClamps checks that omitted validity does not outlive
// a short-lived root, and backdating stays inside the issuer interval.
func TestGenerateServerDefaultClamps(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
		authority.Validity(time.Hour)
	})

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost"})

	if err != nil {
		t.Fatal(err)
	}

	parent, child := root.Bundle().Certificates()[0], leaf.Bundle().Certificates()[0]

	if !child.NotAfter.Equal(parent.NotAfter) || child.NotBefore.Before(parent.NotBefore) {
		t.Fatal("leaf exceeds issuer interval")
	}
}

// TestGenerateServerExplicitExcessiveValidity rejects an explicit lifetime
// instead of silently shortening the requested validity.
func TestGenerateServerExplicitExcessiveValidity(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
		authority.Validity(time.Hour)
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = certificate.GenerateServer(root, []string{"localhost"}, func(leaf *certificate.ServerBuilder) {
		leaf.Validity(2 * time.Hour)
	})

	if !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("excessive validity: %v", err)
	}
}

// TestGenerateServerExpiredIssuer advances fake time rather than waiting for
// a real root expiry. Parsing remains valid after issuance is forbidden.
func TestGenerateServerExpiredIssuer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		root, err := certificate.GenerateRoot("Example development", func(authority *certificate.RootBuilder) {
			authority.Validity(time.Second)
		})

		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(2 * time.Second)

		if _, err := certificate.ParseIdentity(root.Bundle().PEM(), root.PrivatePEM()); err != nil {
			t.Fatal(err)
		}

		if _, err := certificate.GenerateServer(root, []string{"localhost"}); !errors.Is(err, certificate.ErrInvalid) {
			t.Fatalf("expired issuer: %v", err)
		}
	})
}

// TestGenerateServerRejectsLeafIssuer ensures a server identity cannot be used
// as an intermediate issuer even though its key matches its certificate.
func TestGenerateServerRejectsLeafIssuer(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	leaf, err := certificate.GenerateServer(root, []string{"localhost"})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := certificate.GenerateServer(leaf, []string{"localhost"}); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("leaf issuer: %v", err)
	}
}

// TestGenerateServerInvalidHosts rejects bare-host syntax violations with the
// same outcome. These inputs do not exercise different normalization policies.
func TestGenerateServerInvalidHosts(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	invalidHosts := []string{
		"", "https://localhost", "localhost:443", " localhost", "bad..test",
		"-bad", "bad-", "a*b", "é.test", "[::1]",
	}

	for _, host := range invalidHosts {
		if _, err := certificate.GenerateServer(root, []string{host}); !errors.Is(err, certificate.ErrInvalid) {
			t.Fatalf("invalid host %q: %v", host, err)
		}
	}
}

// TestGenerateServerRequiresHosts ensures generation does not infer a SAN set
// when the caller supplies no names.
func TestGenerateServerRequiresHosts(t *testing.T) {
	t.Parallel()

	root, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := certificate.GenerateServer(root, nil); !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("missing hosts: %v", err)
	}
}

// TestGenerateRootInvalidNames checks the documented root display-name bounds.
// All inputs are invalid rather than aliases that generation normalizes.
func TestGenerateRootInvalidNames(t *testing.T) {
	t.Parallel()

	invalidNames := []string{
		"", " \t", "bad\x00name", "bad\nname", "bad\u0085name", string([]byte{0xff}),
		string(make([]byte, 65)),
	}

	for _, name := range invalidNames {
		if _, err := certificate.GenerateRoot(name); !errors.Is(err, certificate.ErrInvalid) {
			t.Fatalf("invalid name %q: %v", name, err)
		}
	}
}

// TestGenerateRootRandomSerials checks independent generations rather than
// assuming any particular random output or serial-number distribution.
func TestGenerateRootRandomSerials(t *testing.T) {
	t.Parallel()

	first, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	second, err := certificate.GenerateRoot("Example development")

	if err != nil {
		t.Fatal(err)
	}

	if first.Bundle().Certificates()[0].SerialNumber.Cmp(second.Bundle().Certificates()[0].SerialNumber) == 0 {
		t.Fatal("independent roots reused a serial")
	}
}
