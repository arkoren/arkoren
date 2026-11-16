package password

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/internal/storage"
)

// Message is what [Builder.Deliver] receives to write a reset email. The token
// is a credential until it is used or expires; never log it.
//
// Example:
//
//	link := message.Link("https://app.example.com/reset-password")
type Message struct {
	// Identifier is the address to write to, as returned by [Users.Lookup]: the
	// one stored with the user, never the text of the request.
	Identifier string

	// Token is the single-use secret that [Verifier.Reset] accepts.
	Token string

	// ExpiresAt is when the token stops working.
	ExpiresAt time.Time

	// Registration is true when the reset was not asked for by the owner but
	// started by a registration with an identifier that already belongs to a
	// user ([Verifier.RegisterHandler]): the email should say that someone tried
	// to register with this address, and that the link sets a new password if it
	// was not them. It is false for [Verifier.SendReset].
	Registration bool
}

// Link returns base with the token in the URL fragment, as "#token=...",
// escaped, so the token is not sent to the server, its logs or proxies. The
// reset page reads it from location.hash and posts it; [Message.QueryLink] is
// the form for a server-rendered page. base must not already carry a fragment.
//
// Example:
//
//	message := password.Message{Token: "abc"}
//	fmt.Println(message.Link("https://app.example.com/reset")) // https://app.example.com/reset#token=abc
func (message Message) Link(base string) string {
	return base + "#" + url.Values{"token": {message.Token}}.Encode()
}

// QueryLink returns base with the token as its "token" query parameter,
// escaped, for a page that a server renders. The token then reaches the server,
// its logs and proxies: prefer [Message.Link]. base must not already carry a
// query.
//
// Example:
//
//	message := password.Message{Token: "abc"}
//	fmt.Println(message.QueryLink("https://app.example.com/reset")) // https://app.example.com/reset?token=abc
func (message Message) QueryLink(base string) string {
	return base + "?" + url.Values{"token": {message.Token}}.Encode()
}

// SendReset starts a password reset for identifier: it creates a single-use
// token and hands it to [Builder.Deliver] with the address that [Users.Lookup]
// returned for the user, never the text of identifier. A user keeps up to three
// live tokens: asking again does not invalidate a link already sent, and the
// oldest is dropped only beyond three. An unknown or disabled user gets nothing
// and the call returns nil, so the caller cannot learn which identifiers
// exist. It returns [ErrNoDelivery] without a delivery function, and an error
// when [Users.Lookup] returns no address. It does not throttle:
// [Verifier.ForgotHandler] does, and runs this in the background.
//
// Example:
//
//	if err := verifier.SendReset(ctx, "ada@example.com"); err != nil {
//		return err
//	}
func (verifier *Verifier) SendReset(ctx context.Context, identifier string) error {
	return verifier.sendReset(ctx, identifier, false)
}

// sendReset is [Verifier.SendReset]; registration marks the message as the
// notice of a registration attempt on a taken identifier.
func (verifier *Verifier) sendReset(ctx context.Context, identifier string, registration bool) error {
	if verifier.deliver == nil {
		return ErrNoDelivery
	}
	token := rand.Text()
	subject, address, err := verifier.resetTarget(ctx, identifier, token)
	if errors.Is(err, errNoTarget) {
		return nil
	}
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(verifier.resetTTL)
	if err := verifier.storeReset(ctx, subject, address, token, expiresAt); err != nil {
		return fmt.Errorf("password: store reset: %w", err)
	}

	return verifier.deliver(ctx, Message{Identifier: address, Token: token, ExpiresAt: expiresAt, Registration: registration})
}

// errNoTarget is returned by [Verifier.resetTarget] for an unknown or disabled
// user, who gets no reset link.
var errNoTarget = errors.New("password: no reset target")

// resetTarget returns the subject of identifier and the address stored with
// the user, or [errNoTarget] when the user is unknown or disabled.
func (verifier *Verifier) resetTarget(ctx context.Context, identifier, token string) (subject, address string, err error) {
	subject, address, err = verifier.users.Lookup(ctx, identifier)
	if errors.Is(err, auth.ErrUserNotFound) {
		// Spend the hashing time of the known path so timing stays level.
		_ = storage.Hash(token)

		return "", "", errNoTarget
	}
	if err != nil {
		return "", "", err
	}
	if address == "" {
		return "", "", errors.New("password: Users.Lookup returned no address")
	}
	if _, err := verifier.provider.Find(ctx, subject); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
			return "", "", errNoTarget
		}

		return "", "", err
	}

	return subject, address, nil
}

// storeReset stores the hash of token for subject in one transaction, after
// making room for it with [Verifier.trimResets].
func (verifier *Verifier) storeReset(ctx context.Context, subject, address, token string, expiresAt time.Time) error {
	return verifier.db.WithTransaction(ctx, func(ctx context.Context) error {
		if err := verifier.trimResets(ctx, subject); err != nil {
			return err
		}
		_, err := verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
			statement.Insert(map[string]any{
				"subject":    subject,
				"token_hash": storage.Hash(token),
				"expires_at": expiresAt.Unix(),
				"created_at": time.Now().UnixNano(),
				"address":    address,
			})
		}))

		return err
	})
}

// trimResets deletes the expired tokens of subject and the oldest live ones
// beyond maxLiveResets-1, so a new token makes at most maxLiveResets. It runs
// in the caller's transaction.
func (verifier *Verifier) trimResets(ctx context.Context, subject string) error {
	// Lock the user's existing tokens first (a no-op update) so concurrent
	// requests count and trim one after the other and the limit holds. With
	// no token yet there is nothing to lock; then the throttle, not this
	// lock, bounds a burst, and the next request trims the surplus.
	_, err := verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
		statement.Where("subject", subject).Update(map[string]any{"subject": subject})
	}))
	if err != nil {
		return err
	}
	// Expired tokens are dead weight.
	_, err = verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
		statement.Where("subject", subject).Where("expires_at", "<=", time.Now().Unix()).Delete()
	}))
	if err != nil {
		return err
	}
	live, err := verifier.db.Select[string](ctx, query.Table(verifier.resets, func(statement *query.Builder) {
		statement.Select("token_hash").Where("subject", subject).OrderBy("created_at")
	}))
	if err != nil {
		return err
	}
	excess := len(live) - (maxLiveResets - 1)
	if excess <= 0 {
		return nil
	}
	_, err = verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
		statement.WhereIn("token_hash", live[:excess]...).Delete()
	}))

	return err
}

// Reset consumes a reset token (and the user's other live ones), sets the
// password, calls [Users.ProveEmail] when set and then [Users.Rotate], in one
// transaction, and returns the subject. The link only reached someone who
// controls the mailbox, so the reset is the proof of ownership an unverified
// registration lacks. Rotation ends every session,
// pending login and API token of the user; call [auth.Guard.RevokeAll] to
// remove their login records as well, as [Verifier.ResetHandler] does. Reset
// never logs the user in, so it cannot bypass a second factor. An unknown,
// expired, used or replaced token is [ErrInvalidToken]; a password that fails
// the policy is [ErrInvalidPassword] and leaves the token usable.
//
// Example:
//
//	subject, err := verifier.Reset(ctx, token, "a new long password")
//	if errors.Is(err, password.ErrInvalidToken) {
//		// ask for a new link
//	}
func (verifier *Verifier) Reset(ctx context.Context, token, password string) (string, error) {
	if err := verifier.policy(ctx, password); err != nil {
		return "", err
	}
	var pending resetRow
	err := verifier.db.WithTransaction(ctx, func(ctx context.Context) error {
		var err error
		if pending, err = verifier.consumeReset(ctx, storage.Hash(token)); err != nil {
			return err
		}

		return verifier.applyReset(ctx, pending.Subject, password)
	})
	if errors.Is(err, ErrInvalidToken) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", fmt.Errorf("password: reset: %w", err)
	}
	// The owner proved the mailbox, so failed logins counted against the
	// identifier (by whoever was guessing) no longer lock them out. Only the
	// cap across addresses can be cleared: the per-address counters are the
	// guesser's. It matches when the user typed the address at login.
	verifier.clear(ctx, verifier.identifierKey(pending.Address))
	verifier.emit(ctx, WasReset, pending.Subject)

	return pending.Subject, nil
}

// resetRow is the part of a reset token row that a reset reads.
type resetRow struct {
	Subject string `db:"subject"`
	Address string `db:"address"`
}

// consumeReset deletes the live token whose hash is hashed, and the user's
// other tokens with it, and returns its row. An unknown, expired or already
// used token is [ErrInvalidToken]. It runs in the caller's transaction.
func (verifier *Verifier) consumeReset(ctx context.Context, hashed string) (resetRow, error) {
	pending, err := verifier.db.Find[resetRow](ctx, query.Table(verifier.resets, func(statement *query.Builder) {
		statement.Select("subject", "address").Where("token_hash", hashed).Where("expires_at", ">", time.Now().Unix()).Limit(1)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return resetRow{}, ErrInvalidToken
	}
	if err != nil {
		return resetRow{}, err
	}
	// The delete is the single-use gate: of two concurrent requests with the
	// same token only one removes the row.
	affected, err := verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
		statement.Where("token_hash", hashed).Delete()
	}))
	if err != nil {
		return resetRow{}, err
	}
	if affected == 0 {
		return resetRow{}, ErrInvalidToken
	}
	// The user's other live tokens die with this one: the password they
	// were meant to reset no longer exists.
	if err := verifier.deleteResets(ctx, pending.Subject); err != nil {
		return resetRow{}, err
	}

	return pending, nil
}

// applyReset sets the password of subject, calls [Users.ProveEmail] when set
// and then [Users.Rotate]. It runs in the caller's transaction.
func (verifier *Verifier) applyReset(ctx context.Context, subject, password string) error {
	if err := verifier.save(ctx, subject, password); err != nil {
		return err
	}
	if verifier.users.ProveEmail != nil {
		if err := verifier.users.ProveEmail(ctx, subject); err != nil {
			return err
		}
	}

	return verifier.users.Rotate(ctx, subject)
}

// deleteResets deletes every reset token of subject.
func (verifier *Verifier) deleteResets(ctx context.Context, subject string) error {
	_, err := verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))

	return err
}
