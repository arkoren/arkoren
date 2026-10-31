// Package bcrypthash provides a bcrypt implementation of [hash.Driver].
//
// [Driver] hashes raw bytes for the [hash] facade.
// New runs ordered Builder callbacks and publishes the copied cost. A zero
// cost selects DefaultCost; other values are clamped to bcrypt's supported
// range after callbacks. No lifecycle hooks are needed.
//
// Check and NeedsRehash validate the complete stored record: the 22-character
// salt and 31-character digest must use canonical unpadded bcrypt base64, with
// no trailing data. Malformed records fail Check with [hash.ErrInvalidHash] and
// always need rehashing; a valid record with a wrong password is not malformed.
// Version labels retain native bcrypt handling, including legacy $2$ records.
// A label does not select a historical implementation's algorithm quirks.
// Hash rejects plaintext longer than 72 bytes; enforce that limit before Check
// as well: native comparison can accept an overlength attempt sharing a valid
// 72-byte prefix. Never truncate or migrate an unverified suffix. Rate-limit
// password endpoints because bcrypt intentionally uses CPU.
//
// Example:
//
//	hasher := bcrypthash.New(func(hasher *bcrypthash.Builder) {
//		hasher.Cost(12)
//	})
//	_ = hasher.NeedsRehash([]byte("invalid")) // true
package bcrypthash
