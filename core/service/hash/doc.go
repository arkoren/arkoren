// Package hash defines the password-hashing contract and a [Hasher] facade
// that hashes and checks values and reports hashes that need rehashing.
// Exact string and []byte values use raw password bytes. Other types, including
// named string and slice types, use JSON; hash and check with the same encoding,
// or explicitly convert named values when raw-byte behavior is required.
//
// Optional aggregate metrics count and time complete facade calls, including
// encoding failures. No spans or events are created. Nil Builder.Telemetry
// follows telemetry.Default per call; telemetry.New(nil) disables reporting.
// Disabled calls do not read time or construct metric instruments. Labels use
// bounded operation, outcome and error classes, never values, hashes or native
// errors. Check mismatches are normal outcomes. NeedsRehash observations do not
// report persisted hash updates.
// Calls that panic in a driver or application JSON encoder propagate that panic
// unchanged and produce no completed count or duration observation.
//
// Example:
//
//	hasher := hash.New(driver, func(hasher *hash.Builder) {
//		hasher.Telemetry(metrics)
//	})
//	stored, err := hasher.Hash("password")
//	if err != nil {
//		return err
//	}
//	_ = stored
package hash
