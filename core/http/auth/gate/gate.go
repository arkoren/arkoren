package gate

import (
	"context"
	"net/http"
	"slices"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ErrForbidden is the problem returned when a rule denies the user.
//
// Example:
//
//	if err := gate.Authorize(ctx, isAdmin); err != nil {
//		return err // renders as 403 for non-admins
//	}
var ErrForbidden = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/auth/gate:forbidden")
	details.Title("Forbidden")
	details.Detail("You do not have permission to perform this action.")
	details.Status(http.StatusForbidden)
})

// Gate runs rules like [Authorize] and [Require] and publishes [Denied] when
// a rule denies. Create it with [New]; it is immutable and safe for
// concurrent use.
//
// Example:
//
//	policy := gate.New(func(access *gate.Builder) {
//		access.Events(app.Events())
//	})
//
//	admin := r.With(policy.Require(isAdmin))
type Gate struct {
	// config holds the event settings.
	config settings
}

// New returns a Gate configured by callbacks run directly once in order on one
// fresh draft. Nil entries panic; omitted or nil/empty expanded slices supply no
// callbacks. The published settings are independent of retained drafts. New
// does no I/O and cannot fail through a returned error.
//
// Example:
//
//	policy := gate.New(func(access *gate.Builder) {
//		access.Events(app.Events())
//	})
//	admin := router.New()
//	admin.Use(policy.Require(isAdmin))
func New(configure ...BuilderFunc) *Gate {
	return &Gate{
		config: newSettings(configure...),
	}
}

// check runs rules in order against the user of type U in ctx. It returns
// [auth.ErrUnauthenticated] without a user, [ErrForbidden] with the index of
// the first rule that returned false, and the error of the first rule that
// failed. The index is -1 for every other outcome.
func check[U any](ctx context.Context, rules []func(context.Context, U) (bool, error)) (int, error) {
	user, ok := auth.FromContext[U](ctx)

	if !ok {
		return -1, auth.ErrUnauthenticated
	}

	for index, rule := range rules {
		allowed, err := rule(ctx, user)

		if err != nil {
			return -1, err
		}

		if !allowed {
			return index, ErrForbidden
		}
	}

	return -1, nil
}

// Authorize works like the function [Authorize] and also publishes [Denied]
// when a rule returns false. The event has no method because there is no
// request; use [Gate.Require] when listeners need HTTP method metadata. Neither
// form publishes resource paths, identities, or rule error causes.
//
// Example:
//
//	if err := policy.Authorize(r.Context(), canEdit); err != nil {
//		return err
//	}
func (gate *Gate) Authorize[U any](ctx context.Context, rules ...func(context.Context, U) (bool, error)) error {
	denied, err := check(ctx, rules)

	if denied >= 0 {
		gate.config.Events.Emit(ctx, Denied, DeniedEvent{
			Rule: denied,
		})
	}

	return err
}

// Require works like the function [Require] and also publishes [Denied], with
// the method of the request, when a rule returns false. Resource paths can carry
// credentials and are omitted. It panics when no rule is given. Like [Require],
// it copies the rule slice before
// returning; functions and their captured state remain shared.
//
// Example:
//
//	admin := r.With(policy.Require(isAdmin))
func (gate *Gate) Require[U any](rules ...func(context.Context, U) (bool, error)) router.Middleware {
	if len(rules) == 0 {
		panic("gate: Require needs at least one rule; use auth.Require for sign-in only")
	}

	rules = slices.Clone(rules)

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			denied, err := check(r.Context(), rules)

			if denied >= 0 {
				gate.config.Events.Emit(r.Context(), Denied, DeniedEvent{
					Method: r.Method,
					Rule:   denied,
				})
			}

			if err != nil {
				return err
			}

			return next(w, r)
		}
	}
}

// Authorize checks the rules against the user of type U in ctx. It returns
// [auth.ErrUnauthenticated] when there is no user, [ErrForbidden] at the
// first rule that returns false, and the error of the first rule that fails.
// Rules run in order and later ones are skipped after a denial.
//
// Example:
//
//	func publish(w http.ResponseWriter, r *http.Request) error {
//		if err := gate.Authorize(r.Context(), isEditor); err != nil {
//			return err
//		}
//
//		return nil
//	}
func Authorize[U any](ctx context.Context, rules ...func(context.Context, U) (bool, error)) error {
	_, err := check(ctx, rules)

	return err
}

// Require returns a middleware that runs [Authorize] with rules before the
// handler. It panics when no rule is given; use [auth.Require] to only
// require sign-in.
//
// Require copies the rule slice before returning, so replacing caller-owned
// entries cannot change the policy. Functions and their captured state remain
// shared.
//
// Example:
//
//	admin := r.With(gate.Require(isAdmin))
func Require[U any](rules ...func(context.Context, U) (bool, error)) router.Middleware {
	if len(rules) == 0 {
		panic("gate: Require needs at least one rule; use auth.Require for sign-in only")
	}

	rules = slices.Clone(rules)

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			if err := Authorize(r.Context(), rules...); err != nil {
				return err
			}

			return next(w, r)
		}
	}
}
