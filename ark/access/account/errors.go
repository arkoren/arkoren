package account

import (
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"

	"arkoren.dev/ark/access/internal/web"
)

// ErrEmailTaken is returned when an address already belongs to a user. The
// package never links accounts by email: the user is asked to log in and
// connect the provider or credential from their settings instead. It is a
// problem with status 409.
//
// Example:
//
//	_, err := directory.Create(ctx, "ada@example.com")
//	if errors.Is(err, account.ErrEmailTaken) {
//		// ask the user to log in
//	}
var ErrEmailTaken = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:account:email-taken")
	details.Title("Email already registered")
	details.Detail("An account with this email already exists. Log in and connect new sign-in methods from your settings.")
	details.Status(http.StatusConflict)
})

// ErrInvalidEmail is returned for an address that is empty, malformed, carries
// a display name or exceeds 254 bytes. It is a problem with status 422.
//
// Example:
//
//	_, err := directory.Create(ctx, "not an email")
//	fmt.Println(errors.Is(err, account.ErrInvalidEmail)) // true
var ErrInvalidEmail = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:account:invalid-email")
	details.Title("Invalid email")
	details.Detail("The email address is not valid.")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrInvalidToken is returned for a verification token that is unknown,
// expired, already used or replaced by a newer one. One response covers all of
// them. It is a problem with status 422.
//
// Example:
//
//	_, err := directory.Verify(ctx, token)
//	if errors.Is(err, account.ErrInvalidToken) {
//		// ask the user to request a new link
//	}
var ErrInvalidToken = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:account:invalid-token")
	details.Title("Invalid token")
	details.Detail("The token is invalid or has expired.")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrEmailUnverified is returned by [RequireVerified] for a user who has not
// verified their email, and by the connect, token and two-factor setup handlers
// of the other packages when their Verified setting says so. It is a problem
// with status 403.
//
// Example:
//
//	router.With(account.RequireVerified()).Post("/posts", create)
var ErrEmailUnverified = web.ErrEmailUnverified

// ErrWrongAccount is returned by [Directory.VerifyFor] and the verify handler
// when the verification token belongs to another account than the logged-in
// one. The token is not consumed. It is a problem with status 403.
//
// Example:
//
//	_, err := directory.VerifyFor(ctx, subject, token)
//	if errors.Is(err, account.ErrWrongAccount) {
//		// ask the user to log in as the account the email was sent for
//	}
var ErrWrongAccount = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:account:verify-wrong-account")
	details.Title("Wrong account")
	details.Detail("This verification link belongs to another account. Log in as that account and open it again.")
	details.Status(http.StatusForbidden)
})

// ErrThrottled is returned by [Directory.ResendHandler] when a user or a client
// address asked for too many verification emails. It is a problem with status
// 429 and a Retry-After header.
//
// Example:
//
//	if errors.Is(err, account.ErrThrottled) {
//		// ask the user to wait
//	}
var ErrThrottled = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:account:throttled")
	details.Title("Too many requests")
	details.Detail("Too many verification emails were requested. Try again later.")
	details.Status(http.StatusTooManyRequests)
})

// ErrNoDelivery is returned by [Directory.SendVerification] when no
// [Builder.Deliver] function is configured. It is a configuration error, not a
// problem, so a request that hits it answers 500.
//
// Example:
//
//	if errors.Is(err, account.ErrNoDelivery) {
//		// configure Builder.Deliver
//	}
var ErrNoDelivery = errors.New("account: no delivery function configured")
