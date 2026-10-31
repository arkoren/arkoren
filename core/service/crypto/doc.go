// Package crypto defines the authenticated-encryption contract and an Encrypter
// facade for typed values. It uses its primary driver for encryption and tries
// configured previous drivers in order for decryption, allowing gradual rotation.
//
// Construction policies run directly once in order on a fresh Builder. Nil
// entries panic; expanded nil/empty slices supply no edits. Previous appends
// copied driver interfaces; the live drivers themselves remain shared. New
// validates the primary before policies and all fallbacks afterwards. It does
// not encrypt, decrypt, connect or start a driver during construction.
//
// Optional aggregate metrics count and time complete facade calls, including
// JSON encoding/decoding failures and all key attempts. Decryption records one
// final logical outcome, never primary or intermediate fallback failures.
// A successful previous_key outcome means the caller can rotate the value,
// not that a replacement was persisted. DecryptWithRotation's boolean reports
// which driver authenticated the ciphertext, even if later JSON decoding fails;
// only use or rotate the decoded value after a nil error. A decoding failure
// records an error outcome, never previous_key. Labels contain bounded operation,
// outcome and error classes, never keys, plaintext, ciphertext or native errors.
// No spans or events are created. Nil Builder.Telemetry follows telemetry.Default
// per call; telemetry.New(nil) disables reporting without timing or instrument
// construction on calls. Published telemetry selections detach from the draft;
// the telemetry service remains shared.
// Driver and application JSON codec panics propagate unchanged. A panicking
// call has no completed logical result and records neither count nor duration,
// including when a previous key succeeded before JSON decoding panicked.
//
// Example:
//
//	encrypter, err := crypto.New(current, func(encrypter *crypto.Builder) {
//		encrypter.Previous(previous)
//	})
//	if err != nil {
//		return err
//	}
//	_ = encrypter
package crypto
