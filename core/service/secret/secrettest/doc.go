// Package secrettest provides a conformance suite for [secret.Driver]
// implementations, modeled on testing/slogtest and testing/fstest.
//
// [Run] executes one subtest per documented behavior of the contract:
// creating and reading secrets, immutable concrete versions within one existing
// logical secret, Update and Delete on missing secrets, copy semantics and
// cancellation. Version checks read the first payload after a later Put and
// after Update, before deletion. Passing verifies these shared behaviors, not
// identical provider retention, name availability, or resource identities.
//
// Backends differ in what a payload may contain, which is a capability and not
// a semantic: [WithTextValues] restricts payloads to UTF-8 text and
// [WithJSONObjectValues] to compact JSON objects with sorted keys, as
// HashiCorp Vault stores them. [WithMissingVersion] supplies a well-formed
// version identifier that was never issued.
//
// Every subtest uses a unique secret name. The suite does not recreate a name
// after deletion: managed backends can retain deleted objects and delay or
// prevent name availability. Permanent deletion and recreation create a new
// logical secret; recovery of a retained object does not. Selector reuse after
// recreation is permitted, not required. Memory-specific tests characterize
// reuse separately; it is not a portable driver requirement.
//
// Example:
//
//	func TestDriver(t *testing.T) {
//		secrettest.Run(t, func(t *testing.T) secret.Driver {
//			return memorysecret.New()
//		})
//	}
package secrettest
