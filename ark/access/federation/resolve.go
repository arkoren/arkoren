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
// problems ([ErrNotLinked], [ErrLinkedElsewhere], the error of [Builder.Create])
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

// createUser makes a user for an unknown identity and connects it, in one
// transaction. When a concurrent first login of the same identity wins the
// insert, this one rolls its user back and returns the winner's subject.
func (links *Links) createUser(ctx context.Context, external auth.External) (string, error) {
	if links.create == nil {
		return "", ErrNotLinked
	}
	var subject string
	err := links.db.WithTransaction(ctx, func(ctx context.Context) error {
		created, err := links.create(ctx, external)
		if err != nil {
			return err
		}
		written, err := links.insert(ctx, created, external)
		if err != nil {
			return err
		}
		if !written {
			return errLostRace
		}
		subject = created

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
