// Package certificate provisions local TLS certificates and imports existing
// certificate identities without changing files or operating-system trust.
// An Identity owns a leaf-first public Bundle and a private key. Bundle never
// contains a private key. Both values are immutable and safe for concurrent
// reads; their zero values are invalid. Native exports are independent copies.
// Bundle.Pool selects private-only trust; Bundle.SystemPool adds the bundle to
// system roots without modifying the operating-system store. System loading
// failures are returned rather than falling back to private-only trust.
//
// GenerateRoot and GenerateServer provide a small local-development CA profile,
// not a production PKI, ACME client, renewal service, or intermediate issuer.
// Parsing checks encoding, chain order, signatures between supplied certificates,
// and key matching. It does not establish trust, hostname coverage, revocation,
// or current validity; use crypto/x509 verification for those policies.
//
// Example:
//
//	root, err := certificate.GenerateRoot("Example development")
//	if err != nil {
//		return err
//	}
//	server, err := certificate.GenerateServer(root, []string{"localhost", "127.0.0.1"})
//	if err != nil {
//		return err
//	}
//	pair, err := server.TLS()
//	if err != nil {
//		return err
//	}
//	_ = tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
package certificate
