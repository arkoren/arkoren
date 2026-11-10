package auth

import (
	"errors"
	"net/http"
	"reflect"
	"slices"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
)

// requireState returns middleware that admits a request only when its user is
// the session's logged-in user and satisfied accepts the session's State.
// Everything it cannot establish is ErrStepUpRequired, so bearer-authenticated
// requests, sessions without state and mismatched users fail closed; only a
// request with no user at all is ErrUnauthenticated.
func (identity SessionIdentity[K, U]) requireState(satisfied func(State) bool) router.Middleware {
	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			user, ok := FromContext[U](r.Context())

			if !ok {
				return ErrUnauthenticated
			}

			current, err := identity.Session(r.Context())

			if errors.Is(err, session.ErrNoSession) {
				return ErrStepUpRequired
			}

			if err != nil {
				return err
			}

			// The middleware may have authenticated this request by another
			// means, such as a bearer token for a different user; only the
			// session's own user can be judged by the session's factors.
			id, err := current.Get(field[K](identity.realm, userKey))

			if errors.Is(err, session.ErrKeyNotFound) || errors.Is(err, session.ErrInvalidValueType) {
				return ErrStepUpRequired
			}

			if err != nil {
				return err
			}

			if !reflect.DeepEqual(id, user.AuthID()) {
				return ErrStepUpRequired
			}

			state, err := identity.State(current)

			if errors.Is(err, session.ErrInvalidValueType) {
				return ErrStepUpRequired
			}

			if err != nil {
				return err
			}

			if !satisfied(state) {
				return ErrStepUpRequired
			}

			return next(w, r)
		}
	}
}

// RequireMethods returns middleware that admits a request only when the
// session's user has proved every listed method (see [SessionIdentity.Login],
// [SessionIdentity.Complete] and [SessionIdentity.Confirm]), at any time.
// Otherwise it returns [ErrStepUpRequired]. Requests authenticated without this
// identity's session (a bearer token, for example), requests whose session
// holds no state or a different user, and requests with a session but no
// matching factors all fail closed with [ErrStepUpRequired]; a request with no
// user at all returns [ErrUnauthenticated]. Install it after [Middleware].
// With no methods it admits any session that has a matching user. Like
// [Middleware], it trusts the context user, so [Require] belongs before it only
// when anonymous requests need the Bearer challenge.
//
// Example:
//
//	r.With(identity.RequireMethods(auth.MethodOTP)).Post("/payouts", payout)
func (identity SessionIdentity[K, U]) RequireMethods(methods ...Method) router.Middleware {
	methods = slices.Clone(methods)

	return identity.requireState(func(state State) bool {
		return !slices.ContainsFunc(methods, func(method Method) bool {
			return !slices.ContainsFunc(state.Factors, func(factor Factor) bool { return factor.Method == method })
		})
	})
}

// RequireFresh returns middleware that admits a request only when some factor
// of the session was proved within maxAge, using the most recent factor time.
// [SessionIdentity.Confirm] refreshes a factor and so satisfies it. A maxAge of
// zero or less admits nothing. Failures are those of [SessionIdentity.RequireMethods].
//
// Example:
//
//	r.With(identity.RequireFresh(10 * time.Minute)).Delete("/account", destroy)
func (identity SessionIdentity[K, U]) RequireFresh(maxAge time.Duration) router.Middleware {
	return identity.requireState(func(state State) bool {
		now := time.Now()

		return maxAge > 0 && slices.ContainsFunc(state.Factors, func(factor Factor) bool {
			return now.Sub(factor.At) <= maxAge
		})
	})
}
