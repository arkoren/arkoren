// Package aescrypto provides an AES-GCM [crypto.Driver].
//
// [Driver] encrypts raw bytes. Wrap it with [crypto.New] for the
// JSON-typed encryption facade.
// New accepts a required key.Shared and ordered Builder callbacks; AdditionalData
// replaces and copies AAD, while omitted callbacks bind no AAD. Publication
// detaches the draft. Stop clears owned raw key material, not native key copies.
// It waits for active operations even when its context has expired, then future
// operations return crypto.ErrEncrypterClosed. Keep New's pointer rather than
// copying synchronization state. Pointer formatting redacts owned credentials
// with a fixed marker; caller-owned objects are not recursively sanitized.
//
// Example:
//
//	material, err := key.LoadShared("app-key.key")
//	if err != nil {
//		return err
//	}
//	driver, err := aescrypto.New(material, func(encrypter *aescrypto.Builder) {
//		encrypter.AdditionalData([]byte("example-context"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Stop(context.Background())
//
// Provision the persistent key once, outside the server startup path.
package aescrypto
