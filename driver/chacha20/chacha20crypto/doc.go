// Package chacha20crypto provides a ChaCha20-Poly1305 [crypto.Driver].
//
// [Driver] encrypts raw bytes. Wrap it with [crypto.New] for the
// JSON-typed encryption facade.
// New accepts a required key.Ring and ordered Builder callbacks. The ring's
// current key encrypts; Decrypt tries the current key and then each previous
// key and reports previous=true when an older key succeeded, so the caller can
// re-encrypt after a rotation. The ciphertext format carries no key IDs. AdditionalData
// replaces and copies AAD, while omitted callbacks bind no AAD. Publication
// detaches the draft. Stop clears owned raw key material, not native key copies.
// It waits for active operations even when its context has expired, then future
// operations return crypto.ErrEncrypterClosed. Keep New's pointer rather than
// copying synchronization state. Pointer formatting redacts owned credentials
// with a fixed marker; caller-owned objects are not recursively sanitized.
//
// Example:
//
//	ring, err := key.ParseRing[key.Shared](os.Getenv("APP_ENCRYPTION_KEYS"))
//	if err != nil {
//		return err
//	}
//	driver, err := chacha20crypto.New(ring, func(encrypter *chacha20crypto.Builder) {
//		encrypter.AdditionalData([]byte("example-context"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Stop(context.Background())
//
// Provision the persistent keys once, outside the server startup path.
package chacha20crypto
