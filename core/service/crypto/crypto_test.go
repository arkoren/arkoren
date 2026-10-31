package crypto_test

import (
	"errors"
	"sync"
	"testing"

	"arkoren.dev/core/service/crypto"
)

// TestBuilderPreviousDetachesInputAndPublication verifies append ordering,
// input capture, and retained-root isolation without cloning live drivers.
func TestBuilderPreviousDetachesInputAndPublication(t *testing.T) {
	t.Parallel()

	first := &successfulDriver{plaintext: []byte("first")}
	input := []crypto.Driver{first}
	var retained *crypto.Builder
	calls := 0

	encrypter, err := crypto.New(keyedDriver{key: "current"}, func(encrypter *crypto.Builder) {
		retained = encrypter
		calls++

		if encrypter.Previous(input...) != encrypter {
			t.Fatal("Previous returned a different draft")
		}

		input[0] = nil
	}, func(encrypter *crypto.Builder) {
		if retained != encrypter || calls != 1 {
			t.Fatal("policies did not share one ordered draft")
		}

		calls++
		encrypter.Previous(successfulDriver{plaintext: []byte("second")})
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Previous(nil)

	first.plaintext = []byte("shared-driver")

	value, previous, err := encrypter.DecryptRawWithRotation([]byte("legacy"))

	if err != nil || !previous {
		t.Fatalf("rotation = %q, %v, %v", value, previous, err)
	}

	if calls != 2 || string(value) != "shared-driver" {
		t.Fatalf("callbacks = %d, value = %q", calls, value)
	}
}

// TestBuilderRejectsPrimaryBeforePolicies preserves the required-driver check
// before policy invocation, including when a supplied callback is nil.
func TestBuilderRejectsPrimaryBeforePolicies(t *testing.T) {
	t.Parallel()

	called := false

	_, err := crypto.New(nil, func(encrypter *crypto.Builder) {
		called = true
	}, nil)

	if !errors.Is(err, crypto.ErrInvalidDriver) || called {
		t.Fatalf("error = %v, callback ran = %v", err, called)
	}
}

// TestBuilderValidatesFallbacksAfterPolicies preserves completed-list validation
// rather than rejecting an incomplete draft or skipping later policies.
func TestBuilderValidatesFallbacksAfterPolicies(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := crypto.New(driver{}, func(encrypter *crypto.Builder) {
		encrypter.Previous(nil)
		calls++
	}, func(encrypter *crypto.Builder) {
		encrypter.Previous(driver{})
		calls++
	})

	if !errors.Is(err, crypto.ErrInvalidDriver) || calls != 2 {
		t.Fatalf("error = %v, callbacks = %d", err, calls)
	}
}

// TestBuilderNilExpansionOmitsPolicies verifies empty policy lists still create
// a valid primary-only encrypter without invoking driver operations.
func TestBuilderNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var callbacks []func(*crypto.Builder)

	_, err := crypto.New(driver{}, callbacks...)

	if err != nil {
		t.Fatal(err)
	}

	_, err = crypto.New(driver{}, []func(*crypto.Builder){}...)

	if err != nil {
		t.Fatal(err)
	}
}

// TestBuilderNilPolicyPanics distinguishes programmer misuse from a nil
// fallback driver, which remains an ordinary ErrInvalidDriver domain error.
func TestBuilderNilPolicyPanics(t *testing.T) {
	t.Parallel()

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_, _ = crypto.New(driver{}, nil)
	}()

	if recovered == nil {
		t.Fatal("nil construction callback did not panic")
	}
}

// TestBuilderPolicyPanicPropagates preserves application panic identity rather
// than mapping it to an encryption error or recovering it during construction.
func TestBuilderPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_, _ = crypto.New(driver{}, func(encrypter *crypto.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// driver implements an identity encryption driver for tests.
type driver struct{}

var _ crypto.Driver = driver{}

// Encrypt returns value unchanged.
func (driver driver) Encrypt(value []byte) ([]byte, error) {
	return value, nil
}

// Decrypt returns value unchanged.
func (driver driver) Decrypt(value []byte) ([]byte, error) {
	return value, nil
}

// errMismatchedKey is returned when keyedDriver cannot decrypt a ciphertext.
var errMismatchedKey = errors.New("mismatched key")

// keyedDriver prefixes ciphertext with a deterministic key identifier.
type keyedDriver struct {
	// key identifies the deterministic encryption key used by this fixture.
	key string
}

// Encrypt prefixes value with the driver key.
func (keyedDriver keyedDriver) Encrypt(value []byte) ([]byte, error) {
	return append([]byte(keyedDriver.key+":"), value...), nil
}

// Decrypt returns value only when it has the driver key prefix.
func (keyedDriver keyedDriver) Decrypt(value []byte) ([]byte, error) {
	prefix := []byte(keyedDriver.key + ":")

	if len(value) < len(prefix) || string(value[:len(prefix)]) != string(prefix) {
		return nil, errMismatchedKey
	}

	return value[len(prefix):], nil
}

// successfulDriver always returns its configured plaintext when decrypting.
type successfulDriver struct {
	// plaintext is returned by successful decryption regardless of ciphertext.
	plaintext []byte
}

// Encrypt returns value unchanged.
func (successfulDriver successfulDriver) Encrypt(value []byte) ([]byte, error) {
	return value, nil
}

// Decrypt returns the driver's configured plaintext.
func (successfulDriver successfulDriver) Decrypt([]byte) ([]byte, error) {
	return successfulDriver.plaintext, nil
}

// newTestEncrypter creates an encrypter or fails the test.
func newTestEncrypter(t *testing.T, driver crypto.Driver) *crypto.Encrypter {
	t.Helper()

	encrypter, err := crypto.New(driver)

	if err != nil {
		t.Fatal(err)
	}

	return encrypter
}

// encryptTestString encrypts value or fails the test.
func encryptTestString(t *testing.T, encrypter *crypto.Encrypter, value string) []byte {
	t.Helper()

	ciphertext, err := encrypter.Encrypt(value)

	if err != nil {
		t.Fatal(err)
	}

	return ciphertext
}

// decryptTestStringWithRotation decrypts value or fails the test.
func decryptTestStringWithRotation(t *testing.T, encrypter *crypto.Encrypter, value []byte) (string, bool) {
	t.Helper()

	plaintext, usedPreviousKey, err := encrypter.DecryptWithRotation[string](value)

	if err != nil {
		t.Fatal(err)
	}

	return plaintext, usedPreviousKey
}

// legacyEncryptedString creates a string encrypted with a previous key.
func legacyEncryptedString(t *testing.T) (keyedDriver, []byte) {
	t.Helper()

	previousDriver := keyedDriver{key: "previous"}
	previous := newTestEncrypter(t, previousDriver)

	return previousDriver, encryptTestString(t, previous, "arkoren")
}

// legacyEncryptedBytes creates raw bytes encrypted with a previous key.
func legacyEncryptedBytes(t *testing.T) (keyedDriver, []byte) {
	t.Helper()

	previousDriver := keyedDriver{key: "previous"}

	legacyCiphertext, err := previousDriver.Encrypt([]byte("arkoren"))

	if err != nil {
		t.Fatal(err)
	}

	return previousDriver, legacyCiphertext
}

// decryptPreviousRepeatedly validates repeated raw decryption during rotation.
func decryptPreviousRepeatedly(encrypter *crypto.Encrypter, value []byte, errs chan<- error) {
	for range 1000 {
		if err := previousDecryptionError(encrypter, value); err != nil {
			errs <- err

			return
		}
	}
}

// previousDecryptionError reports an unexpected raw decryption result.
func previousDecryptionError(encrypter *crypto.Encrypter, value []byte) error {
	plaintext, usedPreviousKey, err := encrypter.DecryptRawWithRotation(value)

	if err == nil {
		if string(plaintext) != "arkoren" || !usedPreviousKey {
			return errors.New("DecryptRawWithRotation() returned an unexpected success")
		}

		return nil
	}

	if errors.Is(err, errMismatchedKey) && !usedPreviousKey {
		return nil
	}

	return err
}

// TestEncrypterRoundTrip verifies typed encryption round-tripping.
func TestEncrypterRoundTrip(t *testing.T) {
	t.Parallel()

	encrypter, err := crypto.New(driver{})

	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := encrypter.Encrypt(struct {
		// Name is the JSON field used to verify typed value serialization.
		Name string `json:"name"`
	}{
		Name: "arkoren",
	})

	if err != nil {
		t.Fatal(err)
	}

	value, err := encrypter.Decrypt[struct {
		// Name is the JSON field used to verify typed value serialization.
		Name string `json:"name"`
	}](ciphertext)

	if err != nil {
		t.Fatal(err)
	}

	if value.Name != "arkoren" {
		t.Fatalf("Name = %q, want arkoren", value.Name)
	}
}

// TestEncrypterDriver verifies driver retrieval.
func TestEncrypterDriver(t *testing.T) {
	t.Parallel()

	want := driver{}

	encrypter, err := crypto.New(want)

	if err != nil {
		t.Fatal(err)
	}

	if encrypter.Driver() != want {
		t.Fatal("Driver() did not return the configured driver")
	}
}

// TestEncrypterCurrentKeyEncryptionWithRotation verifies current-key reporting.
func TestEncrypterCurrentKeyEncryptionWithRotation(t *testing.T) {
	t.Parallel()

	current := keyedDriver{key: "current"}
	previous := keyedDriver{key: "previous"}

	encrypter, err := crypto.New(current, func(encrypter *crypto.Builder) {
		encrypter.Previous(previous)
	})

	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := encrypter.Encrypt("arkoren")

	if err != nil {
		t.Fatal(err)
	}

	value, usedPreviousKey, err := encrypter.DecryptWithRotation[string](ciphertext)

	if err != nil {
		t.Fatal(err)
	}

	if value != "arkoren" {
		t.Fatalf("value = %q, want arkoren", value)
	}

	if usedPreviousKey {
		t.Fatal("DecryptWithRotation() reported a previous key")
	}
}

// TestEncrypterDecryptsAndMigratesPreviousKey verifies previous-key decryption.
func TestEncrypterDecryptsAndMigratesPreviousKey(t *testing.T) {
	t.Parallel()

	previousDriver, legacyCiphertext := legacyEncryptedString(t)

	encrypter, err := crypto.New(keyedDriver{key: "current"}, func(encrypter *crypto.Builder) {
		encrypter.Previous(previousDriver)
	})

	if err != nil {
		t.Fatal(err)
	}

	value, usedPreviousKey := decryptTestStringWithRotation(t, encrypter, legacyCiphertext)

	if value != "arkoren" {
		t.Fatalf("value = %q, want arkoren", value)
	}

	if !usedPreviousKey {
		t.Fatal("DecryptWithRotation() did not report a previous key")
	}

	replacement := encryptTestString(t, encrypter, value)
	value, usedPreviousKey = decryptTestStringWithRotation(t, encrypter, replacement)

	if value != "arkoren" {
		t.Fatalf("replacement value = %q, want arkoren", value)
	}

	if usedPreviousKey {
		t.Fatal("replacement was decrypted with a previous key")
	}
}

// TestEncrypterWithoutRotationReportsCurrentKey verifies default-key reporting.
func TestEncrypterWithoutRotationReportsCurrentKey(t *testing.T) {
	t.Parallel()

	encrypter, err := crypto.New(driver{})

	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := encrypter.Encrypt("arkoren")

	if err != nil {
		t.Fatal(err)
	}

	value, usedPreviousKey, err := encrypter.DecryptWithRotation[string](ciphertext)

	if err != nil {
		t.Fatal(err)
	}

	if value != "arkoren" {
		t.Fatalf("value = %q, want arkoren", value)
	}

	if usedPreviousKey {
		t.Fatal("DecryptWithRotation() reported a previous key")
	}
}

// TestEncrypterRotationReturnsCurrentErrorForMalformedCiphertext verifies error precedence.
func TestEncrypterRotationReturnsCurrentErrorForMalformedCiphertext(t *testing.T) {
	t.Parallel()

	encrypter, err := crypto.New(keyedDriver{key: "current"}, func(encrypter *crypto.Builder) {
		encrypter.Previous(keyedDriver{key: "previous"})
	})

	if err != nil {
		t.Fatal(err)
	}

	_, usedPreviousKey, err := encrypter.DecryptRawWithRotation([]byte("malformed"))

	if !errors.Is(err, errMismatchedKey) {
		t.Fatalf("DecryptRawWithRotation() error = %v, want %v", err, errMismatchedKey)
	}

	if usedPreviousKey {
		t.Fatal("DecryptRawWithRotation() reported a previous key")
	}
}

// TestNewRejectsNilCurrentDriver verifies nil current driver rejection.
func TestNewRejectsNilCurrentDriver(t *testing.T) {
	t.Parallel()

	_, err := crypto.New(nil)

	if !errors.Is(err, crypto.ErrInvalidDriver) {
		t.Fatalf("New() error = %v, want %v", err, crypto.ErrInvalidDriver)
	}
}

// TestNewRejectsNilPreviousDriver verifies nil previous driver rejection.
func TestNewRejectsNilPreviousDriver(t *testing.T) {
	t.Parallel()

	_, err := crypto.New(keyedDriver{key: "current"}, func(encrypter *crypto.Builder) {
		encrypter.Previous(nil)
	})

	if !errors.Is(err, crypto.ErrInvalidDriver) {
		t.Fatalf("New() error = %v, want %v", err, crypto.ErrInvalidDriver)
	}
}

// TestEncrypterDecryptsPreviousKey verifies previous-key fallback.
func TestEncrypterDecryptsPreviousKey(t *testing.T) {
	t.Parallel()

	previousDriver, legacyCiphertext := legacyEncryptedString(t)
	withoutPrevious := newTestEncrypter(t, keyedDriver{key: "current"})

	_, usedPreviousKey, err := withoutPrevious.DecryptWithRotation[string](legacyCiphertext)

	if !errors.Is(err, errMismatchedKey) {
		t.Fatalf("DecryptWithRotation() error = %v, want %v", err, errMismatchedKey)
	}

	if usedPreviousKey {
		t.Fatal("DecryptWithRotation() reported a previous key")
	}

	encrypter, err := crypto.New(keyedDriver{key: "current"}, func(encrypter *crypto.Builder) {
		encrypter.Previous(previousDriver)
	})

	if err != nil {
		t.Fatal(err)
	}

	value, usedPreviousKey := decryptTestStringWithRotation(t, encrypter, legacyCiphertext)

	if value != "arkoren" {
		t.Fatalf("value = %q, want arkoren", value)
	}

	if !usedPreviousKey {
		t.Fatal("DecryptWithRotation() did not report a previous key")
	}
}

// TestEncrypterConcurrentRotationDecryption verifies concurrent decryption.
func TestEncrypterConcurrentRotationDecryption(t *testing.T) {
	t.Parallel()

	previousDriver, legacyCiphertext := legacyEncryptedBytes(t)

	encrypter, err := crypto.New(keyedDriver{key: "current"}, func(encrypter *crypto.Builder) {
		encrypter.Previous(previousDriver)
	})

	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)

	for range 2 {
		go func() {
			defer waitGroup.Done()
			decryptPreviousRepeatedly(encrypter, legacyCiphertext, errs)
		}()
	}

	waitGroup.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
}

// TestEncrypterWithPreviousPreservesOrderAcrossOptions verifies fallback order.
func TestEncrypterWithPreviousPreservesOrderAcrossOptions(t *testing.T) {
	t.Parallel()

	encrypter, err := crypto.New(keyedDriver{key: "current"},
		func(encrypter *crypto.Builder) {
			encrypter.Previous(successfulDriver{plaintext: []byte("first")})
		},
		func(encrypter *crypto.Builder) {
			encrypter.Previous()
		},
		func(encrypter *crypto.Builder) {
			encrypter.Previous(successfulDriver{plaintext: []byte("second")})
		},
	)

	if err != nil {
		t.Fatal(err)
	}

	plaintext, usedPreviousKey, err := encrypter.DecryptRawWithRotation([]byte("ciphertext"))

	if err != nil {
		t.Fatal(err)
	}

	if string(plaintext) != "first" {
		t.Fatalf("DecryptRawWithRotation() plaintext = %q, want first", plaintext)
	}

	if !usedPreviousKey {
		t.Fatal("DecryptRawWithRotation() did not report a previous key")
	}
}
