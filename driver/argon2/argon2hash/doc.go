// Package argon2hash implements [hash.Driver] with Argon2id by default and
// selectable Argon2i. Both hashing and verification support version 19 (0x13).
//
// New runs ordered Builder callbacks and publishes copied scalar settings,
// without lifecycle I/O. Zero settings restore the native library's defaults:
// Argon2id, version 19, three iterations, 64 MiB, four lanes, a 16-byte random salt
// and a 32-byte digest. Bound concurrency and rate-limit password endpoints;
// these costs are intentional. No Start or Stop hook is needed.
//
// Example:
//
//	hasher := argon2hash.New(func(hasher *argon2hash.Builder) {
//		hasher.MemoryCost(64 * 1024)
//	})
//	stored, err := hasher.Hash([]byte("password"))
//	if err != nil {
//		return err
//	}
//	_ = stored // Persist the complete encoded record unchanged.
//
// Check verifies supported Argon2i and Argon2id records using their stored
// parameters, independently of the new-hash policy. Only true, nil authenticates.
// NeedsRehash compares all policy fields, including salt length, but cannot prove
// a password match. Rehash only after successful verification.
//
// The native computation implements only version 19, despite exposing a
// Version10 constant. New preserves nonzero settings without eager validation;
// Hash rejects unsupported configured versions before computation. Check rejects
// unsupported stored versions with an error wrapping hash.ErrInvalidHash, and
// NeedsRehash reports true for those records. Never edit a version-16 label to
// version 19: genuine version-16 records and historically mislabeled records
// cannot be distinguished safely by their labels. Use a correct legacy verifier
// followed by rehashing, or an application-owned password reset.
package argon2hash
