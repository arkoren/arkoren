package argon2hash

import "github.com/matthewhartstonge/argon2"

// Builder holds the hashing policy for one [New] construction. Settings replace
// earlier values; New normalizes zero settings to library defaults and copies
// the completed policy. The draft is not a runtime configuration API.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.TimeCost(3)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid")) // true
type Builder struct {
	// config is copied into the driver after callbacks and normalization.
	config config
}

// BuilderFunc edits one construction draft passed to [New]. Callbacks run
// synchronously once in order; nil entries panic and an expanded nil or empty
// slice supplies no edits. New copies completed scalar settings and does not
// retain callbacks. Later draft edits cannot change the driver. The alias
// preserves ordinary function identity, including slices of callbacks.
//
// Example:
//
//	policies := []argon2hash.BuilderFunc{
//		func(hasher *argon2hash.Builder) {
//			hasher.TimeCost(3)
//		},
//	}
//	driver := argon2hash.New(policies...)
//	stored, err := driver.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = stored
type BuilderFunc = func(*Builder)

// HashLength replaces the output length in bytes and returns the same draft.
// Zero restores the library default at publication. Other values pass through
// to the native library without added validation.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.HashLength(32)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid"))
func (hasher *Builder) HashLength(length uint32) *Builder {
	hasher.config.HashLength = length

	return hasher
}

// SaltLength replaces the generated salt length in bytes and returns the same
// draft. Zero restores the library default; other values pass through to the
// native library without added validation. A salt-length-only policy change
// makes NeedsRehash report true, without changing how Check verifies old records.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.SaltLength(16)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid"))
func (hasher *Builder) SaltLength(length uint32) *Builder {
	hasher.config.SaltLength = length

	return hasher
}

// TimeCost replaces the iteration count and returns the same draft. Zero
// restores the library default; other values pass through to the native library
// without added validation.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.TimeCost(3)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid"))
func (hasher *Builder) TimeCost(cost uint32) *Builder {
	hasher.config.TimeCost = cost

	return hasher
}

// MemoryCost replaces memory usage in kibibytes and returns the same draft.
// Zero restores the library default; other values pass through to the native
// library without added validation.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.MemoryCost(64 * 1024)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid"))
func (hasher *Builder) MemoryCost(cost uint32) *Builder {
	hasher.config.MemoryCost = cost

	return hasher
}

// Parallelism replaces the thread count and returns the same draft. Zero
// restores the library default; other values pass through to the native library
// without added validation.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.Parallelism(2)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid"))
func (hasher *Builder) Parallelism(threads uint8) *Builder {
	hasher.config.Parallelism = threads

	return hasher
}

// Mode replaces the new-hash variant and returns the same draft. Zero selects
// the native Argon2id default, not Argon2d. Supported variants are
// argon2.ModeArgon2id and argon2.ModeArgon2i; other values are not validated during
// construction. Prefer Argon2id for new passwords. Use Argon2i for compatibility
// and keep its iteration count at least three to resist time-memory tradeoffs.
// Check accepts either supported stored variant regardless of this setting.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.Mode(argon2.ModeArgon2i)
//		hasher.TimeCost(3)
//	})
//	stored, err := hasher.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = stored // An Argon2i version-19 record.
func (hasher *Builder) Mode(mode argon2.Mode) *Builder {
	hasher.config.Mode = mode

	return hasher
}

// Version replaces the Argon2 version and returns the same draft. Zero restores
// the native default, argon2.Version13, after all callbacks. Only Version13
// (decimal 19) is implemented by the native computation. Other nonzero values,
// including Version10 (decimal 16), are retained by New but make Hash fail before
// computing or returning a mislabeled record.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.Version(argon2.Version13)
//	})
//	stored, err := hasher.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = stored // A version-19 record; Version10 instead makes Hash return an error.
func (hasher *Builder) Version(version argon2.Version) *Builder {
	hasher.config.Version = version

	return hasher
}
