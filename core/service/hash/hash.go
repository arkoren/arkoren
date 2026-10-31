package hash

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// ErrInvalidHash indicates that a stored hash is malformed or was not
// produced by the driver's algorithm. A password mismatch is not this error:
// Check and CheckRaw return false, nil for a well-formed nonmatching hash.
//
// Example:
//
//	match, err := hasher.Check(password, stored)
//	if errors.Is(err, hash.ErrInvalidHash) {
//		return fmt.Errorf("stored password hash is malformed: %w", err)
//	}
//	if err != nil {
//		return err
//	}
//	_ = match
var ErrInvalidHash = errors.New("invalid hash")

// Driver hashes and verifies raw values and checks whether stored hashes use
// stale parameters.
//
// Implementations must not modify the value slice they are given: the caller
// may keep using it, for example to check a password and then rehash it with
// new parameters.
//
// Hash accepts an empty value and embeds a fresh random salt, so hashing the
// same value twice yields different hashes that both pass Check.
//
// Example:
//
//	var driver hash.Driver = hasher.Driver()
//	stored, err := driver.Hash([]byte("fixture password"))
//	if err != nil {
//		return err
//	}
//	_ = stored // Raw driver calls bypass facade encoding and metrics.
type Driver interface {
	// Hash hashes raw bytes without modifying them. Empty input is valid; the
	// returned hash includes a fresh salt, even for repeated equal inputs.
	// Drivers return errors for unsupported inputs or hashing failures.
	//
	// Example:
	//
	//	stored, err := driver.Hash([]byte("fixture password"))
	//	if err != nil {
	//		return err
	//	}
	//	_ = stored
	Hash(value []byte) ([]byte, error)

	// Check verifies raw bytes against a stored hash. A value that does not
	// match yields (false, nil); a hash the driver cannot parse yields
	// (false, err) with err wrapping [ErrInvalidHash].
	//
	// Example:
	//
	//	match, err := driver.Check([]byte("fixture password"), stored)
	//	if err != nil {
	//		return err
	//	}
	//	_ = match // A false result rejects the password, not the stored hash format.
	Check(value []byte, hash []byte) (bool, error)

	// NeedsRehash reports whether the given hash was produced with different
	// parameters than the current configuration, indicating the value should be
	// re-hashed. A hash the driver cannot parse needs rehashing. The example's
	// persistReplacement function is application-owned password-hash storage.
	//
	// Example:
	//
	//	match, err := driver.Check(password, stored)
	//	if err != nil {
	//		return err
	//	}
	//	if match && driver.NeedsRehash(stored) {
	//		replacement, err := driver.Hash(password)
	//		if err != nil {
	//			return err
	//		}
	//		if err := persistReplacement(replacement); err != nil {
	//			return err
	//		}
	//	}
	NeedsRehash(hash []byte) bool
}

// Hasher provides typed hashing over a [Driver], with optional aggregate metrics.
// It is immutable after New and safe for concurrent use when the driver is.
// Construct it with New; the zero Hasher has no driver for operations.
//
// Example:
//
//	hasher := hash.New(driver)
//	stored, err := hasher.Hash("password")
//	if err != nil {
//		return err
//	}
//	_ = stored
type Hasher struct {
	// driver is the underlying contract implementation used for delegation.
	driver Driver

	// telemetry is the published metrics selection; nil follows the default.
	telemetry *telemetry.Telemetry

	// instruments caches metrics for the resolved service, safely across calls.
	instruments *telemetry.Instruments[instruments]
}

// New creates a Hasher that delegates to driver. Construction callbacks run
// directly once, synchronously in order on one fresh draft; nil entries panic
// and nil/empty expansions omit edits. Retained drafts cannot change published
// settings; the driver and telemetry service remain shared. Callbacks are not
// retained. New makes no driver calls and does not validate driver. An operation
// that reaches a nil Driver interface panics; the caller retains any
// driver-specific lifecycle.
//
// Example:
//
//	hasher := hash.New(driver, func(hasher *hash.Builder) {
//		hasher.Telemetry(telemetry.New(nil))
//	})
//	_ = hasher
func New(driver Driver, configure ...BuilderFunc) *Hasher {
	hasher := &Builder{}

	for _, configure := range configure {
		configure(hasher)
	}

	return &Hasher{
		driver:      driver,
		telemetry:   hasher.telemetry,
		instruments: telemetry.NewInstruments(newInstruments),
	}
}

// Driver returns the shared underlying hashing driver, not a copy. Direct
// driver calls use raw bytes and bypass this Hasher's encoding, buffer clearing,
// and aggregate metrics. The caller still owns any driver-specific lifecycle.
//
// Example:
//
//	stored, err := hasher.Driver().Hash([]byte("fixture password"))
//	if err != nil {
//		return err
//	}
//	_ = stored
func (hasher *Hasher) Driver() Driver {
	return hasher.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() Driver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver. A failed match returns the zero T
// and false. Native calls on a matched driver bypass facade metrics.
//
// With argon2hash imported from arkoren.dev/driver/argon2/argon2hash:
//
//	driver, ok := hasher.DriverAs[*argon2hash.Driver]()
//	if !ok {
//		return errors.New("hasher does not use Argon2")
//	}
//	needed := driver.NeedsRehash(stored)
//	_ = needed // Rehash only after a successful password check.
func (hasher *Hasher) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](hasher.Driver())
}

// Hash hashes value. Exact string and []byte values use their raw bytes, so
// Hash("secret") and HashRaw([]byte("secret")) use the same password bytes,
// although fresh salts make their stored hashes different. Other types,
// including named string or slice types, are JSON-encoded first. Check with
// the same encoding or explicitly convert a named value to string or []byte
// when raw-byte behavior is intended. bcrypt drivers reject inputs over 72
// encoded bytes. Hash zeroes its own encoded copy after hashing, never value.
// Metrics cover returned encoding and hashing outcomes once, including errors.
// A panic propagates without recording a completed operation.
//
// Example:
//
//	stored, err := hasher.Hash("password")
//	if err != nil {
//		return err
//	}
//	_ = stored
func (hasher *Hasher) Hash[T any](value T) (result []byte, err error) {
	operation := hasher.begin("hash")

	encoded, err := encode(value)

	if err != nil {
		operation.end("error", err)

		return nil, fmt.Errorf("encode hashed value: %w", err)
	}

	defer clear(encoded)

	result, err = hasher.driver.Hash(encoded)
	operation.end("ok", err)

	return result, err
}

// Check encodes value as [Hasher.Hash] does and verifies it against valueHash.
// Exact string and []byte values use raw bytes; named string and slice types
// use JSON. Converting between those forms can cause a password mismatch even
// when their underlying bytes look identical. Like Hash, Check zeroes only
// its own encoded copy, never value.
// Metrics include encoding failures; false, nil is a mismatch, not an error.
// A panic propagates without recording a completed operation or mismatch.
//
// Example:
//
//	match, err := hasher.Check("attempt", stored)
//	if err != nil {
//		return err
//	}
//	_ = match
func (hasher *Hasher) Check[T any](value T, valueHash []byte) (match bool, err error) {
	operation := hasher.begin("check")

	encoded, err := encode(value)

	if err != nil {
		operation.end("error", err)

		return false, fmt.Errorf("encode checked value: %w", err)
	}

	defer clear(encoded)

	match, err = hasher.driver.Check(encoded, valueHash)
	operation.check(match, err)

	return match, err
}

// HashRaw hashes raw bytes. It does not modify value, so the caller may reuse
// it, for example to rehash a password after a successful [Hasher.CheckRaw].
// A normal return records one hash operation without encoding; panics propagate
// without recording a completed operation.
//
// Example:
//
//	stored, err := hasher.HashRaw(password)
//	if err != nil {
//		return err
//	}
//	_ = stored
func (hasher *Hasher) HashRaw(value []byte) (result []byte, err error) {
	operation := hasher.begin("hash")

	result, err = hasher.driver.Hash(value)
	operation.end("ok", err)

	return result, err
}

// CheckRaw verifies raw bytes against valueHash. It does not modify value.
// Its metrics distinguish mismatch (false, nil) from malformed hashes or errors.
// A panic propagates without recording a completed operation or mismatch.
//
// Example:
//
//	match, err := hasher.CheckRaw(password, stored)
//	if err != nil {
//		return err
//	}
//	_ = match
func (hasher *Hasher) CheckRaw(value, valueHash []byte) (match bool, err error) {
	operation := hasher.begin("check")

	match, err = hasher.driver.Check(value, valueHash)
	operation.check(match, err)

	return match, err
}

// NeedsRehash reports whether valueHash uses stale driver parameters.
// Its metrics report policy observations, never a completed password update.
// A panic propagates without recording a completed policy observation.
//
// Example:
//
//	needed := hasher.NeedsRehash(stored)
//	_ = needed // Rehash only after a successful password check.
func (hasher *Hasher) NeedsRehash(valueHash []byte) (needed bool) {
	operation := hasher.begin("needs_rehash")
	needed = hasher.driver.NeedsRehash(valueHash)
	operation.rehash(needed)

	return needed
}

// encode uses raw bytes only for exact string and []byte dynamic types;
// named string/slice types and other values use JSON. The successful result
// is caller-owned storage that can be cleared without touching value; a nil
// []byte input remains nil. JSON encoding errors propagate to Hash or Check
// before the driver is called.
func encode[T any](value T) ([]byte, error) {
	switch typed := any(value).(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return bytes.Clone(typed), nil
	default:
		return json.Marshal(value)
	}
}
