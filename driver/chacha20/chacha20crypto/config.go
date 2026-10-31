package chacha20crypto

import "slices"

// Builder configures the authenticated context of one [New] construction.
// New publishes an independent copy; retaining this draft cannot change it.
// The zero draft binds no additional data.
//
// Example:
//
//	driver, err := chacha20crypto.New(material, func(encrypter *chacha20crypto.Builder) {
//		encrypter.AdditionalData([]byte("user-42"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Stop(context.Background())
type Builder struct {
	// additionalData owns the current AAD snapshot; each setting replaces it.
	additionalData []byte
}

// BuilderFunc edits one fresh construction draft synchronously, once in order,
// before New validates the key. Nil entries panic; an expanded empty or nil
// slice omits edits. Owned authenticated data is detached before publication.
//
// Example:
//
//	configure := func(encrypter *chacha20crypto.Builder) {
//		encrypter.AdditionalData([]byte("application-record"))
//	}
//	policies := []chacha20crypto.BuilderFunc{configure}
//	driver, err := chacha20crypto.New(material, policies...)
//	if err != nil {
//		return err
//	}
//	return driver.Stop(ctx)
type BuilderFunc = func(*Builder)

// AdditionalData replaces the authenticated context and returns the same draft.
// It copies data immediately; nil or empty removes the context. Decryption must
// use the same context as encryption. New copies it again at publication.
//
// Example:
//
//	driver, err := chacha20crypto.New(material, func(encrypter *chacha20crypto.Builder) {
//		encrypter.AdditionalData([]byte("user-42"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Stop(context.Background())
func (encrypter *Builder) AdditionalData(data []byte) *Builder {
	encrypter.additionalData = slices.Clone(data)

	return encrypter
}
