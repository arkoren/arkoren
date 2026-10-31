package argon2hash_test

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/matthewhartstonge/argon2"

	"arkoren.dev/core/service/hash"
	"arkoren.dev/core/service/hash/hashtest"
	"arkoren.dev/driver/argon2/argon2hash"
)

// minimalPolicy keeps Argon2 cheap enough for the conformance suite while
// preserving distinct time costs for its rehash checks.
func minimalPolicy(timeCost uint32) argon2hash.BuilderFunc {
	return func(hasher *argon2hash.Builder) {
		hasher.TimeCost(timeCost)
		hasher.MemoryCost(8)
		hasher.Parallelism(1)
	}
}

// TestDriverConformance verifies the driver passes the hash conformance suite.
func TestDriverConformance(t *testing.T) {
	hashtest.Run(t, func(*testing.T) hash.Driver {
		return argon2hash.New(minimalPolicy(1))
	}, hashtest.WithOtherParameters(func(*testing.T) hash.Driver {
		return argon2hash.New(minimalPolicy(2))
	}))
}

// Example demonstrates retaining the complete encoded record and permitting
// authentication only after Check succeeds, before considering policy renewal.
func Example() {
	policies := []argon2hash.BuilderFunc{
		func(hasher *argon2hash.Builder) {
			hasher.TimeCost(3)
		},
	}

	driver := argon2hash.New(policies...)

	stored, err := driver.Hash([]byte("password"))

	if err != nil {
		fmt.Println("hash failed")

		return
	}

	matched, err := driver.Check([]byte("password"), stored)

	if err != nil {
		fmt.Println("verification failed")

		return
	}

	fmt.Println("verified:", matched)
	fmt.Println("needs rehash:", driver.NeedsRehash(stored))
	// Output:
	// verified: true
	// needs rehash: false
}

// TestHashRejectsUnsupportedConfiguredVersion prevents the encoder from
// advertising version 16 for a digest computed with version 19 semantics.
func TestHashRejectsUnsupportedConfiguredVersion(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
		hasher.Version(argon2.Version10)
	})

	stored, err := hasher.Hash([]byte("password"))

	if err == nil {
		t.Fatal("Hash accepted unsupported version 16")
	}

	if stored != nil {
		t.Fatal("Hash returned a record for unsupported version 16")
	}
}

// TestCheckRejectsRelabeledVersion16 verifies that changing only the version
// label cannot authenticate a digest computed with version 19 semantics.
func TestCheckRejectsRelabeledVersion16(t *testing.T) {
	t.Parallel()

	stored := nativeArgon2Record(t, argon2.ModeArgon2id)
	stored = bytes.Replace(stored, []byte("$v=19$"), []byte("$v=16$"), 1)

	matched, err := argon2hash.New().Check([]byte("password"), stored)

	if matched || !errors.Is(err, hash.ErrInvalidHash) {
		t.Fatalf("Check(version 16) = %t, %v; want false, ErrInvalidHash", matched, err)
	}
}

// TestNeedsRehashRejectsUnsupportedStoredVersion verifies that a matching
// unsupported target policy cannot make a version-16 record appear current.
func TestNeedsRehashRejectsUnsupportedStoredVersion(t *testing.T) {
	t.Parallel()

	stored := nativeArgon2Record(t, argon2.ModeArgon2id)
	stored = bytes.Replace(stored, []byte("$v=19$"), []byte("$v=16$"), 1)
	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
		hasher.Version(argon2.Version10)
		hasher.MemoryCost(32)
		hasher.Parallelism(1)
	})

	if !hasher.NeedsRehash(stored) {
		t.Fatal("unsupported version 16 must never be considered current")
	}
}

// TestCheckAcceptsNativeArgon2idVersion19Record preserves verification of
// existing native records rather than requiring records produced by this driver.
func TestCheckAcceptsNativeArgon2idVersion19Record(t *testing.T) {
	t.Parallel()

	stored := nativeArgon2Record(t, argon2.ModeArgon2id)

	matched, err := argon2hash.New().Check([]byte("password"), stored)

	if err != nil || !matched {
		t.Fatalf("Check(native Argon2id version 19) = %t, %v", matched, err)
	}
}

// TestCheckAcceptsNativeArgon2iVersion19Record preserves Argon2i compatibility
// even when the driver's policy for newly hashed passwords selects Argon2id.
func TestCheckAcceptsNativeArgon2iVersion19Record(t *testing.T) {
	t.Parallel()

	stored := nativeArgon2Record(t, argon2.ModeArgon2i)

	matched, err := argon2hash.New().Check([]byte("password"), stored)

	if err != nil || !matched {
		t.Fatalf("Check(native Argon2i version 19) = %t, %v", matched, err)
	}
}

// TestCheckNativeVersion19Mismatch keeps a wrong password distinct from
// unsupported records: an ordinary mismatch must remain false with no error.
func TestCheckNativeVersion19Mismatch(t *testing.T) {
	t.Parallel()

	stored := nativeArgon2Record(t, argon2.ModeArgon2id)

	matched, err := argon2hash.New().Check([]byte("wrong password"), stored)

	if err != nil || matched {
		t.Fatalf("Check(wrong password) = %t, %v; want false, nil", matched, err)
	}
}

// TestNeedsRehashAcceptsNativeVersion19Policy preserves the no-renewal result
// for a supported native record whose complete policy matches the driver.
func TestNeedsRehashAcceptsNativeVersion19Policy(t *testing.T) {
	t.Parallel()

	stored := nativeArgon2Record(t, argon2.ModeArgon2id)
	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
		hasher.MemoryCost(32)
		hasher.Parallelism(1)
	})

	if hasher.NeedsRehash(stored) {
		t.Fatal("supported native record with matching parameters needs no renewal")
	}
}

// TestHashRejectsUnknownConfiguredVersion ensures that version rejection is
// not limited to the native library's named, but unimplemented, Version10 value.
func TestHashRejectsUnknownConfiguredVersion(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
		hasher.Version(argon2.Version(32))
	})

	stored, err := hasher.Hash([]byte("password"))

	if err == nil || stored != nil {
		t.Fatalf("Hash(version 32) = %q, %v; want nil, error", stored, err)
	}
}

// TestCheckRejectsUnknownStoredVersion keeps arbitrary encoded versions from
// being interpreted using the underlying implementation's version-19 behavior.
func TestCheckRejectsUnknownStoredVersion(t *testing.T) {
	t.Parallel()

	stored := nativeArgon2Record(t, argon2.ModeArgon2id)
	stored = bytes.Replace(stored, []byte("$v=19$"), []byte("$v=32$"), 1)

	matched, err := argon2hash.New().Check([]byte("password"), stored)

	if matched || !errors.Is(err, hash.ErrInvalidHash) {
		t.Fatalf("Check(version 32) = %t, %v; want false, ErrInvalidHash", matched, err)
	}
}

// TestCheckRejectsArgon2iVersion16Record applies the same version policy to
// the selectable Argon2i variant, not only to the default Argon2id variant.
func TestCheckRejectsArgon2iVersion16Record(t *testing.T) {
	t.Parallel()

	stored := nativeArgon2Record(t, argon2.ModeArgon2i)
	stored = bytes.Replace(stored, []byte("$v=19$"), []byte("$v=16$"), 1)

	matched, err := argon2hash.New().Check([]byte("password"), stored)

	if matched || !errors.Is(err, hash.ErrInvalidHash) {
		t.Fatalf("Check(Argon2i version 16) = %t, %v; want false, ErrInvalidHash", matched, err)
	}
}

// TestNewAllowsSupportedVersionOverride verifies that only the completed
// construction policy applies; an earlier unsupported version is not sticky.
func TestNewAllowsSupportedVersionOverride(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
		hasher.Version(argon2.Version10)
	}, func(hasher *argon2hash.Builder) {
		hasher.Version(argon2.Version13)
		hasher.MemoryCost(32)
		hasher.Parallelism(1)
	})

	stored, err := hasher.Hash([]byte("password"))

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.HasPrefix(stored, []byte("$argon2id$v=19$")) {
		t.Fatalf("Hash used the wrong final variant/version: %q", stored)
	}
}

// TestHashArgon2iProducesVersion19Record verifies that selecting Argon2i
// affects the published format and remains verifiable under the default policy.
func TestHashArgon2iProducesVersion19Record(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
		hasher.Mode(argon2.ModeArgon2i)
		hasher.MemoryCost(32)
		hasher.Parallelism(1)
	})

	stored, err := hasher.Hash([]byte("password"))

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.HasPrefix(stored, []byte("$argon2i$v=19$")) {
		t.Fatalf("Hash used the wrong variant/version: %q", stored)
	}

	matched, err := argon2hash.New().Check([]byte("password"), stored)

	if err != nil || !matched {
		t.Fatalf("Check(Argon2i output) = %t, %v", matched, err)
	}
}

// nativeArgon2Record prepares a reproducible version-19 compatibility fixture
// through the pinned native API, independently of the driver's Hash method.
// Its small memory budget is test-only; production defaults remain unchanged.
func nativeArgon2Record(t *testing.T, mode argon2.Mode) []byte {
	t.Helper()

	config := argon2.Config{
		HashLength:  32,
		SaltLength:  16,
		TimeCost:    3,
		MemoryCost:  32,
		Parallelism: 1,
		Mode:        mode,
		Version:     argon2.Version13,
	}

	raw, err := config.Hash([]byte("password"), []byte("0123456789abcdef"))

	if err != nil {
		t.Fatal(err)
	}

	return raw.Encode()
}

// TestArgon2HashProducesOutput verifies hashing produces an encoded value.
func TestArgon2HashProducesOutput(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New()
	content := []byte("hello, world")

	hashed, err := hasher.Hash(content)

	if err != nil {
		t.Fatal(err)
	}

	if len(hashed) <= 0 {
		t.Fatal("expected hash output")
	}
}

// TestNewDefaultsZeroConfiguration verifies zero configuration uses defaults.
func TestNewDefaultsZeroConfiguration(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New(argon2TestPolicy(argon2.Config{}))

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	decoded, err := argon2.Decode(hashed)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(argon2.DefaultConfig(), decoded.Config) {
		t.Fatalf("want %v, got %v", argon2.DefaultConfig(), decoded.Config)
	}
}

// TestNewPreservesConfiguredValues verifies configured values are retained.
func TestNewPreservesConfiguredValues(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
		hasher.TimeCost(1)
	})

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	decoded, err := argon2.Decode(hashed)

	if err != nil {
		t.Fatal(err)
	}

	if decoded.Config.TimeCost != uint32(1) {
		t.Fatalf("want %v, got %v", uint32(1), decoded.Config.TimeCost)
	}

	if decoded.Config.MemoryCost != argon2.DefaultConfig().MemoryCost {
		t.Fatalf("want %v, got %v", argon2.DefaultConfig().MemoryCost, decoded.Config.MemoryCost)
	}
}

// TestArgon2CheckMatchesCorrectPassword verifies valid credentials match.
func TestArgon2CheckMatchesCorrectPassword(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New()

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	ok, err := hasher.Check([]byte("hello, world"), hashed)

	if err != nil {
		t.Fatal(err)
	}

	if !ok {
		t.Fatal("expected password to match")
	}
}

// TestArgon2WithCustomConfig verifies custom parameters are accepted.
func TestArgon2WithCustomConfig(t *testing.T) {
	t.Parallel()

	config := argon2.Config{
		HashLength:  32,
		SaltLength:  16,
		TimeCost:    1,
		MemoryCost:  64 * 1024,
		Parallelism: 2,
		Mode:        2,
		Version:     0,
	}

	hasher := argon2hash.New(argon2TestPolicy(config))

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	if len(hashed) <= 0 {
		t.Fatal("expected hash output")
	}
}

// TestArgon2CheckWrongPasswordReturnsFalse verifies mismatches return false.
func TestArgon2CheckWrongPasswordReturnsFalse(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New()

	hashed, err := hasher.Hash([]byte("correct-password"))

	if err != nil {
		t.Fatal(err)
	}

	ok, err := hasher.Check([]byte("wrong-password"), hashed)

	if err != nil {
		t.Fatal(err)
	}

	if ok {
		t.Fatal("expected password not to match")
	}
}

// TestArgon2CheckThenRehashKeepsPassword verifies the check-then-rehash flow:
// Hash and Check leave the caller's slice intact, so the same password can be
// hashed again after a successful check.
func TestArgon2CheckThenRehashKeepsPassword(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New()
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

// TestArgon2NeedsRehashReturnsTrueForDifferentParams verifies differing parameters require rehashing.
func TestArgon2NeedsRehashReturnsTrueForDifferentParams(t *testing.T) {
	t.Parallel()

	config1 := argon2TestConfig(1)
	config2 := argon2TestConfig(3)
	hasher1 := argon2hash.New(argon2TestPolicy(config1))
	hasher2 := argon2hash.New(argon2TestPolicy(config2))

	hashed, err := hasher1.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	if !hasher2.NeedsRehash(hashed) {
		t.Fatal("expected rehash to be needed")
	}
}

// TestArgon2NeedsRehashReturnsTrueForDifferentMode verifies mode changes require rehashing.
func TestArgon2NeedsRehashReturnsTrueForDifferentMode(t *testing.T) {
	t.Parallel()

	config := argon2TestConfig(3)

	hashed, err := argon2hash.New(argon2TestPolicy(config)).Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	config.Mode = 1

	if !argon2hash.New(argon2TestPolicy(config)).NeedsRehash(hashed) {
		t.Fatal("expected rehash to be needed for a different mode")
	}
}

// TestArgon2NeedsRehashReturnsTrueForDifferentSaltLength verifies that raising
// the salt-length policy marks an otherwise identical stored hash for renewal.
func TestArgon2NeedsRehashReturnsTrueForDifferentSaltLength(t *testing.T) {
	t.Parallel()

	config := argon2TestConfig(1)

	stored, err := argon2hash.New(argon2TestPolicy(config)).Hash([]byte("password"))

	if err != nil {
		t.Fatal(err)
	}

	config.SaltLength = 24
	hasher := argon2hash.New(argon2TestPolicy(config))

	if !hasher.NeedsRehash(stored) {
		t.Fatal("a salt-length-only policy change must require rehashing")
	}
}

// TestArgon2NeedsRehashReturnsTrueForDifferentVersion verifies version changes require rehashing.
func TestArgon2NeedsRehashReturnsTrueForDifferentVersion(t *testing.T) {
	t.Parallel()

	config := argon2TestConfig(1)

	hashed, err := argon2hash.New(argon2TestPolicy(config)).Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	config.Version = 0x10

	if !argon2hash.New(argon2TestPolicy(config)).NeedsRehash(hashed) {
		t.Fatal("expected rehash to be needed for a different version")
	}
}

// argon2TestConfig returns a complete test configuration with timeCost.
func argon2TestConfig(timeCost uint32) argon2.Config {
	return argon2.Config{
		HashLength:  32,
		SaltLength:  16,
		TimeCost:    timeCost,
		MemoryCost:  64 * 1024,
		Parallelism: 2,
		Mode:        2,
		Version:     0x13,
	}
}

// TestArgon2NeedsRehashReturnsFalseForSameParams verifies matching parameters avoid rehashing.
func TestArgon2NeedsRehashReturnsFalseForSameParams(t *testing.T) {
	t.Parallel()

	config := argon2.Config{
		HashLength:  32,
		SaltLength:  16,
		TimeCost:    1,
		MemoryCost:  64 * 1024,
		Parallelism: 2,
		Mode:        2,
		Version:     0x13,
	}

	hasher := argon2hash.New(argon2TestPolicy(config))

	hashed, err := hasher.Hash([]byte("hello, world"))

	if err != nil {
		t.Fatal(err)
	}

	if hasher.NeedsRehash(hashed) {
		t.Fatal("expected rehash not to be needed")
	}
}

// argon2TestPolicy captures test parameters by value so variants can reuse a
// policy without sharing drafts or changing previously constructed drivers.
func argon2TestPolicy(config argon2.Config) argon2hash.BuilderFunc {
	return func(hasher *argon2hash.Builder) {
		hasher.HashLength(config.HashLength)
		hasher.SaltLength(config.SaltLength)
		hasher.TimeCost(config.TimeCost)
		hasher.MemoryCost(config.MemoryCost)
		hasher.Parallelism(config.Parallelism)
		hasher.Mode(config.Mode)
		hasher.Version(config.Version)
	}
}

// TestArgon2NeedsRehashReturnsTrueForInvalidHash verifies invalid hashes require rehashing.
func TestArgon2NeedsRehashReturnsTrueForInvalidHash(t *testing.T) {
	t.Parallel()

	hasher := argon2hash.New()

	if !hasher.NeedsRehash([]byte("not-a-valid-hash")) {
		t.Fatal("expected rehash to be needed")
	}
}
