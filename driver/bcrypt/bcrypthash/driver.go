package bcrypthash

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"arkoren.dev/core/service/hash"
)

// Driver implements hash.Driver using the bcrypt algorithm.
// It is an acceptable alternative to Argon2 when compatibility
// with existing bcrypt hashes is required.
//
// WARNING: Bcrypt hash operations are intentionally CPU intensive.
// Endpoints that trigger hashing (login, registration, password
// change) should implement rate limiting to prevent
// denial-of-service attacks through hash computation abuse.
// Settings are immutable after construction; the driver supports concurrent use.
//
// Example:
//
//	driver := bcrypthash.New()
//	stored, err := driver.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = stored
type Driver struct {
	// config holds the effective bcrypt parameters used for hashing.
	config config
}

var _ hash.Driver = (*Driver)(nil)

// config configures the bcrypt driver. The Cost parameter
// controls the computational expense of hashing; higher values
// are more secure but slower.
type config struct {
	// Cost controls bcrypt's computational expense. Zero selects
	// [DefaultCost].
	Cost int
}

// DefaultCost is Arkoren's default bcrypt work factor of 12. Measure hashing
// cost and bound concurrency for the deployment; this constant does not set a
// minimum for explicitly selected costs. Zero construction cost restores it.
//
// Example:
//
//	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
//		hasher.Cost(bcrypthash.DefaultCost)
//	})
//	_ = hasher
const DefaultCost = 12

// defaultConfig returns the default bcrypt driver configuration.
func defaultConfig() config {
	return config{
		Cost: DefaultCost,
	}
}

// New creates a bcrypt driver from ordered construction callbacks. A zero cost
// selects [DefaultCost]. Other values are clamped to [bcrypt.MinCost,
// bcrypt.MaxCost].
// Callbacks run directly once, synchronously on one fresh draft; nil entries
// panic and expanded nil/empty slices omit callbacks. Callback panics propagate.
// Retaining the draft cannot change the published driver.
//
// Example:
//
//	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
//		hasher.Cost(12)
//	})
//	encoded, err := hasher.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = encoded // Store the encoded hash, not the password.
func New(configure ...BuilderFunc) *Driver {
	hasher := &Builder{
		config: defaultConfig(),
	}

	for _, configure := range configure {
		configure(hasher)
	}

	config := hasher.config

	if config.Cost == 0 {
		config.Cost = DefaultCost
	}

	config.Cost = min(max(config.Cost, bcrypt.MinCost), bcrypt.MaxCost)

	return &Driver{
		config: config,
	}
}

// Hash generates a bcrypt hash of the given value.
// Bcrypt has a maximum password length of 72 bytes. Passwords exceeding
// this limit will result in an error from the underlying bcrypt library.
// The input value is not modified.
// Output includes a fresh salt and owns its bytes. A too-long new password
// returns bcrypt.ErrPasswordTooLong; it is not a malformed-storage error.
//
// Example:
//
//	stored, err := driver.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = stored // Persist the complete record unchanged.
func (driver *Driver) Hash(value []byte) ([]byte, error) {
	hasher, err := bcrypt.GenerateFromPassword(value, driver.config.Cost)

	if err != nil {
		return nil, err
	}

	return hasher, nil
}

// Check verifies value against a complete bcrypt record. A canonical record
// with a different password returns (false, nil). Malformed storage, including
// invalid salt/digest encoding, missing bytes, or trailing data, returns
// (false, err) with err wrapping [hash.ErrInvalidHash], regardless of value.
// Version handling follows the native library, including legacy $2$ records.
// Neither input is modified. Apply the same 72-byte plaintext limit used when
// creating passwords before calling Check; native verification is unchanged
// and an overlength attempt can match a valid 72-byte prefix. Never truncate
// an attempt and migrate it as though the complete password was verified.
//
// Example:
//
//	hasher := bcrypthash.New()
//	stored, err := hasher.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	matched, err := hasher.Check([]byte("password"), stored)
//	if err != nil {
//		return err
//	}
//	_ = matched // Only true permits authentication.
func (driver *Driver) Check(value []byte, stored []byte) (bool, error) {
	if _, err := validatedCost(stored); err != nil {
		return false, fmt.Errorf("%w: %w", hash.ErrInvalidHash, err)
	}

	err := bcrypt.CompareHashAndPassword(stored, value)

	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("%w: %w", hash.ErrInvalidHash, err)
	}

	return true, nil
}

// NeedsRehash reports whether stored is malformed or its cost differs from the
// configured cost. It applies the same complete-record validation as [Driver.Check]
// without computing a password hash or modifying stored. A supported version
// label alone does not require rehashing. Rehash only after Check succeeds;
// true does not establish that the record or the supplied password is valid.
//
// Example:
//
//	hasher := bcrypthash.New()
//	needed := hasher.NeedsRehash([]byte("invalid"))
//	_ = needed // true; malformed storage must not permit authentication.
func (driver *Driver) NeedsRehash(stored []byte) bool {
	cost, err := validatedCost(stored)

	if err != nil {
		return true
	}

	return cost != driver.config.Cost
}
