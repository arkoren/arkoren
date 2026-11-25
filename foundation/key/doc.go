// Package key generates, parses, and loads immutable cryptographic key values.
//
// Shared holds exactly 32 bytes for symmetric encryption or signing;
// Signing holds an ECDSA P-256 identity and Exchange holds an X25519 identity.
// Generation uses crypto/rand. Parsing validates representation and material,
// not entropy or provisioning authority. Generate separate keys for separate purposes.
//
// Ring orders keys for rotation: the first is current and encrypts or signs new
// data, the rest are previous and only decrypt or verify. Its text form is
// comma-separated padded base64 keys on one line, so a single [Shared] encoding
// is a valid one-key ring. Shared.Fingerprint is a domain-separated SHA-256 that
// is safe to display when identifying a key. Ring and Signing implement
// encoding.TextUnmarshaler for text decoders; neither can be marshaled, to
// prevent accidental serialization.
//
// Load functions read explicit files; they never generate missing keys, inspect
// environment variables, or rotate identities. Private material is exported only
// through explicitly named methods. Every formatting verb and structured logging
// redact it, including when a value sits in an unexported struct field: the
// material is held behind a pointer, so values copy cheaply but compare with ==
// by identity, not by contents.
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
