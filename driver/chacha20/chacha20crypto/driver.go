package chacha20crypto

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"

	"arkoren.dev/core/service/crypto"
	"arkoren.dev/foundation/key"
)

// Driver implements crypto.Driver using ChaCha20-Poly1305
// authenticated encryption. The AEAD cipher is created once at
// construction time and reused for every Encrypt/Decrypt call.
// This is safe because ChaCha20-Poly1305 AEAD instances are safe
// for concurrent use with different nonces. The nonce is generated
// randomly for each Encrypt call and prepended to the ciphertext.
// Keep the constructor's pointer: Driver contains synchronization state and must
// not be copied. Direct formatting of the pointer redacts owned credentials.
//
// Example:
//
//	driver, err := chacha20crypto.New(material)
//	if err != nil {
//		return err
//	}
//	ciphertext, err := driver.Encrypt([]byte("private value"))
//	if err != nil {
//		return err
//	}
//	_ = ciphertext
//	return driver.Stop(ctx)
type Driver struct {
	// mutex synchronizes encryption operations with [Driver.Stop].
	mutex sync.RWMutex

	// key is the raw key material, retained so that Stop
	// can zero it from memory.
	key []byte

	// aead is the underlying ChaCha20-Poly1305 AEAD cipher.
	aead cipher.AEAD

	// additionalData is the additional authenticated data (AAD) bound to every
	// ciphertext. It is a private copy made by [New] and never modified
	// afterwards, so it is read without holding mutex.
	//
	// AAD is authenticated but not encrypted, which allows binding the
	// ciphertext to a particular context (e.g. a user ID, resource path,
	// or version) so that ciphertext cannot be transplanted between
	// contexts. The same AAD must be configured for both encryption and
	// decryption or authentication will fail.
	additionalData []byte
}

var _ crypto.Driver = (*Driver)(nil)

// Format redacts the owned driver's keys, cipher state and authenticated data
// in ordinary and Go-syntax formatting. It reads no mutable state and takes no
// lock. Keep the constructor's pointer; Driver must not be copied.
//
// Example:
//
//	text := fmt.Sprintf("%#v", driver)
//	_ = text // [REDACTED ChaCha20 driver]
func (driver *Driver) Format(state fmt.State, _ rune) {
	// fmt.Formatter cannot return writer errors; formatting is best effort.
	_, _ = fmt.Fprint(state, "[REDACTED ChaCha20 driver]")
}

// ErrMismatchedChaCha20NonceSize is returned when the ciphertext
// provided to Decrypt is shorter than the expected nonce size,
// indicating truncated or corrupted data.
// Decrypt also wraps crypto.ErrInvalidCiphertext for portable classification.
//
// Example:
//
//	_, err := driver.Decrypt(nil)
//	if errors.Is(err, chacha20crypto.ErrMismatchedChaCha20NonceSize) {
//		// Reject the truncated stored value.
//	}
var ErrMismatchedChaCha20NonceSize = errors.New("mismatched nonce size")

// New creates a ChaCha20-Poly1305 driver from validated key.Shared material.
// The driver owns a detached raw copy;
// [Driver.Stop] zeroes only that copy, not the immutable key value.
//
// ChaCha20-Poly1305 uses random 96-bit nonces. Like AES-GCM, a single
// key should not be used for more than 2^32 (~4 billion) encryptions to
// keep nonce collision probability negligible. For higher-volume use
// cases, consider rotating keys or using XChaCha20-Poly1305 which has a
// 192-bit nonce and supports 2^64 encryptions per key.
//
// The key package requires exactly 32 bytes. A zero value returns key.ErrInvalid.
//
// Callbacks run directly once, synchronously in order on one fresh draft,
// before key validation. Nil entries panic; expanded nil/empty slices omit
// callbacks. Callback panics propagate. Settings are detached at publication.
//
// Example:
//
//	material, err := key.LoadShared("app-key.key")
//	if err != nil {
//		return err
//	}
//	driver, err := chacha20crypto.New(material, func(encrypter *chacha20crypto.Builder) {
//		encrypter.AdditionalData([]byte("user-42"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Stop(context.Background())
func New(material key.Shared, configure ...BuilderFunc) (*Driver, error) {
	encrypter := &Builder{}

	for _, configure := range configure {
		configure(encrypter)
	}

	if err := material.Err(); err != nil {
		return nil, err
	}

	raw := material.Bytes()
	aead, err := chacha20poly1305.New(raw)

	if err != nil {
		return nil, err
	}

	return &Driver{
		key:            raw,
		aead:           aead,
		additionalData: slices.Clone(encrypter.additionalData),
	}, nil
}

// Encrypt encrypts the plaintext using ChaCha20-Poly1305 with a
// random nonce. The returned slice contains the nonce followed by
// the ciphertext and authentication tag.
// Nil and empty plaintext are equivalent; input is never modified and output
// owns its bytes. After Stop it returns crypto.ErrEncrypterClosed.
//
// Example:
//
//	ciphertext, err := driver.Encrypt([]byte("private value"))
//	if err != nil {
//		return err
//	}
//	_ = ciphertext
func (driver *Driver) Encrypt(value []byte) ([]byte, error) {
	driver.mutex.RLock()
	defer driver.mutex.RUnlock()

	if driver.aead == nil {
		return nil, crypto.ErrEncrypterClosed
	}

	nonce := make([]byte, driver.aead.NonceSize())

	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return driver.aead.Seal(nonce, nonce, value, driver.additionalData), nil
}

// Decrypt decrypts ChaCha20-Poly1305 ciphertext that has the nonce
// prepended. Returns ErrMismatchedChaCha20NonceSize if the input is
// too short to contain a valid nonce.
// Invalid authentication wraps crypto.ErrInvalidCiphertext. Successful output
// does not alias input; input is never modified. After Stop the method returns
// crypto.ErrEncrypterClosed before validating ciphertext.
//
// Example:
//
//	plaintext, err := driver.Decrypt(ciphertext)
//	if err != nil {
//		return err
//	}
//	_ = plaintext
func (driver *Driver) Decrypt(value []byte) ([]byte, error) {
	driver.mutex.RLock()
	defer driver.mutex.RUnlock()

	if driver.aead == nil {
		return nil, crypto.ErrEncrypterClosed
	}

	nonceSize := driver.aead.NonceSize()

	if len(value) < nonceSize {
		return nil, fmt.Errorf("%w: %w", crypto.ErrInvalidCiphertext, ErrMismatchedChaCha20NonceSize)
	}

	nonce, ciphertext := value[:nonceSize], value[nonceSize:]
	plaintext, err := driver.aead.Open(nil, nonce, ciphertext, driver.additionalData)

	if err != nil {
		return nil, fmt.Errorf("%w: %w", crypto.ErrInvalidCiphertext, err)
	}

	return plaintext, nil
}

// Stop waits for in-flight operations under the exclusive lock, then zeroes the
// stored key and nils the AEAD cipher. Context cancellation does not interrupt
// the wait or prevent release, and repeated Stop calls are safe.
// It also lets composition register driver.Stop with Composer.Cleanup.
// Note: Go's crypto/cipher retains an internal copy of the key in the
// AEAD state that cannot be zeroed from user code. This is a limitation
// of the Go standard library. Nilling the AEAD allows the garbage
// collector to reclaim the cipher state. After Stop, any calls to
// [Driver.Encrypt] or [Driver.Decrypt] return [crypto.ErrEncrypterClosed].
//
// Example:
//
//	return driver.Stop(ctx)
func (driver *Driver) Stop(context.Context) error {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	clear(driver.key)
	driver.aead = nil
	runtime.KeepAlive(&driver.key)

	return nil
}
