package argon2hash

import (
	"fmt"

	"github.com/matthewhartstonge/argon2"

	"arkoren.dev/core/service/hash"
)

// config mirrors argon2.Config, exposing the full set of tuning parameters
// (memory, iterations, parallelism). Zero values select the defaults.
type config struct {
	// HashLength is the length of the resulting hash in bytes.
	HashLength uint32

	// SaltLength is the length of the generated salt in bytes.
	SaltLength uint32

	// TimeCost is the number of Argon2 iterations.
	TimeCost uint32

	// MemoryCost is the amount of memory to use in kibibytes.
	MemoryCost uint32

	// Parallelism is the number of threads to use.
	Parallelism uint8

	// Mode is the Argon2 variant.
	Mode argon2.Mode

	// Version is the Argon2 version.
	Version argon2.Version
}

// Driver implements [hash.Driver] with Argon2id by default and selectable
// Argon2i for compatibility. Both variants use Argon2 version 19 (0x13).
// New publishes immutable scalar settings, so a driver supports concurrent use;
// Check reads the stored record's policy rather than the driver's current policy.
//
// WARNING: Argon2 hash operations are intentionally CPU and memory
// intensive. Endpoints that trigger hashing (login, registration,
// password change) should implement rate limiting to prevent
// denial-of-service attacks through hash computation abuse.
//
// Example:
//
//	driver := argon2hash.New()
//	stored, err := driver.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	matched, err := driver.Check([]byte("password"), stored)
//	if err != nil {
//		return err
//	}
//	_ = matched // true
type Driver struct {
	// config holds the effective Argon2 parameters used for hashing.
	config argon2.Config
}

var _ hash.Driver = (*Driver)(nil)

// defaultConfig returns the default Argon2 hasher configuration.
func defaultConfig() config {
	native := argon2.DefaultConfig()

	return config{
		HashLength:  native.HashLength,
		SaltLength:  native.SaltLength,
		TimeCost:    native.TimeCost,
		MemoryCost:  native.MemoryCost,
		Parallelism: native.Parallelism,
		Mode:        native.Mode,
		Version:     native.Version,
	}
}

// New creates an Argon2 driver from ordered construction callbacks. Defaults
// come from argon2.DefaultConfig; zero settings restore their library defaults
// after all callbacks. Construction does not validate nonzero native parameters.
// Hash rejects configured versions other than argon2.Version13 before computing
// a digest; New never rewrites an unsupported nonzero version to a supported one.
// Callbacks run directly once, synchronously on one fresh draft; nil entries
// panic and expanded nil/empty slices omit callbacks. Callback panics propagate.
// Retaining the draft cannot change the published driver.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.TimeCost(3)
//	})
//	stored, err := hasher.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = stored
func New(configure ...BuilderFunc) *Driver {
	hasher := &Builder{
		config: defaultConfig(),
	}

	for _, configure := range configure {
		configure(hasher)
	}

	return &Driver{
		config: hasher.config.withDefaults().native(),
	}
}

// native converts config to the argon2 library configuration.
func (config config) native() argon2.Config {
	return argon2.Config{
		HashLength:  config.HashLength,
		SaltLength:  config.SaltLength,
		TimeCost:    config.TimeCost,
		MemoryCost:  config.MemoryCost,
		Parallelism: config.Parallelism,
		Mode:        config.Mode,
		Version:     config.Version,
	}
}

// withDefaults fills unset configuration fields from the defaults.
func (config config) withDefaults() config {
	defaults := defaultConfig()
	config = config.withDefaultEncoding(defaults)
	config = config.withDefaultParameters(defaults)

	return config.withDefaultAlgorithm(defaults)
}

// withDefaultEncoding fills unset encoded-hash parameters from defaults.
func (config config) withDefaultEncoding(defaults config) config {
	if config.HashLength == 0 {
		config.HashLength = defaults.HashLength
	}

	if config.SaltLength == 0 {
		config.SaltLength = defaults.SaltLength
	}

	return config
}

// withDefaultParameters fills unset resource parameters from defaults.
func (config config) withDefaultParameters(defaults config) config {
	if config.TimeCost == 0 {
		config.TimeCost = defaults.TimeCost
	}

	if config.MemoryCost == 0 {
		config.MemoryCost = defaults.MemoryCost
	}

	if config.Parallelism == 0 {
		config.Parallelism = defaults.Parallelism
	}

	return config
}

// withDefaultAlgorithm fills unset Argon2 algorithm parameters from defaults.
func (config config) withDefaultAlgorithm(defaults config) config {
	if config.Mode == 0 {
		config.Mode = defaults.Mode
	}

	if config.Version == 0 {
		config.Version = defaults.Version
	}

	return config
}

// Hash produces a version-19 encoded hash using the configured Argon2 variant
// and a fresh random salt. The result owns its bytes and includes the variant,
// version, resource parameters and salt. Nil and empty values are equivalent;
// neither input is modified. Unsupported configured versions return nil and an
// error before native hashing, rather than mislabeling a version-19 digest.
// Other native failures are returned unchanged.
//
// Example:
//
//	hasher := argon2hash.New()
//	stored, err := hasher.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = stored // Persist the complete encoded record unchanged.
func (driver *Driver) Hash(value []byte) ([]byte, error) {
	if driver.config.Version != argon2.Version13 {
		return nil, fmt.Errorf("hash Argon2: unsupported version %d (only version %d is supported)",
			driver.config.Version, argon2.Version13)
	}

	return driver.config.HashEncoded(nonNil(value))
}

// Check verifies value against the variant and parameters stored in a version-19
// Argon2i or Argon2id record, independently of this driver's hashing policy.
// It returns false, nil for a password mismatch. Malformed records and unsupported
// versions return false and an error wrapping [hash.ErrInvalidHash]; version-16
// records are never reinterpreted as version 19. Neither input is modified or
// retained, and nil plaintext is equivalent to empty plaintext.
// Stored costs are used as-is: accept only application-owned records and bound
// concurrent verification to your memory budget.
//
// Example:
//
//	hasher := argon2hash.New()
//	matched, err := hasher.Check([]byte("password"), stored)
//	if err != nil {
//		return err
//	}
//	_ = matched // Permit authentication only when matched is true.
func (driver *Driver) Check(value []byte, stored []byte) (bool, error) {
	raw, err := decodeHash(stored)

	if err != nil {
		return false, fmt.Errorf("check Argon2 hash: %w: %w", hash.ErrInvalidHash, err)
	}

	matched, err := raw.Verify(nonNil(value))

	if err != nil {
		return false, fmt.Errorf("check Argon2 hash: %w: %w", hash.ErrInvalidHash, err)
	}

	return matched, nil
}

// decodeHash decodes one native record and rejects versions the underlying
// computation cannot implement. The returned salt and digest are native-owned
// decoded copies. Version validation precedes any password computation.
func decodeHash(stored []byte) (argon2.Raw, error) {
	raw, err := argon2.Decode(stored)

	if err != nil {
		return argon2.Raw{}, err
	}

	if raw.Config.Version != argon2.Version13 {
		return argon2.Raw{}, fmt.Errorf("unsupported Argon2 version %d (only version %d is supported)",
			raw.Config.Version, argon2.Version13)
	}

	return raw, nil
}

// NeedsRehash reports whether stored differs from this driver's hash length,
// salt length, resource parameters, variant or version. Malformed and unsupported
// records always need replacement, even if their parameters match this driver.
// The input is not modified or retained. This is a policy observation, not proof
// of authentication: replace a record only after Check succeeds, and never
// migrate an unsupported version by editing its encoded version label.
//
// Example:
//
//	hasher := argon2hash.New()
//	matched, err := hasher.Check(password, stored)
//	if err != nil {
//		return err
//	}
//	if matched && hasher.NeedsRehash(stored) {
//		replacement, err := hasher.Hash(password)
//		if err != nil {
//			return err
//		}
//		_ = replacement // Replace stored only if the original record is still current.
//	}
func (driver *Driver) NeedsRehash(stored []byte) bool {
	raw, err := decodeHash(stored)

	if err != nil {
		return true
	}

	return configsDiffer(raw.Config, driver.config)
}

// configsDiffer reports whether the compared rehash parameters differ.
func configsDiffer(first, second argon2.Config) bool {
	return encodingConfigsDiffer(first, second) ||
		parameterConfigsDiffer(first, second) ||
		algorithmConfigsDiffer(first, second)
}

// encodingConfigsDiffer compares both encoded output lengths. A different
// salt-length policy requires renewal even when the derived hash length agrees.
func encodingConfigsDiffer(first, second argon2.Config) bool {
	return first.HashLength != second.HashLength || first.SaltLength != second.SaltLength
}

// parameterConfigsDiffer reports whether resource parameters differ.
func parameterConfigsDiffer(first, second argon2.Config) bool {
	return first.TimeCost != second.TimeCost ||
		first.MemoryCost != second.MemoryCost ||
		first.Parallelism != second.Parallelism
}

// algorithmConfigsDiffer reports whether Argon2 algorithm parameters differ.
func algorithmConfigsDiffer(first, second argon2.Config) bool {
	return first.Mode != second.Mode || first.Version != second.Version
}

// nonNil returns an empty slice for a nil value because the argon2 library
// rejects nil but accepts the empty value, which the driver must hash.
func nonNil(value []byte) []byte {
	if value == nil {
		return []byte{}
	}

	return value
}
