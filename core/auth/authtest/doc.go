// Package authtest provides a conformance suite for [auth.UserDriver]
// implementations, modeled on testing/fstest.
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
