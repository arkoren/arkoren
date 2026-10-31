package bcrypthash

// Builder holds the cost policy for one [New] construction. Its zero value
// selects DefaultCost when published; changes after publication have no effect.
//
// Example:
//
//	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
//		hasher.Cost(12)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid")) // true
type Builder struct {
	// config is copied into the driver after all callbacks and normalization.
	config config
}

// BuilderFunc edits one fresh construction draft directly once in order. Nil
// entries panic; expanded nil or empty slices omit edits. New normalizes the
// completed scalar cost, and retained drafts cannot change a published driver.
//
// Example:
//
//	configure := func(hasher *bcrypthash.Builder) {
//		hasher.Cost(12)
//	}
//	policies := []bcrypthash.BuilderFunc{configure}
//	driver := bcrypthash.New(policies...)
//	_ = driver
type BuilderFunc = func(*Builder)

// Cost replaces the hashing cost and returns the same draft. New normalizes
// zero to DefaultCost, then clamps other values to bcrypt's supported range.
// Repeated settings replace earlier values; validation is not performed here.
//
// Example:
//
//	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
//		hasher.Cost(12)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid")) // true
func (hasher *Builder) Cost(cost int) *Builder {
	hasher.config.Cost = cost

	return hasher
}
