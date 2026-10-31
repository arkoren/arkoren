package certificate

import (
	"crypto/x509"
	"fmt"
)

// SystemPool loads the system trust anchors and adds this Bundle's certificates
// to an independent pool. Unlike Pool, it retains public system trust. The zero
// Bundle returns ErrInvalid before loading roots. System loading errors retain
// their causes; there is no fallback to a private-only pool.
//
// Added certificates are detached from the Bundle. Changes to the returned pool
// cannot change the Bundle, other exports, or operating-system trust. Choosing
// additional trust anchors remains the caller's security decision.
//
// Example:
//
//	public, err := certificate.LoadBundle("development-ca.pem")
//	if err != nil {
//		return err
//	}
//	pool, err := public.SystemPool()
//	if err != nil {
//		return err
//	}
//	_ = tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
func (public Bundle) SystemPool() (*x509.CertPool, error) {
	return public.systemPool(x509.SystemCertPool)
}

// systemPool keeps root loading at a replaceable I/O boundary for isolated
// tests, without mutable package hooks. load must return a nonnil pool on
// success, as x509.SystemCertPool does. The loaded pool is owned by this call.
func (public Bundle) systemPool(load func() (*x509.CertPool, error)) (*x509.CertPool, error) {
	if err := public.Err(); err != nil {
		return nil, err
	}

	pool, err := load()

	if err != nil {
		return nil, fmt.Errorf("load system certificate pool: %w", err)
	}

	for _, cert := range public.Certificates() {
		pool.AddCert(cert)
	}

	return pool, nil
}
