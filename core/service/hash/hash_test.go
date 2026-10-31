package hash_test

import (
	"testing"

	"arkoren.dev/core/service/hash"
)

// TestHashNamedStringUsesJSON protects the documented distinction between a
// named string and the exact string type through the public hashing facade.
func TestHashNamedStringUsesJSON(t *testing.T) {
	t.Parallel()

	// password is an application-owned string type, not an alias for string.
	type password string

	hasher := hash.New(identity{})

	stored, err := hasher.Hash(password("fixture"))

	if err != nil {
		t.Fatal(err)
	}

	if string(stored) != `"fixture"` {
		t.Fatalf("hashed bytes = %q, want JSON string", stored)
	}
}

// TestHashNamedBytesUsesJSON protects named slice encoding without calling the
// facade's private encoder. The expected bytes are a JSON base64 string.
func TestHashNamedBytesUsesJSON(t *testing.T) {
	t.Parallel()

	// credential is a defined byte-slice type whose values use JSON encoding.
	type credential []byte

	hasher := hash.New(identity{})

	stored, err := hasher.Hash(credential("fixture"))

	if err != nil {
		t.Fatal(err)
	}

	if string(stored) != `"Zml4dHVyZQ=="` {
		t.Fatalf("hashed bytes = %q, want JSON base64 string", stored)
	}
}

// TestCheckNamedStringUsesJSON verifies that Check uses the same named-type
// encoding as Hash, rather than treating the underlying string as raw bytes.
func TestCheckNamedStringUsesJSON(t *testing.T) {
	t.Parallel()

	// password represents the same application-owned type on the read path.
	type password string

	hasher := hash.New(identity{})

	match, err := hasher.Check(password("fixture"), []byte(`"fixture"`))

	if err != nil {
		t.Fatal(err)
	}

	if !match {
		t.Fatal("named string did not match its JSON encoding")
	}
}

// TestCheckNamedBytesUsesJSON verifies the matching side of the named-slice
// contract using independently specified JSON bytes rather than an encoder.
func TestCheckNamedBytesUsesJSON(t *testing.T) {
	t.Parallel()

	// credential remains a defined slice type when passed through the facade.
	type credential []byte

	hasher := hash.New(identity{})

	match, err := hasher.Check(credential("fixture"), []byte(`"Zml4dHVyZQ=="`))

	if err != nil {
		t.Fatal(err)
	}

	if !match {
		t.Fatal("named bytes did not match their JSON encoding")
	}
}

// driver records values passed to the hashing contract.
type driver struct {
	// value retains the encoded bytes received by the hashing driver.
	value []byte
	// needsRehash controls the rehash policy returned to the facade.
	needsRehash bool
}

// Hash records value and returns a fixed hash.
func (driver *driver) Hash(value []byte) ([]byte, error) {
	driver.value = value

	return []byte("hash"), nil
}

// Check verifies the fixed encoded test value and hash.
func (driver driver) Check(value, valueHash []byte) (bool, error) {
	return string(value) == `{"name":"arkoren"}` && string(valueHash) == "hash", nil
}

// NeedsRehash returns the configured rehash result.
func (driver driver) NeedsRehash([]byte) bool {
	return driver.needsRehash
}

// TestHasherSerializesValues verifies typed hashing serialization.
func TestHasherSerializesValues(t *testing.T) {
	t.Parallel()

	driver := &driver{}
	hasher := hash.New(driver)

	valueHash, err := hasher.Hash(struct {
		// Name is the JSON field used to verify typed value serialization.
		Name string `json:"name"`
	}{
		Name: "arkoren",
	})

	if err != nil {
		t.Fatal(err)
	}

	ok, err := hasher.Check(struct {
		// Name is the JSON field used to verify typed value serialization.
		Name string `json:"name"`
	}{
		Name: "arkoren",
	}, valueHash)

	if err != nil || !ok {
		t.Fatalf("Check = %t, %v", ok, err)
	}
}

// TestHasherReportsDriverRehashPolicy verifies rehash delegation.
func TestHasherReportsDriverRehashPolicy(t *testing.T) {
	t.Parallel()

	hasher := hash.New(&driver{needsRehash: true})

	if !hasher.NeedsRehash([]byte("hash")) {
		t.Fatal("NeedsRehash = false, want true")
	}
}

// TestHashStringMatchesRaw verifies typed strings and raw bytes use the same encoding.
func TestHashStringMatchesRaw(t *testing.T) {
	t.Parallel()

	hasher := hash.New(identity{})

	fromString, err := hasher.Hash("secret")

	if err != nil {
		t.Fatal(err)
	}

	if string(fromString) != "secret" {
		t.Fatalf("hashed bytes = %q, want raw secret", fromString)
	}
}

// identity is a driver whose hash is its input.
type identity struct{}

// Hash returns a copy of value.
func (identity) Hash(value []byte) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

// Check compares value with hash.
func (identity) Check(value, hasher []byte) (bool, error) {
	return string(value) == string(hasher), nil
}

// NeedsRehash never requests a rehash.
func (identity) NeedsRehash([]byte) bool {
	return false
}

// TestCheckRawThenHashRawKeepsValue verifies the check-then-rehash flow: the
// facade leaves the caller's slice intact so a successful check can be
// followed by hashing the same password.
func TestCheckRawThenHashRawKeepsValue(t *testing.T) {
	t.Parallel()

	hasher := hash.New(identity{})
	password := []byte("secret")

	ok, err := hasher.CheckRaw(password, []byte("secret"))

	if err != nil || !ok {
		t.Fatalf("CheckRaw = %t, %v", ok, err)
	}

	rehashed, err := hasher.HashRaw(password)

	if err != nil {
		t.Fatal(err)
	}

	if string(password) != "secret" || string(rehashed) != "secret" {
		t.Fatalf("password = %q, rehashed = %q", password, rehashed)
	}
}

// TestTypedHashKeepsByteSliceValue verifies Hash and Check zero only their own
// encoded copy of a []byte value.
func TestTypedHashKeepsByteSliceValue(t *testing.T) {
	t.Parallel()

	hasher := hash.New(identity{})
	password := []byte("secret")

	hashed, err := hasher.Hash(password)

	if err != nil {
		t.Fatal(err)
	}

	if ok, err := hasher.Check(password, hashed); err != nil || !ok {
		t.Fatalf("Check = %t, %v", ok, err)
	}

	if string(password) != "secret" || string(hashed) != "secret" {
		t.Fatalf("password = %q, hashed = %q", password, hashed)
	}
}
