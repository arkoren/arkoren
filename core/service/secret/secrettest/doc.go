// Package secrettest provides a conformance suite for [secret.Driver]
// implementations, modeled on testing/slogtest and testing/fstest.
//
// [Run] executes one subtest per documented behavior of the contract:
// creating and reading secrets, byte payloads, immutable concrete versions
// within one existing logical secret, Update and Delete on missing secrets,
// copy semantics and cancellation. Drivers move bytes only, so the payload
// scenarios write UTF-8 text, a JSON object and arbitrary binary bytes and
// expect each to read back exactly. Version checks read the first payload after
// a later Put and after Update, before deletion. Passing verifies these shared
// behaviors, not identical provider retention, name availability, or resource
// identities.
//
// Backends differ in what they can do, which options express without relaxing
// the contract's semantics:
//
//   - [WithTextOnly] declares a backend that stores UTF-8 text only; the suite
//     expects a binary write to fail with [secret.ErrUnsupported] and store
//     nothing.
//   - [WithJSONObjectValues] declares a backend that stores JSON objects and
//     returns them compact with sorted keys, as HashiCorp Vault does. It implies
//     [WithTextOnly].
//   - [WithReadOnly] with [WithSeed] declares a source that cannot be written
//     (files, environment variables). The suite seeds secrets through the hook,
//     checks reads, and expects every write to fail with [secret.ErrUnsupported].
//   - [WithReference] maps the suite's plain names to the driver's reference
//     syntax, for example by adding the "scheme:" prefix a secretmux driver
//     routes on.
//   - [WithMissingVersion] supplies a well-formed version identifier that was
//     never issued.
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
//
// A read-only source supplies its seeding hook:
//
//	func TestEnvDriver(t *testing.T) {
//		secrettest.Run(t, newEnvDriver, secrettest.WithReadOnly(), secrettest.WithTextOnly(),
//			secrettest.WithSeed(func(t testing.TB, data []byte) string {
//				return setVariable(t, data)
//			}))
//	}
package secrettest
