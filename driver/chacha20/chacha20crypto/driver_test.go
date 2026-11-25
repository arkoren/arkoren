package chacha20crypto_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/crypto/cryptotest"
	"arkoren.dev/driver/chacha20/chacha20crypto"
	"arkoren.dev/foundation/key"
)

// newEncryptionDriver provisions typed material for the existing raw fixtures.
func newEncryptionDriver(raw []byte, configure ...chacha20crypto.BuilderFunc) (*chacha20crypto.Driver, error) {
	material, err := key.NewShared(raw)

	if err != nil {
		return nil, err
	}

	ring, err := key.NewRing(material)

	if err != nil {
		return nil, err
	}

	return chacha20crypto.New(ring, configure...)
}

// TestNewRejectsZeroKey verifies the typed boundary remains fail-closed.
func TestNewRejectsZeroKey(t *testing.T) {
	t.Parallel()

	if _, err := chacha20crypto.New(key.Ring[key.Shared]{}); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("New(zero) = %v", err)
	}
}

// TestNewRejectsAES128Material rejects short material before driver construction.
func TestNewRejectsAES128Material(t *testing.T) {
	t.Parallel()

	if _, err := newEncryptionDriver(make([]byte, 16)); !errors.Is(err, key.ErrInvalid) {
		t.Fatalf("short material = %v", err)
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

	created, err := chacha20crypto.New(ring)

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

// TestOwnedDriverFormattingRedactsKey protects the constructor's owned key
// against diagnostic formatting, without testing its internal representation.
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

	driver, err := chacha20crypto.New(ring)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = driver.Stop(t.Context()) })

	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, driver); got != "[REDACTED ChaCha20 driver]" {
			t.Fatalf("owned driver was not redacted for %s", format)
		}
	}
}

// TestChaCha20NewCreatesEncrypter verifies New accepts a valid key.
func TestChaCha20NewCreatesEncrypter(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	_, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}
}

// TestChaCha20EncryptSucceeds verifies encryption returns ciphertext.
func TestChaCha20EncryptSucceeds(t *testing.T) {
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

// TestChaCha20EncryptDecryptRoundTrip verifies encrypted data can be decrypted.
func TestChaCha20EncryptDecryptRoundTrip(t *testing.T) {
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

	if !bytes.Equal(plain, res) {
		t.Fatalf("want %v, got %v", plain, res)
	}
}

// TestChaCha20NewWithInvalidKeySize verifies invalid keys are rejected.
func TestChaCha20NewWithInvalidKeySize(t *testing.T) {
	t.Parallel()

	_, err := newEncryptionDriver([]byte("short"))

	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestChaCha20DecryptWithShortCiphertext verifies truncated input is rejected.
func TestChaCha20DecryptWithShortCiphertext(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	_, _, err = encrypter.Decrypt([]byte("short"))

	if !errors.Is(err, chacha20crypto.ErrMismatchedChaCha20NonceSize) {
		t.Fatalf("expected error %v, got %v", chacha20crypto.ErrMismatchedChaCha20NonceSize, err)
	}
}

// TestChaCha20DecryptWithCorruptedCiphertext verifies corrupted data is rejected.
func TestChaCha20DecryptWithCorruptedCiphertext(t *testing.T) {
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

// TestChaCha20CloseDoesNotClearCallerKeyMaterial verifies Close preserves caller memory.
func TestChaCha20CloseDoesNotClearCallerKeyMaterial(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	copy(key, "12345678901234567890123456789012")

	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	encrypter.Stop(context.Background())

	want := []byte("12345678901234567890123456789012")

	if !bytes.Equal(want, key) {
		t.Fatalf("want %v, got %v", want, key)
	}
}

// TestChaCha20AdditionalDataMustMatchForDecrypt verifies AAD authentication.
func TestChaCha20AdditionalDataMustMatchForDecrypt(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key, func(encrypter *chacha20crypto.Builder) {
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

	decrypter, err := newEncryptionDriver(key, func(encrypter *chacha20crypto.Builder) {
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

// TestChaCha20AdditionalDataRoundTrip verifies matching AAD decrypts correctly.
func TestChaCha20AdditionalDataRoundTrip(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key, func(encrypter *chacha20crypto.Builder) {
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

	if !bytes.Equal(plain, res) {
		t.Fatalf("want %v, got %v", plain, res)
	}
}

// TestChaCha20EncryptProducesDifferentCiphertexts verifies nonces vary per encryption.
func TestChaCha20EncryptProducesDifferentCiphertexts(t *testing.T) {
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

	if bytes.Equal(c1, c2) {
		t.Fatalf("values must differ: %v", c1)
	}
}

// TestChaCha20DecryptEmptyInput verifies empty ciphertext is rejected.
func TestChaCha20DecryptEmptyInput(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	encrypter, err := newEncryptionDriver(key)

	if err != nil {
		t.Fatal(err)
	}

	_, _, err = encrypter.Decrypt([]byte{})

	if !errors.Is(err, chacha20crypto.ErrMismatchedChaCha20NonceSize) {
		t.Fatalf("expected error %v, got %v", chacha20crypto.ErrMismatchedChaCha20NonceSize, err)
	}
}

// TestChaCha20EncryptAfterCloseReturnsError verifies closed adapters reject encryption.
func TestChaCha20EncryptAfterCloseReturnsError(t *testing.T) {
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

// TestChaCha20DecryptAfterCloseReturnsError verifies closed adapters reject decryption.
func TestChaCha20DecryptAfterCloseReturnsError(t *testing.T) {
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

// TestChaCha20StopClosesDriver verifies Stop makes later calls fail as closed.
func TestChaCha20StopClosesDriver(t *testing.T) {
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

// TestChaCha20AdditionalDataIsCopied verifies later changes to the caller's slice do not affect the driver.
func TestChaCha20AdditionalDataIsCopied(t *testing.T) {
	t.Parallel()

	key := []byte("12345678901234567890123456789012")
	data := []byte("user-42")
	driver, err := newEncryptionDriver(key, func(encrypter *chacha20crypto.Builder) {
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
