package web

import (
	"context"
	"errors"
)

// The functions below are how a builder setter adds a hook without dropping the
// ones set before it. The facade sets its own security hooks (revoking tokens
// after a password change, say) and the application's callbacks add theirs
// afterwards; a replacing setter would let an application callback silently
// remove the facade's. Each returns the combination of previous and next,
// treating a nil side as absent, so a setter is
// `builder.hook = web.Then(builder.hook, hook)`.

// Then returns a hook that runs previous and then next, and runs both even when
// the first fails, so a failing application hook cannot skip the facade's
// revocation. The errors are joined. A nil side is skipped.
func Then[A any](previous, next func(context.Context, A) error) func(context.Context, A) error {
	if previous == nil {
		return next
	}
	if next == nil {
		return previous
	}

	return func(ctx context.Context, arg A) error {
		return errors.Join(previous(ctx, arg), next(ctx, arg))
	}
}

// Both is [Then] for the two-string hook of a registration: the subject and the
// identifier.
func Both(previous, next func(context.Context, string, string) error) func(context.Context, string, string) error {
	if previous == nil {
		return next
	}
	if next == nil {
		return previous
	}

	return func(ctx context.Context, subject, identifier string) error {
		return errors.Join(previous(ctx, subject, identifier), next(ctx, subject, identifier))
	}
}

// All combines two checks into one that is true only when both are: the first
// false, or the first error, ends it. It is how two Verified settings compose,
// because a user the facade's check refuses must stay refused whatever the
// application's check says. A nil side is skipped.
func All(previous, next func(context.Context, string) (bool, error)) func(context.Context, string) (bool, error) {
	if previous == nil {
		return next
	}
	if next == nil {
		return previous
	}

	return func(ctx context.Context, subject string) (bool, error) {
		if ok, err := previous(ctx, subject); err != nil || !ok {
			return false, err
		}

		return next(ctx, subject)
	}
}

// Any combines two checks into one that is true when either is: the first true,
// or the first error, ends it. It is how two Fallback settings compose, because
// each names one way for a user to log in without their connections. A nil side
// is skipped.
func Any(previous, next func(context.Context, string) (bool, error)) func(context.Context, string) (bool, error) {
	if previous == nil {
		return next
	}
	if next == nil {
		return previous
	}

	return func(ctx context.Context, subject string) (bool, error) {
		if ok, err := previous(ctx, subject); err != nil || ok {
			return ok, err
		}

		return next(ctx, subject)
	}
}

// First combines two subject-returning functions into one that returns the
// first nonempty subject: previous runs first, and next only when previous
// returns "" and no error. It is how two TakeOver or Create settings compose: a
// function that has nothing to give declines with "". A nil side is skipped.
func First[A any](previous, next func(context.Context, A) (string, error)) func(context.Context, A) (string, error) {
	if previous == nil {
		return next
	}
	if next == nil {
		return previous
	}

	return func(ctx context.Context, arg A) (string, error) {
		if subject, err := previous(ctx, arg); err != nil || subject != "" {
			return subject, err
		}

		return next(ctx, arg)
	}
}
