package server

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"reflect"
)

// validateTLS catches missing credentials without executing application TLS
// callbacks. Dynamic credentials and complete TLS policy remain handshake-time
// concerns; a configured callback is sufficient even without static identities.
func validateTLS(config *tls.Config) error {
	if config == nil {
		return nil
	}

	if len(config.Certificates) == 0 && config.GetCertificate == nil && config.GetConfigForClient == nil {
		return errors.New("certificates, GetCertificate, or GetConfigForClient required")
	}

	for index := range config.Certificates {
		if err := validateTLSCertificate(&config.Certificates[index]); err != nil {
			return fmt.Errorf("certificate %d: %w", index, err)
		}
	}

	return nil
}

// validateTLSCertificate rejects an empty or malformed static chain and absent
// or unsupported private keys, including typed nils of any nil-capable kind.
// It does not prove key correspondence, chain trust, hostname coverage, expiry,
// or compatibility with every client. Native crypto.Signer and legacy RSA
// crypto.Decrypter implementations stay supported without invoking key methods.
func validateTLSCertificate(certificate *tls.Certificate) error {
	if len(certificate.Certificate) == 0 {
		return errors.New("empty certificate chain")
	}

	for _, encoded := range certificate.Certificate {
		if _, err := x509.ParseCertificate(encoded); err != nil {
			return fmt.Errorf("invalid certificate chain: %w", err)
		}
	}

	key := reflect.ValueOf(certificate.PrivateKey)

	if !key.IsValid() {
		return errors.New("missing private key")
	}

	switch key.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if key.IsNil() {
			return errors.New("missing private key")
		}
	}

	switch certificate.PrivateKey.(type) {
	case crypto.Signer, crypto.Decrypter:
		return nil
	default:
		return errors.New("private key must implement crypto.Signer or crypto.Decrypter")
	}
}
