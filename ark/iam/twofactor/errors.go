package twofactor

import (
	"errors"
	"net/http"

	"arkoren.dev/ark/iam/internal/storage"
	"arkoren.dev/foundation/problem"
)

// ErrNotMigrated is returned by [Factors.Ping] when the tables do not exist.
// Add [Factors.Migrations] to the application's migrator and run them.
//
// Example:
//
//	if errors.Is(factors.Ping(ctx), twofactor.ErrNotMigrated) {
//		// run the migrator
//	}
var ErrNotMigrated = storage.ErrNotMigrated

// ErrInvalidCode is the one answer to a wrong, expired, replayed or spent code,
// and to a user who has no such credential, so a response never tells which. It
// is a problem with status 422.
//
// Example:
//
//	_, err := factors.Verify(ctx, subject, "123456")
//	if errors.Is(err, twofactor.ErrInvalidCode) {
//		// ask for the code again
//	}
var ErrInvalidCode = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:twofactor:invalid-code")
	details.Title("Invalid code")
	details.Detail("The code is incorrect or has already been used.")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrAlreadyEnabled is returned by setup when the user already has an
// authenticator. Remove it first to enroll another. It is a problem with status
// 409.
//
// Example:
//
//	_, err := factors.Enroll(ctx, subject, "ada@example.com")
//	if errors.Is(err, twofactor.ErrAlreadyEnabled) {
//		// two-factor is already on
//	}
var ErrAlreadyEnabled = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:twofactor:already-enabled")
	details.Title("Two-factor already enabled")
	details.Detail("Two-factor authentication is already enabled. Disable it before enrolling another authenticator.")
	details.Status(http.StatusConflict)
})

// ErrNotEnabled is returned when an operation needs an enabled authenticator
// and the user has none. It is a problem with status 409.
//
// Example:
//
//	_, err := factors.Regenerate(ctx, subject)
//	if errors.Is(err, twofactor.ErrNotEnabled) {
//		// enroll first
//	}
var ErrNotEnabled = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:twofactor:not-enabled")
	details.Title("Two-factor not enabled")
	details.Detail("Two-factor authentication is not enabled.")
	details.Status(http.StatusConflict)
})

// ErrNotEnrolling is returned by [Factors.Activate] when the user never ran
// setup, so there is no pending secret to confirm. It is a problem with status
// 409.
//
// Example:
//
//	_, err := factors.Activate(ctx, subject, "123456")
//	if errors.Is(err, twofactor.ErrNotEnrolling) {
//		// call setup first
//	}
var ErrNotEnrolling = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:twofactor:not-enrolling")
	details.Title("No enrollment in progress")
	details.Detail("Start the setup before confirming a code.")
	details.Status(http.StatusConflict)
})

// ErrThrottled is returned when too many wrong codes were entered while
// enabling two-factor. The response carries Retry-After in seconds. It is a
// problem with status 429.
//
// Example:
//
//	if errors.Is(err, twofactor.ErrThrottled) {
//		// wait for Retry-After
//	}
var ErrThrottled = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:twofactor:throttled")
	details.Title("Too many attempts")
	details.Detail("Too many wrong codes. Try again later.")
	details.Status(http.StatusTooManyRequests)
})

// ErrInvalidConfig is wrapped by the errors of [NewFactors] for an unusable
// configuration.
//
// Example:
//
//	_, err := twofactor.NewFactors(nil, nil, nil, totp.Scheme{}, nil)
//	fmt.Println(errors.Is(err, twofactor.ErrInvalidConfig)) // true
var ErrInvalidConfig = errors.New("twofactor: invalid configuration")
