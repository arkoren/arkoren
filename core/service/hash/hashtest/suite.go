package hashtest

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"arkoren.dev/core/service/hash"
)

// Option changes one conformance suite setting.
type Option func(*suite)

// WithOtherParameters supplies a factory for a driver of the same algorithm
// configured with different cost parameters. It enables the check that
// NeedsRehash reports hashes produced with other parameters.
func WithOtherParameters(newOther func(t *testing.T) hash.Driver) Option {
	return func(s *suite) {
		s.newOther = newOther
	}
}

// suite holds the settings and driver factory of one [Run].
type suite struct {
	// newDriver creates the driver under test.
	newDriver func(t *testing.T) hash.Driver

	// newOther optionally creates a driver with different parameters.
	newOther func(t *testing.T) hash.Driver
}

// Run checks that the drivers created by newDriver follow the [hash.Driver]
// contract: values round-trip through Hash and Check, mismatches yield
// (false, nil), malformed hashes yield [hash.ErrInvalidHash], hashes are
// salted, NeedsRehash tracks parameters, and input slices are left unmodified.
//
// newDriver is called once per subtest. Use low-cost parameters to keep the
// suite fast.
//
// # Example
//
//	func TestDriver(t *testing.T) {
//		hashtest.Run(t, func(t *testing.T) hash.Driver {
//			return bcrypthash.New(func(hasher *bcrypthash.Builder) {
//				hasher.Cost(4)
//			})
//		})
//	}
func Run(t *testing.T, newDriver func(t *testing.T) hash.Driver, opts ...Option) {
	s := &suite{newDriver: newDriver}

	for _, opt := range opts {
		opt(s)
	}

	t.Run("RoundTrip", s.testRoundTrip)
	t.Run("EmptyValue", s.testEmptyValue)
	t.Run("WrongValue", s.testWrongValue)
	t.Run("SaltsDiffer", s.testSaltsDiffer)
	t.Run("InvalidHash", s.testInvalidHash)
	t.Run("NeedsRehash", s.testNeedsRehash)
	t.Run("PreservesInput", s.testPreservesInput)
	t.Run("Concurrent", s.testConcurrent)
}

// mustHash hashes a copy of value with driver.
func mustHash(t *testing.T, driver hash.Driver, value string) []byte {
	t.Helper()

	hashed, err := driver.Hash([]byte(value))

	if err != nil {
		t.Fatalf("Hash(%q) error = %v", value, err)
	}

	return hashed
}

// testRoundTrip verifies a hashed value passes Check.
func (suite *suite) testRoundTrip(t *testing.T) {
	driver := suite.newDriver(t)
	hashed := mustHash(t, driver, "correct horse battery staple")

	matched, err := driver.Check([]byte("correct horse battery staple"), hashed)

	if err != nil || !matched {
		t.Fatalf("Check() = %v, %v, want true, nil", matched, err)
	}
}

// testEmptyValue verifies the empty value can be hashed and checked.
func (suite *suite) testEmptyValue(t *testing.T) {
	driver := suite.newDriver(t)
	hashed := mustHash(t, driver, "")

	matched, err := driver.Check(nil, hashed)

	if err != nil || !matched {
		t.Fatalf("Check(empty) = %v, %v, want true, nil", matched, err)
	}

	matched, err = driver.Check([]byte("x"), hashed)

	if err != nil || matched {
		t.Fatalf("Check(non-empty) = %v, %v, want false, nil", matched, err)
	}
}

// testWrongValue verifies a different value yields (false, nil).
func (suite *suite) testWrongValue(t *testing.T) {
	driver := suite.newDriver(t)
	hashed := mustHash(t, driver, "secret")

	for _, wrong := range []string{"Secret", "secret ", "secre", ""} {
		matched, err := driver.Check([]byte(wrong), hashed)

		if err != nil || matched {
			t.Errorf("Check(%q) = %v, %v, want false, nil", wrong, matched, err)
		}
	}
}

// testSaltsDiffer verifies hashing the same value twice yields distinct
// hashes that both verify.
func (suite *suite) testSaltsDiffer(t *testing.T) {
	driver := suite.newDriver(t)
	first := mustHash(t, driver, "secret")
	second := mustHash(t, driver, "secret")

	if bytes.Equal(first, second) {
		t.Fatal("two hashes of the same value are identical")
	}

	for _, hashed := range [][]byte{first, second} {
		matched, err := driver.Check([]byte("secret"), hashed)

		if err != nil || !matched {
			t.Errorf("Check() = %v, %v, want true, nil", matched, err)
		}
	}
}

// testInvalidHash verifies malformed hashes fail with hash.ErrInvalidHash.
func (suite *suite) testInvalidHash(t *testing.T) {
	driver := suite.newDriver(t)
	hashed := mustHash(t, driver, "secret")

	invalid := map[string][]byte{
		"Nil":       nil,
		"Empty":     {},
		"Garbage":   []byte("not a hash"),
		"Truncated": hashed[:len(hashed)/2],
	}

	for name, stored := range invalid {
		t.Run(name, func(t *testing.T) {
			matched, err := driver.Check([]byte("secret"), stored)

			if matched || !errors.Is(err, hash.ErrInvalidHash) {
				t.Fatalf("Check() = %v, %v, want false, ErrInvalidHash", matched, err)
			}
		})
	}
}

// testNeedsRehash verifies NeedsRehash for current, malformed, and
// differently parameterized hashes.
func (suite *suite) testNeedsRehash(t *testing.T) {
	driver := suite.newDriver(t)

	if driver.NeedsRehash(mustHash(t, driver, "secret")) {
		t.Error("NeedsRehash(current hash) = true, want false")
	}

	for _, stored := range [][]byte{nil, []byte("not a hash")} {
		if !driver.NeedsRehash(stored) {
			t.Errorf("NeedsRehash(%q) = false, want true", stored)
		}
	}

	if suite.newOther == nil {
		return
	}

	if !driver.NeedsRehash(mustHash(t, suite.newOther(t), "secret")) {
		t.Error("NeedsRehash(other parameters) = false, want true")
	}
}

// testPreservesInput verifies Hash and Check leave their value argument
// unchanged, so a caller can check a password and then rehash it.
func (suite *suite) testPreservesInput(t *testing.T) {
	driver := suite.newDriver(t)

	value := []byte("secret")

	hashed, err := driver.Hash(value)

	if err != nil {
		t.Fatal(err)
	}

	assertUnchanged(t, "Hash", value)

	if matched, err := driver.Check(value, hashed); err != nil || !matched {
		t.Fatalf("Check() = %v, %v, want true, nil", matched, err)
	}

	assertUnchanged(t, "Check", value)

	if _, err := driver.Hash(value); err != nil {
		t.Fatal(err)
	}

	assertUnchanged(t, "second Hash", value)
}

// assertUnchanged fails t unless value still holds the suite's test secret.
func assertUnchanged(t *testing.T, method string, value []byte) {
	t.Helper()

	if string(value) != "secret" {
		t.Errorf("%s modified its input: %q", method, value)
	}
}

// testConcurrent verifies one driver serves parallel callers.
func (suite *suite) testConcurrent(t *testing.T) {
	driver := suite.newDriver(t)
	hashed := mustHash(t, driver, "secret")

	var group sync.WaitGroup

	for range 4 {
		group.Go(func() {
			matched, err := driver.Check([]byte("secret"), hashed)

			if err != nil || !matched {
				t.Errorf("Check() = %v, %v, want true, nil", matched, err)
			}
		})
	}

	group.Wait()
}
