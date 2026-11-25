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
//	driver, err := aescrypto.New(ring)
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

	// keys holds the raw 32-byte key material, current key first, then the
	// ring's previous keys. Stop zeroes every entry.
	keys [][]byte

	// gcms holds the AEAD cipher of each key, in the same order as keys,
	// created during construction so Encrypt and Decrypt avoid repeated cipher
	// setup. Index 0 encrypts; every index may decrypt. Stop nils it.
	gcms []cipher.AEAD

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
//	_, _, err := driver.Decrypt(nil)
//	if errors.Is(err, aescrypto.ErrMismatchedAESNonceSize) {
//		// Reject the truncated stored value.
//	}
var ErrMismatchedAESNonceSize = errors.New("mismatched nonce size")

// New creates a AES-GCM driver from a key ring of validated key.Shared
// material. [key.Ring.Current] encrypts every new value; Decrypt tries the
// current key first and then each [key.Ring.Previous] key, so values written
// before a rotation stay readable. Load or parse the ring through the key
// package. The driver owns detached raw copies; [Driver.Stop] zeroes only
// those copies, not the immutable ring.
//
// The ciphertext layout (nonce, ciphertext, tag) carries no key identifier, so
// Decrypt may try every key of the ring for a value written under an old one;
// keep rings short and drop previous keys once their data is re-encrypted.
//
// AES-GCM uses random 96-bit nonces. Per NIST recommendations, a single
// key should not be used for more than 2^32 (~4 billion) encryptions to
// keep nonce collision probability negligible. For higher-volume use
// cases, rotate keys or use XChaCha20-Poly1305.
//
// A zero or invalid ring returns the ring's error (wrapping key.ErrInvalid).
// Every cipher is constructed eagerly so that Encrypt and Decrypt avoid
// repeated setup overhead.
//
// Callbacks run directly once, synchronously in order on one fresh draft,
// before ring validation. Nil entries panic; expanded nil/empty slices omit
// callbacks. Callback panics propagate. Settings are detached at publication.
//
// Example:
//
//	ring, err := key.ParseRing[key.Shared](os.Getenv("APP_ENCRYPTION_KEYS"))
//	if err != nil {
//		return err
//	}
//	driver, err := aescrypto.New(ring, func(encrypter *aescrypto.Builder) {
//		encrypter.AdditionalData([]byte("user-42"))
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Stop(context.Background())
func New(ring key.Ring[key.Shared], configure ...BuilderFunc) (*Driver, error) {
	encrypter := &Builder{}

	for _, configure := range configure {
		configure(encrypter)
	}

	if err := ring.Err(); err != nil {
		return nil, err
	}

	material := append([]key.Shared{ring.Current()}, ring.Previous()...)
	driver := &Driver{additionalData: slices.Clone(encrypter.additionalData)}

	for _, shared := range material {
		raw := shared.Bytes()
		driver.keys = append(driver.keys, raw)

		block, err := aes.NewCipher(raw)

		if err != nil {
			return nil, err
		}

		gcm, err := cipher.NewGCM(block)

		if err != nil {
			return nil, err
		}

		driver.gcms = append(driver.gcms, gcm)
	}

	return driver, nil
}

// Encrypt encrypts the plaintext with the ring's current key using AES-GCM
// and a random nonce. The returned slice contains the nonce followed by the
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

	if driver.gcms == nil {
		return nil, crypto.ErrEncrypterClosed
	}

	current := driver.gcms[0]
	nonce := make([]byte, current.NonceSize())

	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return current.Seal(nonce, nonce, value, driver.additionalData), nil
}

// Decrypt decrypts AES-GCM ciphertext that has the nonce prepended, trying the
// ring's current key first and then each previous key in order. previous is
// true when a previous key authenticated the ciphertext, telling the caller to
// re-encrypt the plaintext with [Driver.Encrypt]; it is false on every error.
// Returns ErrMismatchedAESNonceSize if the input is too short to contain a
// valid nonce. Truncated, altered or unauthenticated input (under every key)
// wraps crypto.ErrInvalidCiphertext. Input is not changed and successful
// plaintext never aliases it. After Stop, crypto.ErrEncrypterClosed takes
// precedence over input validation.
//
// Example:
//
//	plaintext, previous, err := driver.Decrypt(ciphertext)
//	if err != nil {
//		return err
//	}
//	if previous {
//		// Re-encrypt plaintext so the old key can be retired.
//	}
//	_ = plaintext
func (driver *Driver) Decrypt(value []byte) ([]byte, bool, error) {
	driver.mutex.RLock()
	defer driver.mutex.RUnlock()

	if driver.gcms == nil {
		return nil, false, crypto.ErrEncrypterClosed
	}

	nonceSize := driver.gcms[0].NonceSize()

	if len(value) < nonceSize {
		return nil, false, fmt.Errorf("%w: %w", crypto.ErrInvalidCiphertext, ErrMismatchedAESNonceSize)
	}

	nonce, ciphertext := value[:nonceSize], value[nonceSize:]
	var failure error

	for index, candidate := range driver.gcms {
		plaintext, err := candidate.Open(nil, nonce, ciphertext, driver.additionalData)

		if err == nil {
			return plaintext, index > 0, nil
		}

		failure = err
	}

	return nil, false, fmt.Errorf("%w: %w", crypto.ErrInvalidCiphertext, failure)
}

// Stop waits for in-flight encryption/decryption operations under the exclusive
// lock, then zeroes every stored key and nils the AEAD ciphers. Context cancellation
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

	for _, raw := range driver.keys {
		clear(raw)
	}

	driver.gcms = nil
	runtime.KeepAlive(&driver.keys)

	return nil
}
