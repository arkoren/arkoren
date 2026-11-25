package aescrypto_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/crypto/cryptotest"
	"arkoren.dev/driver/aes/aescrypto"
	"arkoren.dev/foundation/key"
)

// newEncryptionDriver provisions typed material for the existing raw fixtures.
func newEncryptionDriver(raw []byte, configure ...aescrypto.BuilderFunc) (*aescrypto.Driver, error) {
	material, err := key.NewShared(raw)

	if err != nil {
		return nil, err
	}

	ring, err := key.NewRing(material)

	if err != nil {
		return nil, err
	}

	return aescrypto.New(ring, configure...)
}

// TestNewRejectsZeroKey verifies the typed boundary remains fail-closed.
func TestNewRejectsZeroKey(t *testing.T) {
	t.Parallel()

	if _, err := aescrypto.New(key.Ring[key.Shared]{}); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("New(zero) = %v", err)
	}
}

// newDriver creates a driver using a key made of fill bytes.
func newDriver(t *testing.T, fill byte) crypto.Driver {
	t.Helper()

	key := make([]byte, 32)

	for index := range key {
		key[index] = fill
	}

	created, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	return created
}

// newRingDriver builds a driver from the ring the suite generates.
func newRingDriver(t *testing.T, ring key.Ring[key.Shared]) crypto.Driver {
	t.Helper()

	created, err := aescrypto.New(ring)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = created.Stop(t.Context()) })

	return created
}

// TestDriverConformance verifies the driver passes the crypto conformance
// suite, including key ring rotation.
func TestDriverConformance(t *testing.T) {
	cryptotest.Run(t, func(t *testing.T) crypto.Driver {
		return newDriver(t, 1)
	}, cryptotest.WithForeignDriver(func(t *testing.T) crypto.Driver {
		return newDriver(t, 2)
	}), cryptotest.WithRing(newRingDriver))
}

// TestDecryptReportsPreviousKey verifies a value written under an old key
// decrypts through a rotated ring with previous=true, and a value written under
// the new key reports previous=false.
func TestDecryptReportsPreviousKey(t *testing.T) {
	t.Parallel()

	oldKey, err := key.NewShared(bytes.Repeat([]byte{1}, 32))

	if err != nil {
		t.Fatal(err)
	}

	newKey, err := key.NewShared(bytes.Repeat([]byte{2}, 32))

	if err != nil {
		t.Fatal(err)
	}

	oldRing, err := key.NewRing(oldKey)

	if err != nil {
		t.Fatal(err)
	}

	rotated, err := key.NewRing(newKey, oldKey)

	if err != nil {
		t.Fatal(err)
	}

	before := newRingDriver(t, oldRing)
	after := newRingDriver(t, rotated)
	legacy, err := before.Encrypt([]byte("legacy"))

	if err != nil {
		t.Fatal(err)
	}

	plaintext, previous, err := after.Decrypt(legacy)

	if err != nil || !previous || string(plaintext) != "legacy" {
		t.Fatalf("Decrypt(legacy) = %q, %v, %v", plaintext, previous, err)
	}

	fresh, err := after.Encrypt([]byte("fresh"))

	if err != nil {
		t.Fatal(err)
	}

	plaintext, previous, err = after.Decrypt(fresh)

	if err != nil || previous || string(plaintext) != "fresh" {
		t.Fatalf("Decrypt(fresh) = %q, %v, %v", plaintext, previous, err)
	}

	if _, _, err := before.Decrypt(fresh); !errors.Is(err, crypto.ErrInvalidCiphertext) {
		t.Fatalf("old ring decrypted new key data: %v", err)
	}
}

// TestOwnedDriverFormattingRedactsKey checks the public constructor's pointer
// in ordinary and Go-syntax formatting without inspecting its private fields.
func TestOwnedDriverFormattingRedactsKey(t *testing.T) {
	t.Parallel()

	material, err := key.NewShared(bytes.Repeat([]byte{'s'}, 32))

	if err != nil {
		t.Fatal(err)
	}

	ring, err := key.NewRing(material)

	if err != nil {
		t.Fatal(err)
	}

	driver, err := aescrypto.New(ring)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = driver.Stop(t.Context()) })

	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, driver); got != "[REDACTED AES driver]" {
			t.Fatalf("owned driver was not redacted for %s", format)
		}
	}
}

// TestAESNewCreatesEncrypter verifies New accepts a valid AES key.
func TestAESNewCreatesEncrypter(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")

	_, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}
}

// TestAESCloseDoesNotClearCopiedCallerKey verifies New copies caller key material.
func TestAESCloseDoesNotClearCopiedCallerKey(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	if err := encrypter.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual([]byte("12345678901234567890123456789012"), key) {
		t.Fatalf("want %v, got %v", []byte("12345678901234567890123456789012"), key)
	}
}

// TestAESEncryptSucceeds verifies encryption returns ciphertext.
func TestAESEncryptSucceeds(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	plain := []byte("Hello, World!")
	_, err = encrypter.Encrypt(plain)

	if err != nil {
		t.Fatal(err)
	}
}

// TestAESEncryptDecryptRoundTrip verifies encrypted data can be decrypted.
func TestAESEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	plain := []byte("Hello, World!")
	ciphertext, err := encrypter.Encrypt(plain)

	if err != nil {
		t.Fatal(err)
	}

	res, _, err := encrypter.Decrypt(ciphertext)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(plain, res) {
		t.Fatalf("want %v, got %v", plain, res)
	}
}

// TestAESNewWithInvalidKeySize verifies invalid AES keys are rejected.
func TestAESNewWithInvalidKeySize(t *testing.T) {
	t.Parallel()

	_, err := newEncryptionDriver([]byte("short"))

	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestAESNewWith16ByteKey verifies AES-128 material is rejected.
func TestAESNewWith16ByteKey(t *testing.T) {
	t.Parallel()

	key := []byte("1234567890123456")

	if _, err := newEncryptionDriver(key); err == nil {
		t.Fatal("accepted 16-byte material")
	}
}

// TestAESNewWith24ByteKey verifies AES-192 material is rejected.
func TestAESNewWith24ByteKey(t *testing.T) {
	t.Parallel()

	key := []byte("123456789012345678901234")

	if _, err := newEncryptionDriver(key); err == nil {
		t.Fatal("accepted 24-byte material")
	}
}

// TestAESDecryptWithShortCiphertext verifies truncated input is rejected.
func TestAESDecryptWithShortCiphertext(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	_, _, err = encrypter.Decrypt([]byte("short"))

	if !errors.Is(err, aescrypto.ErrMismatchedAESNonceSize) {
		t.Fatalf("expected error %v, got %v", aescrypto.ErrMismatchedAESNonceSize, err)
	}
}

// TestAESDecryptWithCorruptedCiphertext verifies corrupted data is rejected.
func TestAESDecryptWithCorruptedCiphertext(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	plain := []byte("Hello, World!")
	ciphertext, err := encrypter.Encrypt(plain)

	if err != nil {
		t.Fatal(err)
	}

	ciphertext[len(ciphertext)-1] ^= 0xFF

	_, _, err = encrypter.Decrypt(ciphertext)

	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestAESCloseDoesNotClearCallerKeyMaterial verifies Close preserves caller memory.
func TestAESCloseDoesNotClearCallerKeyMaterial(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	copy(key, "12345678901234567890123456789012")

	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	encrypter.Stop(context.Background())

	if !reflect.DeepEqual([]byte("12345678901234567890123456789012"), key) {
		t.Fatalf("want %v, got %v", []byte("12345678901234567890123456789012"), key)
	}
}

// TestAESAdditionalDataMustMatchForDecrypt verifies AAD authentication.
func TestAESAdditionalDataMustMatchForDecrypt(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key, func(encrypter *aescrypto.Builder) {
		encrypter.AdditionalData([]byte("context-v1"))
	})

	if err != nil {
		t.Fatal(err)
	}

	plain := []byte("Hello, World!")
	ciphertext, err := encrypter.Encrypt(plain)

	if err != nil {
		t.Fatal(err)
	}

	decrypter, err := newEncryptionDriver(key, func(encrypter *aescrypto.Builder) {
		encrypter.AdditionalData([]byte("context-v2"))
	})

	if err != nil {
		t.Fatal(err)
	}

	_, _, err = decrypter.Decrypt(ciphertext)

	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestAESAdditionalDataRoundTrip verifies matching AAD decrypts correctly.
func TestAESAdditionalDataRoundTrip(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key, func(encrypter *aescrypto.Builder) {
		encrypter.AdditionalData([]byte("user-42"))
	})

	if err != nil {
		t.Fatal(err)
	}

	plain := []byte("Hello, World!")
	ciphertext, err := encrypter.Encrypt(plain)

	if err != nil {
		t.Fatal(err)
	}

	res, _, err := encrypter.Decrypt(ciphertext)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(plain, res) {
		t.Fatalf("want %v, got %v", plain, res)
	}
}

// TestAESEncryptProducesDifferentCiphertexts verifies nonces vary per encryption.
func TestAESEncryptProducesDifferentCiphertexts(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	plain := []byte("Hello, World!")

	c1, err := encrypter.Encrypt(plain)

	if err != nil {
		t.Fatal(err)
	}

	c2, err := encrypter.Encrypt(plain)

	if err != nil {
		t.Fatal(err)
	}

	if reflect.DeepEqual(c1, c2) {
		t.Fatalf("values must differ: %v", c1)
	}
}

// TestAESDecryptEmptyInput verifies empty ciphertext is rejected.
func TestAESDecryptEmptyInput(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	_, _, err = encrypter.Decrypt([]byte{})

	if !errors.Is(err, aescrypto.ErrMismatchedAESNonceSize) {
		t.Fatalf("expected error %v, got %v", aescrypto.ErrMismatchedAESNonceSize, err)
	}
}

// TestAESEncryptAfterCloseReturnsError verifies closed adapters reject encryption.
func TestAESEncryptAfterCloseReturnsError(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	encrypter.Stop(context.Background())

	_, err = encrypter.Encrypt([]byte("Hello, World!"))

	if !errors.Is(err, crypto.ErrEncrypterClosed) {
		t.Fatalf("expected error %v, got %v", crypto.ErrEncrypterClosed, err)
	}
}

// TestAESDecryptAfterCloseReturnsError verifies closed adapters reject decryption.
func TestAESDecryptAfterCloseReturnsError(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	encrypter.Stop(context.Background())

	_, _, err = encrypter.Decrypt([]byte("some ciphertext that is long enough"))

	if !errors.Is(err, crypto.ErrEncrypterClosed) {
		t.Fatalf("expected error %v, got %v", crypto.ErrEncrypterClosed, err)
	}
}

// TestAESCloseNilsAEAD verifies Close releases the AEAD cipher.
func TestAESCloseNilsAEAD(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	encrypter.Stop(context.Background())

	// After Close, the internal AEAD is nil, so Encrypt must fail
	// with crypto.ErrEncrypterClosed rather than panicking.
	_, err = encrypter.Encrypt([]byte("test"))

	if !errors.Is(err, crypto.ErrEncrypterClosed) {
		t.Fatalf("expected error %v, got %v", crypto.ErrEncrypterClosed, err)
	}
}

// TestAESStopClosesDriver verifies Stop makes later calls fail as closed.
func TestAESStopClosesDriver(t *testing.T) {
	t.Parallel()

	encrypter, err := newEncryptionDriver([]byte("12345678901234567890123456789012"))

	if err != nil {
		t.Fatal(err)
	}

	if err := encrypter.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := encrypter.Encrypt([]byte("data")); !errors.Is(err, crypto.ErrEncrypterClosed) {
		t.Fatalf("Encrypt error = %v, want ErrEncrypterClosed", err)
	}
}

// TestAESAdditionalDataIsCopied verifies later changes to the caller's slice do not affect the driver.
func TestAESAdditionalDataIsCopied(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	data := []byte("user-42")
	driver, err := newEncryptionDriver(key, func(encrypter *aescrypto.Builder) {
		encrypter.AdditionalData(data)
	})

	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := driver.Encrypt([]byte("Hello"))

	if err != nil {
		t.Fatal(err)
	}

	copy(data, "user-99")

	if _, _, err := driver.Decrypt(ciphertext); err != nil {
		t.Fatal(err)
	}
}
