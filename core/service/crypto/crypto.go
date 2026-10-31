package crypto

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// ErrEncrypterClosed identifies use of a stopped driver that owned key
// material. Match it with errors.Is and construct a new active driver before
// retrying; an Encrypter does not restart or take ownership of its drivers.
//
// Example:
//
//	_, err := encrypter.DecryptRaw(ciphertext)
//	if errors.Is(err, crypto.ErrEncrypterClosed) {
//		return fmt.Errorf("decrypt with an active driver: %w", err)
//	}
//	if err != nil {
//		return err
//	}
var ErrEncrypterClosed = errors.New("encrypter is closed")

// ErrInvalidDriver identifies a nil current or previous encryption driver.
// New rejects a nil current driver before construction callbacks and a nil
// previous driver after all callbacks; no driver operation is invoked.
//
// Example:
//
//	_, err := crypto.New(nil)
//	if errors.Is(err, crypto.ErrInvalidDriver) {
//		return fmt.Errorf("configure a current encryption driver: %w", err)
//	}
var ErrInvalidDriver = errors.New("encryption driver is invalid")

// ErrInvalidCiphertext is returned by Decrypt when the input is truncated,
// was altered, or was not produced by the driver's key and parameters.
// A facade tries its configured previous drivers before returning the primary
// failure; this sentinel alone does not distinguish tampering from a wrong key.
//
// Example:
//
//	_, err := encrypter.DecryptRaw(ciphertext)
//	if errors.Is(err, crypto.ErrInvalidCiphertext) {
//		return fmt.Errorf("reject the unauthenticated encrypted record: %w", err)
//	}
//	if err != nil {
//		return err
//	}
var ErrInvalidCiphertext = errors.New("invalid ciphertext")

// Driver encrypts and decrypts raw data.
//
// Encryption is authenticated and randomized: encrypting the same value twice
// yields different outputs that both decrypt to the value, and any
// modification of an output makes Decrypt fail. Encrypt accepts an empty
// value. Neither method modifies its argument, and the result never aliases
// it. A driver that holds key material and has been stopped returns
// [ErrEncrypterClosed] from both methods.
//
// Example:
//
//	var driver crypto.Driver = encrypter.Driver()
//	ciphertext, err := driver.Encrypt([]byte("fixture"))
//	if err != nil {
//		return err
//	}
//	_ = ciphertext // Raw driver calls bypass facade metrics and fallback policy.
type Driver interface {
	// Encrypt encrypts raw bytes using the driver's current key and parameters.
	// Empty input is valid; the input is not modified and the returned
	// ciphertext does not alias it. Driver failures are returned as errors.
	//
	// Example:
	//
	//	ciphertext, err := driver.Encrypt([]byte("fixture"))
	//	if err != nil {
	//		return err
	//	}
	//	_ = ciphertext
	Encrypt(value []byte) ([]byte, error)

	// Decrypt decrypts raw bytes. Input that fails authentication yields an
	// error wrapping [ErrInvalidCiphertext]. The input is not modified and the
	// returned plaintext does not alias it. This driver alone does not try the
	// Encrypter's previous keys; the facade owns fallback ordering.
	//
	// Example:
	//
	//	plaintext, err := driver.Decrypt(ciphertext)
	//	if err != nil {
	//		return err
	//	}
	//	useCredential(plaintext)
	Decrypt(value []byte) ([]byte, error)
}

// Encrypter provides typed encryption over a [Driver]. It is immutable after
// [New] and safe for concurrent use whenever its drivers are.
// Aggregate metrics describe complete calls, not individual fallback attempts.
// The zero Encrypter has no driver; construct it with New before use.
//
// Example:
//
//	encrypter, err := crypto.New(driver)
//	if err != nil {
//		return err
//	}
//	ciphertext, err := encrypter.Encrypt("stored token")
//	if err != nil {
//		return err
//	}
//	_ = ciphertext
type Encrypter struct {
	// driver encrypts all values and is tried first when decrypting.
	driver Driver

	// previous drivers are tried in order after the current driver fails.
	previous []Driver

	// telemetry selects aggregate metrics; nil follows the default per call.
	telemetry *telemetry.Telemetry

	// instruments caches metrics for the resolved service across concurrent calls.
	instruments *telemetry.Instruments[instruments]
}

// New creates an Encrypter that encrypts and decrypts with driver. To
// migrate stored values, pass older drivers with [Builder.Previous], then decrypt
// with DecryptWithRotation and, when it reports a previous key, encrypt with
// this Encrypter and persist the replacement in the caller's transaction.
//
// It returns [ErrInvalidDriver] when driver or any previous driver is nil.
// The primary driver is validated before callbacks; fallback validation follows
// them. Callbacks run directly once in order; supplied nil entries panic, while
// expanded nil or empty slices omit edits. Construction performs no I/O.
// Driver objects and any state they reference remain shared; construction
// callbacks are not retained. The caller owns each driver's lifecycle and must
// not stop it while this Encrypter can still use it.
//
// Example:
//
//	encrypter, err := crypto.New(current, func(encrypter *crypto.Builder) {
//		encrypter.Previous(previous)
//	})
//	if err != nil {
//		return err
//	}
//	_ = encrypter
func New(driver Driver, configure ...BuilderFunc) (*Encrypter, error) {
	if driver == nil {
		return nil, ErrInvalidDriver
	}

	encrypter := &Builder{}

	for _, configure := range configure {
		configure(encrypter)
	}

	if slices.Contains(encrypter.previous, nil) {
		return nil, ErrInvalidDriver
	}

	return &Encrypter{
		driver:      driver,
		previous:    slices.Clone(encrypter.previous),
		telemetry:   encrypter.telemetry,
		instruments: telemetry.NewInstruments(newInstruments),
	}, nil
}

// Driver returns the shared primary encryption driver, not previous drivers.
// Direct calls bypass this Encrypter's JSON encoding, fallback chain, and
// aggregate metrics. The driver retains its original lifecycle owner.
//
// Example:
//
//	ciphertext, err := encrypter.Driver().Encrypt([]byte("fixture"))
//	if err != nil {
//		return err
//	}
//	_ = ciphertext
func (encrypter *Encrypter) Driver() Driver {
	return encrypter.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() Driver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver. Only the primary chain is searched,
// not previous drivers. A failed match returns the zero T and false. Matching
// does not transfer lifecycle ownership or add facade metrics to native calls.
//
// With aescrypto imported from arkoren.dev/driver/aes/aescrypto:
//
//	driver, ok := encrypter.DriverAs[*aescrypto.Driver]()
//	if !ok {
//		return errors.New("primary driver is not AES")
//	}
//	ciphertext, err := driver.Encrypt([]byte("fixture"))
//	if err != nil {
//		return err
//	}
//	_ = ciphertext
func (encrypter *Encrypter) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](encrypter.Driver())
}

// Encrypt encrypts a JSON-encoded value.
// Metrics include encoding and driver failures, with one observation per normal
// return. Panics propagate without recording a completed operation.
//
// Example:
//
//	ciphertext, err := encrypter.Encrypt("stored token")
//	if err != nil {
//		return err
//	}
//	_ = ciphertext
func (encrypter *Encrypter) Encrypt[T any](value T) (result []byte, err error) {
	operation := encrypter.begin("encrypt")

	encoded, err := json.Marshal(value)

	if err != nil {
		operation.end("error", err)

		return nil, fmt.Errorf("encode encrypted value: %w", err)
	}

	result, err = encrypter.driver.Encrypt(encoded)
	operation.end("ok", err)

	return result, err
}

// Decrypt decrypts value and JSON-decodes it into T.
// Metrics cover all key attempts and decoding as one logical operation;
// a decoding error is a failed operation even after successful decryption.
// Panics propagate without recording a completed operation.
//
// Example:
//
//	token, err := encrypter.Decrypt[string](ciphertext)
//	if err != nil {
//		return err
//	}
//	_ = token
func (encrypter *Encrypter) Decrypt[T any](value []byte) (res T, err error) {
	operation := encrypter.begin("decrypt")

	plaintext, previous, err := encrypter.decrypt(value)

	if err != nil {
		operation.decrypt(previous, err)

		return res, err
	}

	if err := json.Unmarshal(plaintext, &res); err != nil {
		operation.decrypt(previous, err)

		return res, fmt.Errorf("decode decrypted value: %w", err)
	}

	operation.decrypt(previous, nil)

	return res, nil
}

// DecryptWithRotation decrypts and JSON-decodes value, reporting whether a
// previous driver authenticated the ciphertext. The boolean remains true if
// that driver's plaintext later fails JSON decoding; inspect err before using
// the result or replacing stored data. Only after a nil error, encrypt the
// decoded result with this Encrypter and persist the replacement using the
// caller's normal write path when the boolean is true.
// Metrics record the final returned result once, including JSON decoding errors.
// A decode error has an error outcome even when the boolean is true. A
// successful previous_key outcome does not mean rotation was persisted.
// Panics propagate without recording a completed operation.
// The example's persistReplacement function is application-owned persistence.
//
// Example:
//
//	token, previous, err := encrypter.DecryptWithRotation[string](ciphertext)
//	if err != nil {
//		return err
//	}
//	if previous {
//		replacement, err := encrypter.Encrypt(token)
//		if err != nil {
//			return err
//		}
//		if err := persistReplacement(replacement); err != nil {
//			return err
//		}
//	}
func (encrypter *Encrypter) DecryptWithRotation[T any](value []byte) (res T, usedPreviousKey bool, err error) {
	operation := encrypter.begin("decrypt")

	plaintext, usedPreviousKey, err := encrypter.decrypt(value)

	if err != nil {
		operation.decrypt(usedPreviousKey, err)

		return res, false, err
	}

	if err := json.Unmarshal(plaintext, &res); err != nil {
		operation.decrypt(usedPreviousKey, err)

		return res, usedPreviousKey, fmt.Errorf("decode encrypted value: %w", err)
	}

	operation.decrypt(usedPreviousKey, nil)

	return res, usedPreviousKey, nil
}

// EncryptRaw encrypts raw bytes.
// A normal return records one encryption operation without JSON encoding.
// Panics propagate without recording a completed operation.
//
// Example:
//
//	ciphertext, err := encrypter.EncryptRaw(plaintext)
//	if err != nil {
//		return err
//	}
//	_ = ciphertext
func (encrypter *Encrypter) EncryptRaw(value []byte) (result []byte, err error) {
	operation := encrypter.begin("encrypt")

	result, err = encrypter.driver.Encrypt(value)
	operation.end("ok", err)

	return result, err
}

// DecryptRaw decrypts raw bytes.
// A normal return records one logical decrypt across the entire key chain.
// Panics propagate without recording a completed operation.
//
// Example:
//
//	plaintext, err := encrypter.DecryptRaw(ciphertext)
//	if err != nil {
//		return err
//	}
//	_ = plaintext
func (encrypter *Encrypter) DecryptRaw(value []byte) ([]byte, error) {
	plaintext, _, err := encrypter.DecryptRawWithRotation(value)

	return plaintext, err
}

// DecryptRawWithRotation decrypts raw bytes and reports whether a previous
// driver succeeded. When it does, encrypt the plaintext with this Encrypter
// and persist the replacement using the caller's normal write path.
// Metrics combine all driver attempts into one final outcome, preserving the
// primary error if all fail. Successful fallback never implies persisted rotation.
// A true boolean accompanies successful raw decryption; any decryption failure
// returns false. No JSON decoding occurs. Panics propagate without recording a
// completed operation.
// The example's persistReplacement function is application-owned persistence.
//
// Example:
//
//	plaintext, previous, err := encrypter.DecryptRawWithRotation(ciphertext)
//	if err != nil {
//		return err
//	}
//	if previous {
//		replacement, err := encrypter.EncryptRaw(plaintext)
//		if err != nil {
//			return err
//		}
//		if err := persistReplacement(replacement); err != nil {
//			return err
//		}
//	}
func (encrypter *Encrypter) DecryptRawWithRotation(value []byte) (result []byte, previous bool, err error) {
	operation := encrypter.begin("decrypt")

	result, previous, err = encrypter.decrypt(value)
	operation.decrypt(previous, err)

	return result, previous, err
}

// decrypt tries the complete key chain without recording individual attempts.
// It preserves the primary error if every key fails, regardless of fallback errors.
func (encrypter *Encrypter) decrypt(value []byte) ([]byte, bool, error) {
	plaintext, err := encrypter.driver.Decrypt(value)

	if err == nil {
		return plaintext, false, nil
	}

	for _, driver := range encrypter.previous {
		plaintext, err := driver.Decrypt(value)

		if err == nil {
			return plaintext, true, nil
		}
	}

	return nil, false, err
}
