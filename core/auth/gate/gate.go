package gate

import (
	"context"
	"net/http"
	"reflect"
	"slices"

	"arkoren.dev/core/auth"
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

// user returns the U that rules receive from ctx. When U is [auth.Identity]
// it is the identity itself; otherwise it is the application user the
// authenticator attached to the identity's record (see [auth.UserFromContext]).
// The boolean is false without an identity, without a record value, or when the
// value has another type, so a mismatch fails closed.
func user[U any](ctx context.Context) (U, bool) {
	if reflect.TypeFor[U]() == reflect.TypeFor[auth.Identity]() {
		identity, ok := auth.FromContext(ctx)

		return any(identity).(U), ok
	}

	return auth.UserFromContext[U](ctx)
}

// check runs rules in order against the user of type U in ctx, see user. It
// returns [auth.ErrUnauthenticated] without a user, [ErrForbidden] with the
// index of the first rule that returned false, and the error of the first rule
// that failed. The index is -1 for every other outcome.
func check[U any](ctx context.Context, rules []func(context.Context, U) (bool, error)) (int, error) {
	current, ok := user[U](ctx)

	if !ok {
		return -1, auth.ErrUnauthenticated
	}

	for index, rule := range rules {
		allowed, err := rule(ctx, current)

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

// Bind adapts rules for U to a context-only availability check, the shape taken
// by features such as tool or action availability that have no user parameter.
// The returned function resolves U as [Require] does and runs the rules in
// order. It returns (false, nil) when there is no user, which hides the feature
// from anonymous callers without an error, (false, nil) at the first rule that
// denies, and (false, err) at the first rule that fails. Like [Require], it
// panics when no rule is given and copies the rule slice; functions and their
// captures remain shared.
//
// Example:
//
//	tool := mcp.NewTool("report", report, func(action *mcp.ToolBuilder[In, Out]) {
//		action.Available(gate.Bind(isAnalyst))
//	})
func Bind[U any](rules ...func(context.Context, U) (bool, error)) func(context.Context) (bool, error) {
	if len(rules) == 0 {
		panic("gate: Bind needs at least one rule")
	}

	rules = slices.Clone(rules)

	return func(ctx context.Context) (bool, error) {
		current, ok := user[U](ctx)

		if !ok {
			return false, nil
		}

		for _, rule := range rules {
			allowed, err := rule(ctx, current)

			if err != nil || !allowed {
				return false, err
			}
		}

		return true, nil
	}
}

// HasScopes returns a rule over [auth.Identity] that allows an identity
// holding every required scope, read through [auth.Identity.Scopes]. It copies
// required before capturing it, so later caller edits cannot change
// authorization. Empty requirements allow any identity. Use the rule with
// [Require], [Authorize] or [Bind]; it does not authenticate a request.
//
// Example:
//
//	r.With(gate.Require(gate.HasScopes("posts:write"))).Post("/posts", create)
func HasScopes(required ...string) func(context.Context, auth.Identity) (bool, error) {
	required = slices.Clone(required)

	return func(_ context.Context, identity auth.Identity) (bool, error) {
		granted := identity.Scopes()

		return !slices.ContainsFunc(required, func(scope string) bool {
			return !slices.Contains(granted, scope)
		}), nil
	}
}
