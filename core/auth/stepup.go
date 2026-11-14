package auth

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ErrStepUpRequired is the 403 problem returned by [RequireLevel],
// [Guard.RequireFresh] and [Guard.Fresh] when an authenticated request has not
// proved enough, or not recently enough. A client should ask the user for one of
// the methods and retry. [Guard.Fresh] adds a "methods" extension listing the
// methods the user is enrolled in, in registration order, which the client
// answers through [Guard.ConfirmHandler]; [RequireLevel] adds none. It is 403,
// not 401, because the user is authenticated and a 401 invites login redirects
// that would discard the session. [errors.Is] matches it whatever the extension.
//
// Example:
//
//	if errors.Is(err, auth.ErrStepUpRequired) {
//		// Ask the user to confirm with one of the listed methods, then retry.
//	}
var ErrStepUpRequired = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:core:auth:step-up-required")
	details.Title("Step-up required")
	details.Detail("Additional authentication is required to access this resource.")
	details.Status(http.StatusForbidden)
})

// ErrNoStepUpMethod is the 403 problem returned by [Guard.Fresh],
// [Guard.RequireFresh] and [Guard.RecentLogin] when the session is too old to be
// trusted and the user has no method with which to prove themselves again. The
// client's only way forward is to log in again.
//
// Example:
//
//	if errors.Is(err, auth.ErrNoStepUpMethod) {
//		// Send the user to the login page.
//	}
var ErrNoStepUpMethod = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:core:auth:no-step-up-method")
	details.Title("No step-up method")
	details.Detail("This session is too old and has no method to confirm it again. Log in again.")
	details.Status(http.StatusForbidden)
})

// guarded returns middleware that admits an identity only when check accepts it.
// The callbacks run once. Anonymous requests are rejected by the same logic as
// [Require], with the same settings; any other outcome is check's error. It reads
// the [Identity] from the context, so it works for any authenticator, such as
// bearer tokens that carry factors.
func guarded(check func(r *http.Request, identity Identity) error, configure []MiddlewareBuilderFunc) router.Middleware {
	anonymous := require(newMiddlewareSettings(configure...))

	return func(next router.Handler) router.Handler {
		return anonymous(func(w http.ResponseWriter, r *http.Request) error {
			identity, _ := FromContext(r.Context())

			if err := check(r, identity); err != nil {
				return err
			}

			return next(w, r)
		})
	}
}

// RequireLevel returns middleware that admits a request only when its
// [Identity] has at least level (see [Identity.Level]). A request without an
// identity is rejected like [Require] does, with the same configuration
// ([MiddlewareBuilder.ErrorResponse] and [MiddlewareBuilder.Events] apply to
// that case only, and the callbacks run once); an
// identity that is too weak gets [ErrStepUpRequired], for example a
// single-factor session on a route that needs [LevelMulti]. Install it after
// [Middleware]. It reads the identity from the context, so it also works for
// authenticators other than [Guard.Authenticator], such as tokens that carry
// factors.
//
// Example:
//
//	r.With(auth.RequireLevel(auth.LevelMulti)).Post("/payouts", payout)
func RequireLevel(level Level, configure ...MiddlewareBuilderFunc) router.Middleware {
	return guarded(func(_ *http.Request, identity Identity) error {
		if identity.Level() < level {
			return ErrStepUpRequired
		}

		return nil
	}, configure)
}

// RequireSession returns middleware that admits a request only when its identity
// comes from a browser session ([Identity.Session]), which only
// [Guard.Authenticator] sets. Mount every route that enrolls, changes or confirms
// a credential behind it: a bearer token, however it was issued, is rejected
// with [ErrUnauthenticated] even though it is authenticated. A request without an
// identity is rejected like [Require] does, with the same configuration. Install
// it after [Middleware].
//
// Example:
//
//	r.With(auth.RequireSession()).Post("/account/password", changePassword)
func RequireSession(configure ...MiddlewareBuilderFunc) router.Middleware {
	return guarded(func(_ *http.Request, identity Identity) error {
		if !identity.Session() {
			return ErrUnauthenticated
		}

		return nil
	}, configure)
}

// Fresh reports whether the request's identity proved itself recently enough, with
// the window of [GuardBuilder.FreshFor] (15 minutes by default). It is the check
// behind [Guard.RequireFresh], for handlers that decide inside the route.
//
// The rule: fresh means a factor of one of the user's enrolled
// [GuardBuilder.StepUp] methods that this server verified within the window. A
// user enrolled in no step-up method cannot prove anything else, so any factor
// within the window counts, which makes a federated-only user fresh right after
// logging in. A silent re-login through a provider adds a factor that is not a
// step-up method, so it never refreshes a user who has a password or an
// authenticator. A factor with an unknown (zero) or future time is not fresh.
//
// It returns nil when fresh; [ErrStepUpRequired] with the user's enrolled step-up
// methods in its "methods" extension when the user can step up; and
// [ErrNoStepUpMethod] when they cannot and must log in again. A request without an
// identity is [ErrUnauthenticated]. An error from a credential's Enrolled passes
// through, so a failing lookup never relaxes the rule.
//
// Example:
//
//	if err := guard.Fresh(r); err != nil {
//		return err
//	}
func (guard *Guard) Fresh(r *http.Request) error {
	return guard.fresh(r, guard.settings.Fresh)
}

// RequireFresh returns middleware that admits a request only when [Guard.Fresh]
// passes with the window maxAge instead of the guard's [GuardBuilder.FreshFor], so
// a long-lived session cannot delete an account without a recent password or
// second factor. A maxAge of zero or less admits nothing. Failures are those of
// [Guard.Fresh]. A request without an identity is rejected like [Require] does,
// with the same configuration. Install it after [Middleware].
//
// Example:
//
//	r.With(guard.RequireFresh(10 * time.Minute)).Delete("/account", destroy)
func (guard *Guard) RequireFresh(maxAge time.Duration, configure ...MiddlewareBuilderFunc) router.Middleware {
	return guarded(func(r *http.Request, _ Identity) error {
		return guard.fresh(r, maxAge)
	}, configure)
}

// fresh implements [Guard.Fresh] for a window of maxAge.
func (guard *Guard) fresh(r *http.Request, maxAge time.Duration) error {
	identity, ok := FromContext(r.Context())

	if !ok {
		return ErrUnauthenticated
	}

	methods, err := enrolled(r.Context(), identity.Subject(), guard.settings.StepUps)

	if err != nil {
		return err
	}

	now := time.Now()

	recent := func(factor Factor) bool {
		return maxAge > 0 && !factor.At.IsZero() && !factor.At.After(now) && now.Sub(factor.At) <= maxAge &&
			(len(methods) == 0 || slices.Contains(methods, factor.Method))
	}

	switch {
	case slices.ContainsFunc(identity.Factors(), recent):
		return nil
	case len(methods) == 0:
		return ErrNoStepUpMethod
	default:
		return ErrStepUpRequired.Derive(func(details *problem.Builder) {
			details.Extension("methods", methods)
		})
	}
}

// RecentLogin reports whether the request's session was created, by a login and
// not by a later [Guard.Begin] of the same user, within the last within. It is the
// check for actions that need a new login rather than freshness, such as
// exchanging a session for a long-lived device token: a silent re-login by the
// same subject keeps the login record and its creation time, so it cannot extend
// the window, and neither can [Guard.ConfirmHandler]. It returns nil when the
// login is recent; [ErrNoStepUpMethod] when it is older, or within is zero or
// less, because only a new login helps; and [ErrUnauthenticated] when the session
// holds no logged-in user, its user was deleted or disabled, its credential
// version no longer matches [UserRecord.Version] or its own login was revoked. It
// returns [session.ErrNoSession] without a session and passes store errors
// through.
//
// Example:
//
//	if err := guard.RecentLogin(r, 5*time.Minute); err != nil {
//		return err
//	}
func (guard *Guard) RecentLogin(r *http.Request, within time.Duration) error {
	stored, _, err := guard.valid(r)

	if errors.Is(err, ErrNoCredentials) {
		return ErrUnauthenticated
	}

	if err != nil {
		return err
	}

	records, err := guard.LoginsOf(r.Context(), stored.Subject)

	if err != nil {
		return err
	}

	index := slices.IndexFunc(records, func(record LoginRecord) bool { return record.ID == stored.LoginID })

	if index < 0 {
		return ErrUnauthenticated
	}

	if within <= 0 || time.Since(records[index].CreatedAt) > within {
		return ErrNoStepUpMethod
	}

	return nil
}
