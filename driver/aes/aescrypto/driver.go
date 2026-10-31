package aescrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"sync"

	"arkoren.dev/core/service/crypto"
	"arkoren.dev/foundation/key"
)

// Driver implements crypto.Driver using AES-GCM (Galois/Counter
// Mode) authenticated encryption. The cipher.AEAD is created once
// during construction and reused for every operation. This is safe
// because GCM instances are safe for concurrent use with different
// nonces. The nonce is generated randomly for each Encrypt call
// and prepended to the ciphertext.
// Keep the constructor's pointer: Driver contains synchronization state and must
// not be copied. Direct formatting of that pointer redacts owned credentials.
//
// Example:
//
//	driver, err := aescrypto.New(material)
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

	// key is the raw 32-byte AES-256 key material.
	key []byte

	// gcm holds the pre-computed AEAD cipher created from the
	// key during construction, avoiding repeated cipher setup
	// on every Encrypt and Decrypt call.
	gcm cipher.AEAD

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
// for ordinary and Go-syntax formatting. It does not read mutable cipher state
// or take its lock. Keep the constructor's pointer; Driver must not be copied.
//
// Example:
//
//	text := fmt.Sprintf("%#v", driver)
//	_ = text // [REDACTED AES driver]
func (driver *Driver) Format(state fmt.State, _ rune) {
	// fmt.Formatter cannot return a writer error; formatting is best effort.
	_, _ = fmt.Fprint(state, "[REDACTED AES driver]")
}

// ErrMismatchedAESNonceSize is returned when the ciphertext provided
// to Decrypt is shorter than the expected GCM nonce size, indicating
// truncated or corrupted data.
// Decrypt also wraps crypto.ErrInvalidCiphertext, so callers can use the
// portable classification without depending on the nonce layout.
//
// Example:
//
//	_, err := driver.Decrypt(nil)
//	if errors.Is(err, aescrypto.ErrMismatchedAESNonceSize) {
//		// Reject the truncated stored value.
//	}
var ErrMismatchedAESNonceSize = errors.New("mismatched nonce size")

// New creates an AES-256-GCM driver from validated key.Shared material.
// Load or parse it through the key package. The driver owns a detached raw copy;
// [Driver.Stop] zeroes only that copy, not the immutable key value.
//
// AES-GCM uses random 96-bit nonces. Per NIST recommendations, a single
// key should not be used for more than 2^32 (~4 billion) encryptions to
// keep nonce collision probability negligible. For higher-volume use
// cases, consider rotating keys or using XChaCha20-Poly1305.
//
// The key package requires exactly 32 bytes for AES-256. A zero value
// returns key.ErrInvalid. The GCM
// cipher is constructed eagerly so that Encrypt and Decrypt avoid repeated
// setup overhead.
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
//	driver, err := aescrypto.New(material, func(encrypter *aescrypto.Builder) {
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

	block, err := aes.NewCipher(raw)

	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)

	if err != nil {
		return nil, err
	}

	return &Driver{
		key:            raw,
		gcm:            gcm,
		additionalData: slices.Clone(encrypter.additionalData),
	}, nil
}

// Encrypt encrypts the plaintext using AES-GCM with a random
// nonce. The returned slice contains the nonce followed by the
// ciphertext and authentication tag.
// Nil and empty plaintext are equivalent. Input is not modified, and output
// owns its bytes. After Stop the method returns crypto.ErrEncrypterClosed.
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

	if driver.gcm == nil {
		return nil, crypto.ErrEncrypterClosed
	}

	nonce := make([]byte, driver.gcm.NonceSize())

	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return driver.gcm.Seal(
		nonce, nonce, value, driver.additionalData,
	), nil
}

// Decrypt decrypts AES-GCM ciphertext that has the nonce
// prepended. Returns ErrMismatchedAESNonceSize if the input is
// too short to contain a valid nonce.
// Truncated, altered or unauthenticated input wraps crypto.ErrInvalidCiphertext.
// Input is not changed and successful plaintext never aliases it. After Stop,
// crypto.ErrEncrypterClosed takes precedence over input validation.
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

	if driver.gcm == nil {
		return nil, crypto.ErrEncrypterClosed
	}

	nonceSize := driver.gcm.NonceSize()

	if len(value) < nonceSize {
		return nil, fmt.Errorf("%w: %w", crypto.ErrInvalidCiphertext, ErrMismatchedAESNonceSize)
	}

	nonce, ciphertext := value[:nonceSize], value[nonceSize:]
	plaintext, err := driver.gcm.Open(
		nil, nonce, ciphertext, driver.additionalData,
	)

	if err != nil {
		return nil, fmt.Errorf("%w: %w", crypto.ErrInvalidCiphertext, err)
	}

	return plaintext, nil
}

// Stop waits for in-flight encryption/decryption operations under the exclusive
// lock, then zeroes the stored key and nils the AEAD cipher. Context cancellation
// does not interrupt that wait or prevent release; repeated Stop calls are safe.
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
	driver.gcm = nil
	runtime.KeepAlive(&driver.key)

	return nil
}
