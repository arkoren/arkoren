package bcrypthash_test

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/bcrypt/bcrypthash"
)

// storedPassword is a canonical cost-4 bcrypt record for "password" with a
// zero-byte salt. Its fixed encoding makes individual format mutations precise.
const storedPassword = "$2a$04$......................LAtw7/ohmmBAhnXqmkuIz83Rl5Qdjhm"

// TestBcryptRejectsTrailingData prevents native digest truncation from accepting
// a valid record with additional bytes, even when the password matches.
func TestBcryptRejectsTrailingData(t *testing.T) {
	t.Parallel()

	requireInvalidRecord(t, []byte(storedPassword+"trailing"))
}

// TestBcryptRejectsInvalidSalt rejects an alphabet violation during both
// verification and cost inspection, rather than trusting the native Cost parser.
func TestBcryptRejectsInvalidSalt(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[7] = '!'

	requireInvalidRecord(t, stored)
}

// TestBcryptRejectsInvalidDigest distinguishes an invalid digest alphabet from
// the ordinary wrong-password result returned by native comparison.
func TestBcryptRejectsInvalidDigest(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[29] = '!'

	requireInvalidRecord(t, stored)
}

// TestBcryptRejectsTruncatedDigest requires the complete 31-character digest,
// even though the native parser accepts a minor-version record one byte short.
func TestBcryptRejectsTruncatedDigest(t *testing.T) {
	t.Parallel()

	requireInvalidRecord(t, []byte(storedPassword[:59]))
}

// TestBcryptRejectsNoncanonicalSalt rejects nonzero unused bits in the salt's
// final character, which native comparison otherwise accepts as the same salt.
func TestBcryptRejectsNoncanonicalSalt(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[28] = '/'

	requireInvalidRecord(t, stored)
}

// TestBcryptRejectsNoncanonicalDigest rejects nonzero unused digest bits instead
// of classifying the malformed encoding as an incorrect password.
func TestBcryptRejectsNoncanonicalDigest(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[59] = 'n'

	requireInvalidRecord(t, stored)
}

// TestBcryptRejectsSaltNewline prevents the base64 decoder's newline tolerance
// from treating a short salt as a complete stored salt.
func TestBcryptRejectsSaltNewline(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[7] = '\n'

	requireInvalidRecord(t, stored)
}

// TestBcryptRejectsDigestNewline requires every stored digest character to
// belong to bcrypt's alphabet rather than ignoring embedded newlines.
func TestBcryptRejectsDigestNewline(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[29] = '\n'

	requireInvalidRecord(t, stored)
}

// TestBcryptRejectsMissingCostSeparator rejects a malformed header that the
// native cost parser skips without checking its delimiter.
func TestBcryptRejectsMissingCostSeparator(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[6] = '!'

	requireInvalidRecord(t, stored)
}

// TestBcryptRejectsMissingVersionSeparator requires the delimiter between a
// minor-version label and cost, even when the native parser skips that byte.
func TestBcryptRejectsMissingVersionSeparator(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[3] = '!'

	requireInvalidRecord(t, stored)
}

// TestBcryptRejectsSignedCost requires the stored two-digit cost rather than
// accepting a plus sign through the native parser's integer conversion.
func TestBcryptRejectsSignedCost(t *testing.T) {
	t.Parallel()

	stored := []byte(storedPassword)
	stored[4] = '+'

	requireInvalidRecord(t, stored)
}

// requireInvalidRecord verifies that both matching and incorrect plaintexts
// report malformed storage with the contract sentinel and request rehashing.
// It uses the fixture's cost so malformed payloads cannot hide behind a cost
// mismatch in NeedsRehash.
func requireInvalidRecord(t *testing.T, stored []byte) {
	t.Helper()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(4)
	})

	for _, password := range []string{"password", "wrong-password"} {
		matched, err := hasher.Check([]byte(password), stored)

		if matched || !errors.Is(err, hash.ErrInvalidHash) {
			t.Fatalf("Check(%q) = %t, %v, want false, ErrInvalidHash", password, matched, err)
		}
	}

	if !hasher.NeedsRehash(stored) {
		t.Fatal("NeedsRehash(malformed) = false, want true")
	}
}

// TestBcryptPreservesMinorlessRecord verifies that complete legacy $2$ records
// retain successful verification and their original cost policy.
func TestBcryptPreservesMinorlessRecord(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(4)
	})
	stored := []byte("$2" + storedPassword[3:])

	matched, err := hasher.Check([]byte("password"), stored)

	if err != nil || !matched {
		t.Fatalf("Check($2$) = %t, %v, want true, nil", matched, err)
	}

	if hasher.NeedsRehash(stored) {
		t.Fatal("NeedsRehash($2$) = true, want false")
	}
}

// TestBcryptMinorlessPasswordMismatchIsNotMalformed keeps the legacy record's
// ordinary incorrect-password result separate from stored-format failures.
func TestBcryptMinorlessPasswordMismatchIsNotMalformed(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(4)
	})
	stored := []byte("$2" + storedPassword[3:])

	matched, err := hasher.Check([]byte("wrong-password"), stored)

	if err != nil || matched {
		t.Fatalf("Check($2$, wrong password) = %t, %v, want false, nil", matched, err)
	}

	if hasher.NeedsRehash(stored) {
		t.Fatal("NeedsRehash($2$) = true, want false")
	}
}

// TestBcryptPreservesNativeMinorVersions keeps the native minor-version policy,
// including b/x/y compatibility labels and its acceptance of unknown minors.
// The adapter validates storage, not historical implementations of each label.
func TestBcryptPreservesNativeMinorVersions(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(4)
	})

	for _, minor := range []byte{'a', 'b', 'x', 'y', 'z'} {
		stored := []byte(storedPassword)
		stored[2] = minor

		matched, err := hasher.Check([]byte("password"), stored)

		if err != nil || !matched {
			t.Fatalf("Check($2%c$) = %t, %v, want true, nil", minor, matched, err)
		}

		if hasher.NeedsRehash(stored) {
			t.Fatalf("NeedsRehash($2%c$) = true, want false", minor)
		}
	}
}

// TestBcryptValidDigestMismatchIsNotMalformed confirms that canonical digest
// bytes cannot be declared malformed merely because they do not match a password.
func TestBcryptValidDigestMismatchIsNotMalformed(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(4)
	})
	stored := []byte(storedPassword)
	stored[29] = 'M'

	matched, err := hasher.Check([]byte("password"), stored)

	if err != nil || matched {
		t.Fatalf("Check(different digest) = %t, %v, want false, nil", matched, err)
	}

	if hasher.NeedsRehash(stored) {
		t.Fatal("NeedsRehash(valid record) = true, want false")
	}
}

// TestBcryptValidSaltMismatchIsNotMalformed accepts a different canonical salt
// as valid storage, even though the fixture password no longer matches it.
func TestBcryptValidSaltMismatchIsNotMalformed(t *testing.T) {
	t.Parallel()

	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
		hasher.Cost(4)
	})
	stored := []byte(storedPassword)
	stored[7] = '/'

	matched, err := hasher.Check([]byte("password"), stored)

	if err != nil || matched {
		t.Fatalf("Check(different salt) = %t, %v, want false, nil", matched, err)
	}

	if hasher.NeedsRehash(stored) {
		t.Fatal("NeedsRehash(valid record) = true, want false")
	}
}
