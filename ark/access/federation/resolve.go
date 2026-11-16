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

// Resolve is the [auth.Resolver] of the package: it maps a verified external
// identity to the local subject to log in, as the protocol module's callback
// expects. See [Links] for the rules. It reads the logged-in identity and the
// session from ctx, which protocol modules take from the request, so the
// callback route needs the session and auth middleware. Errors that are
// problems ([ErrNoAccount], [ErrLinkedElsewhere], the error of [Builder.Create])
// reach the client as problem responses.
//
// Example:
//
//	web.Get("/auth/google/callback", google.CallbackHandler(guard, links.Resolve))
func (links *Links) Resolve(ctx context.Context, external auth.External) (string, error) {
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
		return links.login(ctx, existing)
	case current != "":
		if err := links.Link(ctx, current, external); err != nil {
			return "", err
		}

		return current, nil
	}

	return links.createUser(ctx, external)
}

// login returns the subject of a known connection after checking the user is
// still allowed in, and records the use.
func (links *Links) login(ctx context.Context, found row) (string, error) {
	if _, err := links.provider.Find(ctx, found.Subject); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
			return "", auth.ErrUnauthenticated
		}

		return "", err
	}
	// The time is informational: a failed write must not fail the login.
	_, _ = links.db.Exec(ctx, query.Table(links.table, func(statement *query.Builder) {
		statement.Where("id", found.ID).Update(map[string]any{"last_used_at": time.Now().Unix()})
	}))

	return found.Subject, nil
}

// createUser connects an unknown identity to a user, in one transaction: the
// unverified user [Builder.TakeOver] hands over, or else a new one from
// [Builder.Create] when [Builder.SignUp] is enabled. Without either it is
// [ErrNoAccount] and nothing is written. When a concurrent first login of the
// same identity wins the insert, this one rolls back and returns the winner's
// subject.
func (links *Links) createUser(ctx context.Context, external auth.External) (string, error) {
	if !links.signUp && links.takeOver == nil {
		return "", ErrNoAccount
	}
	var subject string
	err := links.db.WithTransaction(ctx, func(ctx context.Context) error {
		var err error
		subject, err = links.owner(ctx, external)
		if err != nil {
			return err
		}
		written, err := links.insert(ctx, subject, external)
		if err != nil {
			return err
		}
		if !written {
			return errLostRace
		}

		return nil
	})
	if errors.Is(err, errLostRace) {
		return links.loginWinner(ctx, external)
	}
	if err != nil {
		return "", err
	}
	links.emit(ctx, LinkCreated, subject)

	return subject, nil
}

// owner returns the user that gets an unknown identity, inside the transaction
// of [Links.createUser]: the user [Builder.TakeOver] hands over, or a new user.
// A taken-over user must still be allowed in: a disabled one is
// [auth.ErrUnauthenticated], which rolls the takeover back, so no login ever
// re-enables or empties a disabled account.
func (links *Links) owner(ctx context.Context, external auth.External) (string, error) {
	if links.takeOver != nil {
		subject, err := links.takeOver(ctx, external)
		if err != nil {
			return "", err
		}
		if subject != "" {
			if _, err := links.provider.Find(ctx, subject); err != nil {
				if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
					return "", auth.ErrUnauthenticated
				}

				return "", err
			}

			return subject, nil
		}
	}
	if !links.signUp {
		return "", ErrNoAccount
	}

	return links.create(ctx, external)
}

// loginWinner logs in the user that a concurrent first login of external
// created and connected first.
func (links *Links) loginWinner(ctx context.Context, external auth.External) (string, error) {
	winner, ok, err := links.find(ctx, external.Issuer, external.Subject)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotLinked
	}

	return links.login(ctx, winner)
}
