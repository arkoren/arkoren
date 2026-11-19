package federation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/foundation/query"
)

// errLostRace aborts a creating transaction whose link insert lost to a
// concurrent first login of the same external identity.
var errLostRace = errors.New("federation: lost link race")

// onboarding is what a login does beyond the connection itself: with an
// identity that no connection and no takeover claims, create a user with create
// when signUp is set and refuse with [ErrNoAccount] otherwise, and with any
// identity store the claims that keep names. [Links] holds the one from its
// [Builder], which keeps nothing; [Links.Resolver] makes one per provider.
type onboarding struct {
	signUp bool
	create func(context.Context, auth.External) (string, error)

	// keep names the claims of the identity that are stored on its connection,
	// see [ResolverBuilder.KeepClaims]. Empty keeps nothing.
	keep []string
}

// Resolve is the [auth.Resolver] of the package: it maps a verified external
// identity to the local subject to log in, as the protocol module's callback
// expects, with the sign-up settings of the [Builder]. [Links.Resolver] makes
// resolvers with their own sign-up settings over the same connections. See
// [Links] for the rules. It reads the logged-in identity and the
// session from ctx, which protocol modules take from the request, so the
// callback route needs the session and auth middleware. Errors that are
// problems ([ErrNoAccount], [ErrLinkedElsewhere], the error of [Builder.Create])
// reach the client as problem responses.
//
// Example:
//
//	web.Get("/auth/google/callback", google.CallbackHandler(guard, links.Resolve))
func (links *Links) Resolve(ctx context.Context, external auth.External) (string, error) {
	return links.resolve(ctx, external, links.onboard)
}

// resolve is [Links.Resolve] with the sign-up settings to apply to an unknown
// identity.
func (links *Links) resolve(ctx context.Context, external auth.External, onboard onboarding) (string, error) {
	if external.Issuer == "" || external.Subject == "" {
		return "", fmt.Errorf("federation: empty issuer or subject: %w", auth.ErrNoCredentials)
	}
	current := links.takeIntent(ctx)
	existing, linked, err := links.find(ctx, external.Issuer, external.Subject)
	if err != nil {
		return "", err
	}
	switch {
	case linked && current != "" && existing.Subject != current:
		return "", ErrLinkedElsewhere
	case linked:
		return links.login(ctx, existing, external, onboard.keep)
	case current != "":
		if err := links.link(ctx, current, external, onboard.keep); err != nil {
			return "", err
		}

		return current, nil
	}

	return links.createUser(ctx, external, onboard)
}

// login returns the subject of a known connection after checking the user is
// still allowed in, and records the use together with the claims of external
// that keep names, merged over the stored ones.
func (links *Links) login(ctx context.Context, found row, external auth.External, keep []string) (string, error) {
	if _, err := links.provider.Find(ctx, found.Subject); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
			return "", auth.ErrUnauthenticated
		}

		return "", err
	}
	update := map[string]any{"last_used_at": time.Now().Unix()}
	if claims, changed := mergeClaims(ctx, found.Claims, keepClaims(ctx, external.Claims, keep)); changed {
		update["claims"] = claims
	}
	// The time and the claims are informational: a failed write must not fail
	// the login.
	_, _ = links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
		statement.Where("id", found.ID).Update(update)
	}))

	return found.Subject, nil
}

// createUser connects an unknown identity to a user, in one transaction: the
// unverified user [Builder.TakeOver] hands over, or else a new one from
// [Builder.Create] when [Builder.SignUp] is enabled. Without either it is
// [ErrNoAccount] and nothing is written. When a concurrent first login of the
// same identity wins the insert, this one rolls back and returns the winner's
// subject.
func (links *Links) createUser(ctx context.Context, external auth.External, onboard onboarding) (string, error) {
	if !onboard.signUp && links.takeOver == nil {
		return "", ErrNoAccount
	}
	var subject string
	var claimed bool
	err := links.db.WithTransaction(ctx, func(ctx context.Context) error {
		var err error
		subject, claimed, err = links.owner(ctx, external, onboard)
		if err != nil {
			return err
		}
		written, err := links.insert(ctx, subject, external, onboard.keep)
		if err != nil {
			return err
		}
		if !written {
			return errLostRace
		}

		return nil
	})
	if errors.Is(err, errLostRace) {
		return links.loginWinner(ctx, external, onboard.keep)
	}
	if err != nil {
		return "", err
	}
	links.emit(ctx, LinkCreated, subject)
	if claimed {
		links.emit(ctx, AccountClaimed, subject)
	}

	return subject, nil
}

// owner returns the user that gets an unknown identity, inside the transaction
// of [Links.createUser]: the user [Builder.TakeOver] hands over (claimed is then
// true), or a new user. A taken-over user must still be allowed in: a disabled
// one is [auth.ErrUnauthenticated], which rolls the takeover back, so no login
// ever re-enables or empties a disabled account. A new user needs
// [Builder.SignUp] and an identity with a proven email ([ErrEmailUnverified]
// otherwise).
func (links *Links) owner(ctx context.Context, external auth.External, onboard onboarding) (subject string, claimed bool, err error) {
	if links.takeOver != nil {
		subject, err := links.takeOver(ctx, external)
		if err != nil {
			return "", false, err
		}
		if subject != "" {
			if _, err := links.provider.Find(ctx, subject); err != nil {
				if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
					return "", false, auth.ErrUnauthenticated
				}

				return "", false, err
			}

			return subject, true, nil
		}
	}
	if !onboard.signUp {
		return "", false, ErrNoAccount
	}
	if !external.EmailVerified || external.Email == "" {
		return "", false, ErrEmailUnverified
	}
	subject, err = onboard.create(ctx, external)

	return subject, false, err
}

// loginWinner logs in the user that a concurrent first login of external
// created and connected first. Only the winner's transaction wrote a row, so
// only its row held the claims of the first login; this login merges its own.
func (links *Links) loginWinner(ctx context.Context, external auth.External, keep []string) (string, error) {
	winner, ok, err := links.find(ctx, external.Issuer, external.Subject)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotLinked
	}

	return links.login(ctx, winner, external, keep)
}
