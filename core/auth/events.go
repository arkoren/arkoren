package auth

import (
	"time"

	"arkoren.dev/core/event"
)

// UserForgottenEvent reports that every forget hook of a user completed. It
// carries only the subject, never personal data.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.UserForgotten, func(ctx context.Context, forgotten auth.UserForgottenEvent) error {
//		logger.InfoContext(ctx, "user forgotten", "subject", forgotten.Subject)
//		return nil
//	})
type UserForgottenEvent struct {
	// Subject is the subject passed to [UserProvider.Forget].
	Subject string
}

// UserForgotten is emitted by [UserProvider.Forget] with [event.Bus.Emit] after
// every hook registered with [UserProvider.OnForget] succeeded, and never when
// a hook failed. It means "forget hooks completed": the delete may still roll
// back with the caller's transaction. A listener whose effect needs the delete
// to be committed wraps that effect in Database.AfterCommit with the event
// context, which carries the transaction. Emit logs listener errors instead of
// returning them, so listeners cannot change the outcome of Forget.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.UserForgotten, func(ctx context.Context, forgotten auth.UserForgottenEvent) error {
//		return search.Remove(ctx, forgotten.Subject)
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var UserForgotten = event.NewTopic[UserForgottenEvent]("auth.user.forgotten")

// RejectedEvent reports anonymous rejection without request paths, which can
// contain identifiers or credentials. It carries no user, credential, header,
// query, or cause. Its event context still contains the request's context values.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.Rejected, func(ctx context.Context, rejected auth.RejectedEvent) error {
//		logger.InfoContext(ctx, "authentication required", "method", rejected.Method)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type RejectedEvent struct {
	// Method is the client-controlled HTTP method, not a route or user identity.
	// Applications must not place secrets in custom method names.
	//
	// Example:
	//	logger.InfoContext(ctx, "authentication required", "method", rejected.Method)
	Method string
}

// Rejected is published when [Require], built with [MiddlewareBuilder.Events], rejects an
// anonymous request. The default response is 401; ErrorResponse can override it.
// Every rejection publishes without deduplication, so a client controls the
// rate. Listeners must be cheap, and
// a rate limiter such as the ratelimit middleware should sit in front of
// [Require]. Requests that pass publish nothing.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.Rejected, onRejected)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Rejected = event.NewTopic[RejectedEvent]("http.auth.rejected")

// CredentialEnrolledEvent reports that a user registered a credential. It
// carries only the subject and the method, never the credential's secret or
// any personal data.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.CredentialEnrolled, func(ctx context.Context, enrolled auth.CredentialEnrolledEvent) error {
//		return notify(ctx, enrolled.Subject, string(enrolled.Method))
//	})
type CredentialEnrolledEvent struct {
	// Subject is the user who enrolled the credential.
	Subject string

	// Method is the method of the enrolled credential, such as [MethodOTP].
	Method Method
}

// CredentialEnrolled is published by the modules that enroll credentials (a
// TOTP authenticator, a passkey, a password) after a first or a new enrolment,
// with [event.Bus.Emit], so an application can tell users that a way to prove
// their identity was added to their account. Core itself never publishes it:
// only the module knows when an enrolment succeeded. Emit logs listener errors,
// so listeners cannot change the outcome of the enrolment.
//
// Example:
//
//	events.Emit(ctx, auth.CredentialEnrolled, auth.CredentialEnrolledEvent{
//		Subject: subject,
//		Method:  auth.MethodOTP,
//	})
var CredentialEnrolled = event.NewTopic[CredentialEnrolledEvent]("auth.credential.enrolled")

// SecondFactorLockedEvent reports that a user used up their second-factor
// attempts and is locked out. It carries the subject and the end of the
// lockout, never a code, credential or request detail.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.SecondFactorLocked, func(ctx context.Context, locked auth.SecondFactorLockedEvent) error {
//		return notify(ctx, locked.Subject, locked.Until)
//	})
type SecondFactorLockedEvent struct {
	// Subject is the user whose second factor and step-up are locked.
	Subject string

	// Until is when the lockout ends, in UTC. Until then every challenge or
	// confirmation of the subject fails without verifying anything, in any
	// session.
	Until time.Time
}

// SecondFactorLocked is published by [Guard.ChallengeHandler] and
// [Guard.ConfirmHandler] with [event.Bus.Emit], once per lockout, when a wrong
// credential uses up the user's [GuardBuilder.MaxAttempts] attempts in a window
// or takes their 24 hour failure budget to a multiple of it, and only when the
// bus was set with [GuardBuilder.Events]. Each lockout lasts the challenge window
// doubled once per MaxAttempts failures the budget already holds before this one,
// capped at 24 hours (at the window when that is longer). The budget expires 24
// hours after its first failure; a successful challenge or [Guard.Unlock] clears
// it. When several requests race to lock, one publishes.
//
// The application must subscribe to this event itself: ark/access does not do it
// yet. Treat every event as a failed login for the throttle that guards your
// login route. Anyone who knows a subject can lock it, and a locked user cannot
// finish a pending login, but without the throttle the same attacker can start a
// fresh password login, which only needs the first factor, after every lockout.
// The login throttle belongs to the application, so the guard cannot do this for
// you. A lockout also means someone holds the password: reset it, then call
// [Guard.Unlock]. Emit logs listener errors instead of returning them, so a
// listener cannot change the outcome of the request.
//
// Example:
//
//	// failLogin is the application's own: it adds one failure for subject to the
//	// throttle of the login route, for as long as the lockout lasts.
//	failLogin := func(ctx context.Context, subject string, wait time.Duration) error { return nil }
//
//	unsubscribe, err := bus.Listen(auth.SecondFactorLocked, func(ctx context.Context, locked auth.SecondFactorLockedEvent) error {
//		return failLogin(ctx, locked.Subject, time.Until(locked.Until))
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var SecondFactorLocked = event.NewTopic[SecondFactorLockedEvent]("auth.second-factor.locked")
