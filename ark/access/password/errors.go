package password

import (
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"
)

// ErrInvalidCredentials is the one answer to a failed login or password check:
// unknown user, wrong password, user without a password or disabled user. It is
// a problem with status 422.
//
// Example:
//
//	_, err := verifier.Authenticate(ctx, "ada@example.com", "wrong")
//	if errors.Is(err, password.ErrInvalidCredentials) {
//		// show "wrong email or password"
//	}
var ErrInvalidCredentials = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:password:invalid-credentials")
	details.Title("Invalid credentials")
	details.Detail("The identifier or password is incorrect.")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrInvalidPassword is returned when a new password fails the policy (by
// default 8 to 72 bytes; see [Builder.Policy]). It is a problem with status 422.
//
// Example:
//
//	err := verifier.Set(ctx, subject, "short")
//	fmt.Println(errors.Is(err, password.ErrInvalidPassword)) // true
var ErrInvalidPassword = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:password:invalid-password")
	details.Title("Invalid password")
	details.Detail("The password does not meet the requirements.")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrInvalidToken is returned for a reset token that is unknown, expired, used
// or replaced. It is a problem with status 422.
//
// Example:
//
//	_, err := verifier.Reset(ctx, token, "a new password")
//	if errors.Is(err, password.ErrInvalidToken) {
//		// ask the user to request a new link
//	}
var ErrInvalidToken = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:password:invalid-token")
	details.Title("Invalid token")
	details.Detail("The token is invalid or has expired.")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrThrottled is returned by the login and change handlers when too many
// attempts were made. The response carries Retry-After in seconds. It is a
// problem with status 429.
//
// Example:
//
//	if errors.Is(err, password.ErrThrottled) {
//		// wait for Retry-After
//	}
var ErrThrottled = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:password:throttled")
	details.Title("Too many attempts")
	details.Detail("Too many failed attempts. Try again later.")
	details.Status(http.StatusTooManyRequests)
})

// ErrInvalidConfig is wrapped by the errors of [NewVerifier] for an unusable
// configuration.
//
// Example:
//
//	_, err := password.NewVerifier(nil, nil, nil, nil, nil, password.Users{})
//	fmt.Println(errors.Is(err, password.ErrInvalidConfig)) // true
var ErrInvalidConfig = errors.New("password: invalid configuration")

// ErrNoDelivery is returned by [Verifier.SendReset] when no [Builder.Deliver]
// function is configured. It is a configuration error and answers 500.
//
// Example:
//
//	if errors.Is(err, password.ErrNoDelivery) {
//		// configure Builder.Deliver
//	}
var ErrNoDelivery = errors.New("password: no delivery function configured")

// ErrIdentifierTaken is what [Users.Create] returns, or wraps, when the
// identifier already belongs to a user. [Verifier.RegisterHandler] answers the
// same 202 as for a new user and notifies the owner instead of revealing it, so
// the error never reaches the client. It is a plain error, not a problem: with
// the built-in directory the facade maps its own [account.ErrEmailTaken] to it.
//
// Example:
//
//	create := func(ctx context.Context, identifier string) (string, error) {
//		if exists(identifier) {
//			return "", password.ErrIdentifierTaken
//		}
//		return insert(ctx, identifier)
//	}
var ErrIdentifierTaken = errors.New("password: identifier taken")
