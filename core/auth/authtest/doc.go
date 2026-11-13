// Package authtest provides conformance suites for [auth.UserDriver] and
// [auth.Authenticator] implementations, modeled on testing/fstest.
//
// [Run] checks [auth.Authenticator] implementations against the rules every
// authenticator shares. Call it from the authenticator's tests with factories
// for a request without credentials, one with valid credentials and, through
// [WithInvalid], one with rejected credentials. [Option] is one type that
// serves both suites.
//
// [RunUserDriver] seeds a driver with known [User] fixtures and runs one
// subtest per documented behavior: a found user with its version and claims, a
// missing user, a disabled user and invalid subjects. Fixture subjects are
// decimal digits, so drivers keyed by integers, strings or text types can all
// pass.
//
// Example:
//
//	func TestDriver(t *testing.T) {
//		authtest.RunUserDriver(t, func(t *testing.T, users []authtest.User) auth.UserDriver {
//			driver := memoryauth.New()
//			// Seed driver from users.
//			return driver
//		})
//	}
package authtest
