package bcrypthash_test

import (
	"bytes"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"arkoren.dev/core/service/hash"
	"arkoren.dev/core/service/hash/hashtest"
	"arkoren.dev/driver/bcrypt/bcrypthash"
)

// TestDriverConformance verifies the driver passes the hash conformance suite.
func TestDriverConformance(t *testing.T) {
	hashtest.Run(t, func(*testing.T) hash.Driver {
		return bcrypthash.New(func(hasher *bcrypthash.Builder) {
			hasher.Cost(4)
		})
	}, hashtest.WithOtherParameters(func(*testing.T) hash.Driver {
		return bcrypthash.New(func(hasher *bcrypthash.Builder) {
			hasher.Cost(5)
		})
	}))
}

// TestCheckPreservesDocumentedNativeOverlengthPolicy characterizes the legacy
// comparison boundary. Applications must reject overlength attempts before
// calling Check; the driver does not silently truncate or migrate passwords.
func TestCheckPreservesDocumentedNativeOverlengthPolicy(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(4)
	})
	password := bytes.Repeat([]byte{'p'}, 72)

	stored, err := hasher.Hash(password)

	if err != nil {
		t.Fatal(err)
	}

	attempt := append(bytes.Clone(password), '!')

	matched, err := hasher.Check(attempt, stored)

	if err != nil || !matched {
		t.Fatalf("legacy comparison = %t, %v", matched, err)
	}

	if _, err := hasher.Hash(attempt); err == nil {
		t.Fatal("Hash accepted an overlength new password")
	}
}

// TestBcryptHashProducesOutput verifies hashing produces an encoded value.
func TestBcryptHashProducesOutput(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New()
	content := []byte("hello, world")

	hashed, err := hasher.Hash(content)

	if err != nil {
		t.Fatal(err)
	}

	if len(hashed) <= 0 {
		t.Fatalf("expected %d to be greater than %d", len(hashed), 0)
	}
}

// TestBcryptCheckMatchesCorrectPassword verifies valid credentials match.
func TestBcryptCheckMatchesCorrectPassword(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New()

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	ok, err := hasher.Check([]byte("hello, world"), hashed)

	if err != nil {
		t.Fatal(err)
	}

	if !ok {
		t.Fatal("expected true")
	}
}

// TestBcryptWithDefaultConfig verifies zero cost selects the default cost.
func TestBcryptWithDefaultConfig(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(0)
	})

	hashed := hashDefaultConfigValue(t, hasher)
	ok := checkDefaultConfigValue(t, hasher, hashed)

	if !ok {
		t.Fatal("expected true")
	}

	cost := bcryptHashCost(t, hashed)

	if cost != bcrypthash.DefaultCost {
		t.Fatalf("want %v, got %v", bcrypthash.DefaultCost, cost)
	}
}

// hashDefaultConfigValue hashes the default configuration test password.
func hashDefaultConfigValue(t *testing.T, hasher *bcrypthash.Driver) []byte {
	t.Helper()

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	if len(hashed) <= 0 {
		t.Fatalf("expected %d to be greater than %d", len(hashed), 0)
	}

	return hashed
}

// checkDefaultConfigValue checks the default configuration test password.
func checkDefaultConfigValue(t *testing.T, hasher *bcrypthash.Driver, hashed []byte) bool {
	t.Helper()

	ok, err := hasher.Check([]byte("hello, world"), hashed)

	if err != nil {
		t.Fatal(err)
	}

	return ok
}

// bcryptHashCost returns the cost encoded in hashed.
func bcryptHashCost(t *testing.T, hashed []byte) int {
	t.Helper()

	cost, err := bcrypt.Cost(hashed)

	if err != nil {
		t.Fatal(err)
	}

	return cost
}

// TestBcryptCheckWrongPasswordReturnsFalse verifies mismatches return false.
func TestBcryptCheckWrongPasswordReturnsFalse(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New()

	hashed, err := hasher.Hash([]byte("correct-password"))

	if err != nil {
		t.Fatal(err)
	}

	ok, err := hasher.Check([]byte("wrong-password"), hashed)

	if err != nil {
		t.Fatal(err)
	}

	if ok {
		t.Fatal("expected false")
	}
}

// TestBcryptCheckCorruptedHashReturnsError verifies malformed hashes return errors.
func TestBcryptCheckCorruptedHashReturnsError(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New()

	ok, err := hasher.Check([]byte("password"), []byte("not-a-hash"))

	if err == nil {
		t.Fatal("expected an error")
	}

	if ok {
		t.Fatal("expected false")
	}
}

// TestBcryptCheckThenRehashKeepsPassword verifies the check-then-rehash flow:
// Hash and Check leave the caller's slice intact, so the same password can be
// hashed again after a successful check.
func TestBcryptCheckThenRehashKeepsPassword(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New()
	password := []byte("sensitive-data")

	hashed, err := hasher.Hash(password)

	if err != nil {
		t.Fatal(err)
	}

	matched, err := hasher.Check(password, hashed)

	if err != nil || !matched {
		t.Fatalf("Check = %t, %v", matched, err)
	}

	rehashed, err := hasher.Hash(password)

	if err != nil {
		t.Fatal(err)
	}

	if string(password) != "sensitive-data" {
		t.Fatalf("password modified: %q", password)
	}

	matched, err = hasher.Check([]byte("sensitive-data"), rehashed)

	if err != nil || !matched {
		t.Fatalf("Check(rehashed) = %t, %v", matched, err)
	}
}

// TestNewWithCustomCost verifies New preserves a valid custom cost.
func TestNewWithCustomCost(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(14)
	})

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	cost, err := bcrypt.Cost(hashed)

	if err != nil {
		t.Fatal(err)
	}

	if cost != 14 {
		t.Fatalf("want %v, got %v", 14, cost)
	}
}

// TestBcryptNeedsRehashReturnsTrueForDifferentCost verifies changed costs require rehashing.
func TestBcryptNeedsRehashReturnsTrueForDifferentCost(t *testing.T) {
	t.Parallel()

	hasher12 := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(12)
	})
	hasher14 := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(14)
	})

	hashed, err := hasher12.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	if !hasher14.NeedsRehash(hashed) {
		t.Fatal("expected true")
	}
}

// TestBcryptNeedsRehashReturnsFalseForSameCost verifies matching costs avoid rehashing.
func TestBcryptNeedsRehashReturnsFalseForSameCost(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(12)
	})

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	if hasher.NeedsRehash(hashed) {
		t.Fatal("expected false")
	}
}

// TestBcryptNeedsRehashReturnsTrueForInvalidHash verifies invalid hashes require rehashing.
func TestBcryptNeedsRehashReturnsTrueForInvalidHash(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New()

	if !hasher.NeedsRehash([]byte("not-a-valid-hash")) {
		t.Fatal("expected true")
	}
}
