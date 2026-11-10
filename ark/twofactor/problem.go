package twofactor

import (
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"
)

// ErrInvalidConfig reports arguments or settings that cannot establish a
// [TwoFactor]: a nil database, cipher or hasher, an invalid identity, a database
// dialect without SQL support, or an invalid [Builder] setting. [New] wraps it
// with the specific cause.
//
// Example:
//
//	_, err := twofactor.New(identity, nil, cipher, hasher)
//	if errors.Is(err, twofactor.ErrInvalidConfig) {
//		return err // Supply the application's database.
//	}
var ErrInvalidConfig = errors.New("invalid two-factor configuration")

// ErrNotFound reports that the ark's tables do not exist. [TwoFactor.Ping]
// wraps it and names [TwoFactor.Migrations], which the application must add to
// its migrator and run before the ark is used.
//
// Example:
//
//	if err := factors.Ping(ctx); errors.Is(err, twofactor.ErrNotFound) {
//		return err // Run the ark's migrations.
//	}
var ErrNotFound = errors.New("two-factor tables not found")

// ErrInvalidCode is returned, rendered as 401, when a submitted code does not
// complete the second factor. One value covers every failure of the check: a
// wrong code, a code whose time step was already used, a recovery code that
// does not exist or was spent, and a user without a confirmed enrollment, so
// the response does not tell an attacker which of these happened.
//
// The ark does not count failed attempts. Put a rate limit keyed by the session
// ID in front of [TwoFactor.Verify] and [TwoFactor.Recover]: a pending login
// is a login in progress and its codes can be guessed until it expires.
//
// Example:
//
//	if errors.Is(err, twofactor.ErrInvalidCode) {
//		// Ask for the code again.
//	}
var ErrInvalidCode = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/twofactor:twofactor:invalid-code")
	details.Title("Invalid code")
	details.Detail("The code is not valid.")
	details.Status(http.StatusUnauthorized)
})

// ErrAlreadyEnrolled is returned, rendered as 409, by [TwoFactor.Enroll] when the
// user already has a confirmed enrollment. Call [TwoFactor.Disable] first to
// replace it.
//
// Example:
//
//	if errors.Is(err, twofactor.ErrAlreadyEnrolled) {
//		// Offer to disable the current authenticator first.
//	}
var ErrAlreadyEnrolled = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/twofactor:twofactor:already-enrolled")
	details.Title("Already enrolled")
	details.Detail("Two-factor authentication is already enabled.")
	details.Status(http.StatusConflict)
})

// ErrNotEnrolling is returned, rendered as 409, by [TwoFactor.Confirm] when the
// user has no enrollment waiting for its first code, either because
// [TwoFactor.Enroll] was never called or because it was already confirmed.
//
// Example:
//
//	if errors.Is(err, twofactor.ErrNotEnrolling) {
//		// Start over with Enroll.
//	}
var ErrNotEnrolling = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/twofactor:twofactor:not-enrolling")
	details.Title("Not enrolling")
	details.Detail("There is no two-factor enrollment waiting for confirmation.")
	details.Status(http.StatusConflict)
})
