package cryptotest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"arkoren.dev/core/service/crypto"
)

// Option changes one conformance suite setting.
type Option func(*suite)

// WithForeignDriver supplies a factory for a driver of the same algorithm
// with a different key. It enables the check that ciphertext produced under
// another key is rejected.
func WithForeignDriver(newForeign func(t *testing.T) crypto.Driver) Option {
	return func(s *suite) {
		s.newForeign = newForeign
	}
}

// suite holds the settings and driver factory of one [Run].
type suite struct {
	// newDriver creates the driver under test.
	newDriver func(t *testing.T) crypto.Driver

	// newForeign optionally creates a driver with a different key.
	newForeign func(t *testing.T) crypto.Driver
}

// Run checks that the drivers created by newDriver follow the [crypto.Driver]
// contract: values round-trip, encryption is randomized, tampered, truncated,
// or foreign ciphertext fails with [crypto.ErrInvalidCiphertext], inputs are
// left intact, and closed drivers return [crypto.ErrEncrypterClosed].
//
// newDriver is called once per subtest and must return drivers sharing the
// same key.
//
// # Example
//
//	func TestDriver(t *testing.T) {
//		cryptotest.Run(t, func(t *testing.T) crypto.Driver {
//			driver, err := aes.New(key)
//			if err != nil {
//				t.Fatal(err)
//			}
//			return driver
//		})
//	}
func Run(t *testing.T, newDriver func(t *testing.T) crypto.Driver, opts ...Option) {
	s := &suite{newDriver: newDriver}

	for _, opt := range opts {
		opt(s)
	}

	t.Run("RoundTrip", s.testRoundTrip)
	t.Run("EmptyPlaintext", s.testEmptyPlaintext)
	t.Run("Randomized", s.testRandomized)
	t.Run("SharedKey", s.testSharedKey)
	t.Run("TamperDetection", s.testTamperDetection)
	t.Run("Truncated", s.testTruncated)
	t.Run("ForeignKey", s.testForeignKey)
	t.Run("InputUntouched", s.testInputUntouched)
	t.Run("Closed", s.testClosed)
	t.Run("Concurrent", s.testConcurrent)
}

// mustEncrypt encrypts value with driver.
func mustEncrypt(t *testing.T, driver crypto.Driver, value []byte) []byte {
	t.Helper()

	encrypted, err := driver.Encrypt(value)

	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	return encrypted
}

// testRoundTrip verifies values of several sizes survive Encrypt and Decrypt.
func (suite *suite) testRoundTrip(t *testing.T) {
	driver := suite.newDriver(t)

	for _, size := range []int{1, 15, 16, 17, 1000, 1 << 16} {
		plaintext := bytes.Repeat([]byte{'x'}, size)

		decrypted, err := driver.Decrypt(mustEncrypt(t, driver, plaintext))

		if err != nil || !bytes.Equal(decrypted, plaintext) {
			t.Errorf("round trip of %d bytes = %d bytes, %v", size, len(decrypted), err)
		}
	}
}

// testEmptyPlaintext verifies empty values round-trip.
func (suite *suite) testEmptyPlaintext(t *testing.T) {
	driver := suite.newDriver(t)

	for _, plaintext := range [][]byte{nil, {}} {
		decrypted, err := driver.Decrypt(mustEncrypt(t, driver, plaintext))

		if err != nil || len(decrypted) != 0 {
			t.Errorf("round trip of empty value = %q, %v", decrypted, err)
		}
	}
}

// testRandomized verifies repeated encryption yields distinct outputs, which
// requires unique nonces.
func (suite *suite) testRandomized(t *testing.T) {
	driver := suite.newDriver(t)
	seen := map[string]bool{}

	for range 256 {
		encrypted := string(mustEncrypt(t, driver, []byte("same plaintext")))

		if seen[encrypted] {
			t.Fatal("two encryptions of the same plaintext are identical")
		}

		seen[encrypted] = true
	}
}

// testSharedKey verifies independently created drivers with the same key
// interoperate.
func (suite *suite) testSharedKey(t *testing.T) {
	encrypted := mustEncrypt(t, suite.newDriver(t), []byte("shared"))

	decrypted, err := suite.newDriver(t).Decrypt(encrypted)

	if err != nil || string(decrypted) != "shared" {
		t.Fatalf("Decrypt() = %q, %v", decrypted, err)
	}
}

// testTamperDetection verifies flipping any bit of the ciphertext, or
// appending to it, makes Decrypt fail.
func (suite *suite) testTamperDetection(t *testing.T) {
	driver := suite.newDriver(t)
	encrypted := mustEncrypt(t, driver, []byte("authentic message"))

	for index := range encrypted {
		tampered := bytes.Clone(encrypted)
		tampered[index] ^= 0x01

		assertInvalidCiphertext(t, driver, tampered, "flipped byte %d", index)
	}

	assertInvalidCiphertext(t, driver, append(bytes.Clone(encrypted), 0), "appended byte")
}

// testTruncated verifies every proper prefix of a ciphertext is rejected.
func (suite *suite) testTruncated(t *testing.T) {
	driver := suite.newDriver(t)
	encrypted := mustEncrypt(t, driver, []byte("authentic message"))

	for length := range len(encrypted) {
		assertInvalidCiphertext(t, driver, encrypted[:length], "prefix of %d bytes", length)
	}

	assertInvalidCiphertext(t, driver, nil, "nil input")
}

// testForeignKey verifies ciphertext from another key is rejected.
func (suite *suite) testForeignKey(t *testing.T) {
	if suite.newForeign == nil {
		t.Skip("no foreign driver supplied")
	}

	encrypted := mustEncrypt(t, suite.newForeign(t), []byte("secret"))

	assertInvalidCiphertext(t, suite.newDriver(t), encrypted, "foreign ciphertext")
}

// assertInvalidCiphertext fails t unless Decrypt rejects value with
// crypto.ErrInvalidCiphertext.
func assertInvalidCiphertext(t *testing.T, driver crypto.Driver, value []byte, format string, args ...any) {
	t.Helper()

	plaintext, err := driver.Decrypt(value)

	if !errors.Is(err, crypto.ErrInvalidCiphertext) || plaintext != nil {
		t.Errorf("Decrypt(%s) = %q, %v, want nil, ErrInvalidCiphertext", fmt.Sprintf(format, args...), plaintext, err)
	}
}

// testInputUntouched verifies Encrypt and Decrypt neither modify their input
// nor return slices aliasing it.
func (suite *suite) testInputUntouched(t *testing.T) {
	driver := suite.newDriver(t)
	plaintext := []byte("do not touch")
	original := bytes.Clone(plaintext)

	encrypted := mustEncrypt(t, driver, plaintext)

	if !bytes.Equal(plaintext, original) {
		t.Fatal("Encrypt modified its input")
	}

	clear(plaintext)

	decrypted, err := driver.Decrypt(mustEncrypt(t, driver, original))

	if err != nil || !bytes.Equal(decrypted, original) {
		t.Fatalf("Decrypt() = %q, %v", decrypted, err)
	}

	snapshot := bytes.Clone(encrypted)

	if _, err := driver.Decrypt(encrypted); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(encrypted, snapshot) {
		t.Error("Decrypt modified its input")
	}

	clear(decrypted)

	if again, err := driver.Decrypt(encrypted); err != nil || !bytes.Equal(again, original) {
		t.Errorf("Decrypt result aliases ciphertext or earlier plaintext: %q, %v", again, err)
	}
}

// testClosed verifies a driver that can be closed fails with
// crypto.ErrEncrypterClosed afterwards.
func (suite *suite) testClosed(t *testing.T) {
	driver := suite.newDriver(t)

	closer, ok := driver.(interface{ Stop(context.Context) error })

	if !ok {
		t.Skip("driver cannot be closed")
	}

	encrypted := mustEncrypt(t, driver, []byte("secret"))

	if err := closer.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if _, err := driver.Encrypt([]byte("secret")); !errors.Is(err, crypto.ErrEncrypterClosed) {
		t.Errorf("Encrypt() after Stop error = %v, want ErrEncrypterClosed", err)
	}

	if _, err := driver.Decrypt(encrypted); !errors.Is(err, crypto.ErrEncrypterClosed) {
		t.Errorf("Decrypt() after Stop error = %v, want ErrEncrypterClosed", err)
	}
}

// testConcurrent verifies one driver serves parallel callers.
func (suite *suite) testConcurrent(t *testing.T) {
	driver := suite.newDriver(t)

	var group sync.WaitGroup

	for range 8 {
		group.Go(func() {
			decrypted, err := driver.Decrypt(mustEncrypt(t, driver, []byte("secret")))

			if err != nil || string(decrypted) != "secret" {
				t.Errorf("round trip = %q, %v", decrypted, err)
			}
		})
	}

	group.Wait()
}
