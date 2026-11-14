package auth

import (
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ErrStepUpRequired is the 403 problem returned by [RequireLevel] and
// [RequireFresh] when an authenticated request has not proved enough, or not
// recently enough. A client should ask the user for the missing factor and
// retry. It is 403, not 401, because the user is authenticated and a 401 invites
// login redirects that would discard the session.
//
// Example:
//
//	if errors.Is(err, auth.ErrStepUpRequired) {
//		// Ask the user to confirm, then retry.
//	}
var ErrStepUpRequired = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:core:auth:step-up-required")
	details.Title("Step-up required")
	details.Detail("Additional authentication is required to access this resource.")
	details.Status(http.StatusForbidden)
})

// stepUp returns middleware that admits an identity only when satisfied accepts
// it. The callbacks run once. Anonymous requests are rejected by the same logic
// as [Require], with the same settings; an identity that fails satisfied is
// [ErrStepUpRequired]. It reads the [Identity] from the context, so it works for
// any authenticator, such as bearer tokens that carry factors.
func stepUp(satisfied func(Identity) bool, configure []MiddlewareBuilderFunc) router.Middleware {
	anonymous := require(newMiddlewareSettings(configure...))

	return func(next router.Handler) router.Handler {
		return anonymous(func(w http.ResponseWriter, r *http.Request) error {
			identity, _ := FromContext(r.Context())

			if !satisfied(identity) {
				return ErrStepUpRequired
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
	return stepUp(func(identity Identity) bool {
		return identity.Level() >= level
	}, configure)
}

// RequireFresh returns middleware that admits a request only when one of its
// identity's factors was verified within maxAge, so a long-lived session cannot
// delete an account without a recent password or second factor; [Guard.Confirm]
// records such a factor. A factor with an unknown (zero) or future time is not
// fresh. A maxAge of zero or less admits nothing. Freshness is only as honest as
// the factor times: a login method must stamp [Factor.At] with when the user
// really proved the factor. A federated login that a provider approves silently
// proves no recent credential, so pass the provider's authentication time (the
// OIDC auth_time claim) as [Factor.At] instead of leaving it zero (now). Anonymous requests and
// failures are those of [RequireLevel].
//
// Example:
//
//	r.With(auth.RequireFresh(10 * time.Minute)).Delete("/account", destroy)
func RequireFresh(maxAge time.Duration, configure ...MiddlewareBuilderFunc) router.Middleware {
	return stepUp(func(identity Identity) bool {
		now := time.Now()

		return maxAge > 0 && slices.ContainsFunc(identity.Factors(), func(factor Factor) bool {
			return !factor.At.IsZero() && !factor.At.After(now) && now.Sub(factor.At) <= maxAge
		})
	}, configure)
}
