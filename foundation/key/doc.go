// Package key generates, parses, and loads immutable cryptographic key values.
//
// Shared holds exactly 32 bytes for symmetric encryption or signing;
// Signing holds an ECDSA P-256 identity and Exchange holds an X25519 identity.
// Generation uses crypto/rand. Parsing validates representation and material,
// not entropy or provisioning authority. Generate separate keys for separate purposes.
//
// Load functions read explicit files; they never generate missing keys, inspect
// environment variables, or rotate identities. Private material is exported only
// through explicitly named methods. Formatting and structured logging redact it.
// Exported byte slices and native keys are independent copies. The zero values
// are invalid; consumers must check Err before using exported material.
//
// Exchange exposes native ECDH keys, not an encryption protocol. Applications
// own peer authentication, protocol-specific key derivation, and transport.
// Never pass raw ECDH output directly to an encryption driver.
//
// Example:
//
//	material, err := key.GenerateShared()
//	if err != nil {
//		return err
//	}
//	restored, err := key.ParseShared(material.Base64())
//	if err != nil {
//		return err
//	}
//	_ = restored
package key
