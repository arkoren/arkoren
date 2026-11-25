package cryptotest_test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"testing"

	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/crypto/cryptotest"
	"arkoren.dev/foundation/key"
)

// ringDriver is a minimal AES-GCM driver over a key ring, standing in for a
// vendor driver so the suite, including its rotation check, runs in core.
type ringDriver struct {
	// aeads holds one AEAD per ring key; aeads[0] is the current key.
	aeads []cipher.AEAD
}

// newRingDriver builds a ringDriver whose first AEAD uses the ring's current key.
func newRingDriver(ring key.Ring[key.Shared]) (*ringDriver, error) {
	driver := &ringDriver{}

	for _, shared := range append([]key.Shared{ring.Current()}, ring.Previous()...) {
		block, err := aes.NewCipher(shared.Bytes())

		if err != nil {
			return nil, fmt.Errorf("new cipher: %w", err)
		}

		aead, err := cipher.NewGCM(block)

		if err != nil {
			return nil, fmt.Errorf("new GCM: %w", err)
		}

		driver.aeads = append(driver.aeads, aead)
	}

	return driver, nil
}

// Encrypt seals value under the current key with a random nonce prefix.
func (driver *ringDriver) Encrypt(value []byte) ([]byte, error) {
	nonce := make([]byte, driver.aeads[0].NonceSize())

	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}

	return driver.aeads[0].Seal(nonce, nonce, value, nil), nil
}

// Decrypt opens value with the current key, then each previous key.
func (driver *ringDriver) Decrypt(value []byte) ([]byte, bool, error) {
	for index, aead := range driver.aeads {
		if len(value) < aead.NonceSize() {
			break
		}

		plaintext, err := aead.Open(nil, value[:aead.NonceSize()], value[aead.NonceSize():], nil)

		if err == nil {
			return plaintext, index > 0, nil
		}
	}

	return nil, false, crypto.ErrInvalidCiphertext
}

// TestRun runs the conformance suite, with rotation and foreign-key checks,
// against the in-test ring driver.
func TestRun(t *testing.T) {
	t.Parallel()

	shared, err := key.GenerateShared()

	if err != nil {
		t.Fatal(err)
	}

	foreign, err := key.GenerateShared()

	if err != nil {
		t.Fatal(err)
	}

	build := func(t *testing.T, ring key.Ring[key.Shared]) crypto.Driver {
		driver, err := newRingDriver(ring)

		if err != nil {
			t.Fatal(err)
		}

		return driver
	}
	ringOf := func(t *testing.T, shared key.Shared) key.Ring[key.Shared] {
		ring, err := key.NewRing(shared)

		if err != nil {
			t.Fatal(err)
		}

		return ring
	}

	cryptotest.Run(t, func(t *testing.T) crypto.Driver {
		return build(t, ringOf(t, shared))
	}, cryptotest.WithRing(build), cryptotest.WithForeignDriver(func(t *testing.T) crypto.Driver {
		return build(t, ringOf(t, foreign))
	}))
}
