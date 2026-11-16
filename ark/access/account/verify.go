package account

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/ark/access/internal/throttle"
	"arkoren.dev/ark/access/internal/web"
)

const (
	// maxResends is how many resend requests one user may make within
	// resendWindow.
	maxResends = 5

	// maxClientResends is how many resend requests one client address may make
	// within resendWindow, across users.
	maxClientResends = 20

	// resendWindow is the lifetime of a resend counter.
	resendWindow = 15 * time.Minute
)

// Message is what [Builder.Deliver] receives: the address to write to and the
// secret to put in the link. The token is a credential until it is used or
// expires; never log it.
//
// Example:
//
//	link := message.Link("https://app.example.com/verify-email")
type Message struct {
	// Email is the address to send the verification to.
	Email string

	// Token is the single-use secret that [Directory.Verify] accepts.
	Token string

	// ExpiresAt is when the token stops working.
	ExpiresAt time.Time
}

// Link returns base with the token in the URL fragment, as "#token=...",
// escaped, so the token is not sent to the server, its logs or proxies. The
// verify page reads it from location.hash and posts it; [Message.QueryLink] is
// the form for a server-rendered page. base must not already carry a fragment.
//
// Example:
//
//	message := account.Message{Token: "abc"}
//	fmt.Println(message.Link("https://app.example.com/verify")) // https://app.example.com/verify#token=abc
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
//	message := account.Message{Token: "abc"}
//	fmt.Println(message.QueryLink("https://app.example.com/verify")) // https://app.example.com/verify?token=abc
func (message Message) QueryLink(base string) string {
	return base + "?" + url.Values{"token": {message.Token}}.Encode()
}

// SendVerification creates a verification token for subject, replacing any
// earlier one, and hands it to [Builder.Deliver]. A user who already verified
// their email gets nothing. It returns [ErrNoDelivery] without a delivery
// function and an error wrapping [auth.ErrUserNotFound] for an unknown subject.
//
// Example:
//
//	if err := directory.SendVerification(ctx, subject); err != nil {
//		return err
//	}
func (directory *Directory) SendVerification(ctx context.Context, subject string) error {
	if directory.deliver == nil {
		return ErrNoDelivery
	}
	row, err := directory.row(ctx, "id", subject)
	if err != nil {
		return err
	}
	if row.VerifiedAt != 0 {
		return nil
	}
	token := rand.Text()
	expiresAt := time.Now().Add(directory.verifyTTL)
	err = directory.db.WithTransaction(ctx, func(ctx context.Context) error {
		_, err := directory.db.Exec(ctx, query.Table(directory.verifs, func(statement *query.Builder) {
			statement.Where("subject", subject).Delete()
		}))
		if err != nil {
			return err
		}
		_, err = directory.db.Exec(ctx, query.Table(directory.verifs, func(statement *query.Builder) {
			statement.Insert(map[string]any{
				"subject":    subject,
				"token_hash": storage.Hash(token),
				"expires_at": expiresAt.Unix(),
			})
		}))

		return err
	})
	if err != nil {
		return fmt.Errorf("account: store verification: %w", err)
	}

	return directory.deliver(ctx, Message{Email: row.Email, Token: token, ExpiresAt: expiresAt})
}

// Verify consumes a verification token and marks the user's email verified. It
// returns the subject. A token that is unknown, expired, used or replaced
// returns [ErrInvalidToken]. It publishes [EmailVerified] after the commit.
//
// Example:
//
//	subject, err := directory.Verify(ctx, token)
//	if errors.Is(err, account.ErrInvalidToken) {
//		// show the expired-link page
//	}
func (directory *Directory) Verify(ctx context.Context, token string) (string, error) {
	return directory.verify(ctx, "", token)
}

// VerifyFor is [Directory.Verify] for a token that must belong to subject, the
// logged-in user: a token of anyone else returns [ErrWrongAccount] and is not
// consumed. [Directory.VerifyHandler] uses
// it, because a link opened by someone who is not logged in as the account
// proves nothing about who holds the account's password: an attacker can
// register a victim's address, and the victim's click on the genuine email must
// not hand the attacker a verified account. Only a password reset, which also
// removes whatever the registrant attached, proves ownership of such an
// account.
//
// Example:
//
//	_, err := directory.VerifyFor(ctx, identity.Subject(), token)
func (directory *Directory) VerifyFor(ctx context.Context, subject, token string) (string, error) {
	return directory.verify(ctx, subject, token)
}

// verify implements [Directory.Verify] and [Directory.VerifyFor]; only is the
// subject the token must belong to, or empty for any.
func (directory *Directory) verify(ctx context.Context, only, token string) (string, error) {
	var subject string
	now := time.Now().Unix()
	err := directory.db.WithTransaction(ctx, func(ctx context.Context) error {
		var err error
		if subject, err = directory.consumeVerification(ctx, only, storage.Hash(token), now); err != nil {
			return err
		}
		_, err = directory.db.Exec(ctx, query.Table(directory.users, func(statement *query.Builder) {
			statement.Where("id", subject).Where("verified_at", 0).Update(map[string]any{"verified_at": now})
		}))

		return err
	})
	switch {
	case errors.Is(err, ErrInvalidToken):
		return "", ErrInvalidToken
	case errors.Is(err, ErrWrongAccount):
		return "", err
	case err != nil:
		return "", fmt.Errorf("account: verify email: %w", err)
	}
	directory.emit(ctx, EmailVerified, subject)

	return subject, nil
}

// consumeVerification deletes the live verification token whose hash is hashed
// and returns its subject. An unknown, expired or used token is
// [ErrInvalidToken], and one of another user than only (when set)
// [ErrWrongAccount]. It runs in the caller's transaction.
func (directory *Directory) consumeVerification(ctx context.Context, only, hashed string, now int64) (string, error) {
	pending, err := directory.db.Find[struct {
		Subject string `db:"subject"`
	}](ctx, query.Table(directory.verifs, func(statement *query.Builder) {
		statement.Select("subject").Where("token_hash", hashed).Where("expires_at", ">", now).Limit(1)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", err
	}
	if only != "" && pending.Subject != only {
		return "", fmt.Errorf("account: verification token of another user: %w", ErrWrongAccount)
	}
	// The delete is the single-use gate: of two concurrent requests with
	// the same token only one removes the row.
	affected, err := directory.db.Exec(ctx, query.Table(directory.verifs, func(statement *query.Builder) {
		statement.Where("token_hash", hashed).Delete()
	}))
	if err != nil {
		return "", err
	}
	if affected == 0 {
		return "", ErrInvalidToken
	}

	return pending.Subject, nil
}

// VerifyHandler returns the handler of POST /email/verify. The body is
// {"token": "..."}; success is 204. It needs a browser session
// ([auth.Identity.Session]) of the user the token belongs to, as GitHub's links
// do: the user opens the link while logged in. Without a session the answer is
// [auth.ErrUnauthenticated] (401); a session of another user is
// [ErrWrongAccount] (403). Either way the token is not consumed, so the owner
// of an address cannot be tricked into verifying an account someone else
// registered and holds the password of (a link opened while logged out proves
// nothing about who controls the account). A bad token is [ErrInvalidToken]
// (422).
//
// Example:
//
//	web.Post("/email/verify", directory.VerifyHandler())
func (directory *Directory) VerifyHandler() router.Handler {
	return web.JSON(func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
		}
		body, err := web.Decode[struct {
			Token string `json:"token"`
		}](r)
		if err != nil {
			return err
		}
		if _, err := directory.VerifyFor(r.Context(), identity.Subject(), body.Token); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	})
}

// ResendHandler returns the handler of POST /email/verify/resend. It needs a
// browser session ([auth.Identity.Session]) and sends a new verification to the
// logged-in user's stored address: 202, whether or not anything was sent and
// whether or not the delivery function failed. The work runs in the
// background, so the response time says nothing about the outcome; a failed
// delivery is logged (without the token) and never reaches the client. Wait for
// the sends with [Directory.Wait]. Without a delivery function the request
// fails with [ErrNoDelivery] (500).
//
// Requests are counted per user and per client address before anything is
// sent: more than 5 per user or 20 per client address in 15 minutes is
// [ErrThrottled] (429, with Retry-After).
//
// Example:
//
//	web.Post("/email/verify/resend", directory.ResendHandler())
func (directory *Directory) ResendHandler() router.Handler {
	return web.JSON(func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
		}
		if directory.deliver == nil {
			return ErrNoDelivery
		}
		ctx, subject := r.Context(), identity.Subject()
		prefix := "access:" + directory.instance + ":account:resend:"
		subjectKey := cache.NewKey[int64](prefix + "subject:" + storage.Hash(subject))
		if err := throttle.Attempt(ctx, directory.store, subjectKey, maxResends, resendWindow, ErrThrottled); err != nil {
			return err
		}
		clientKey := cache.NewKey[int64](prefix + "ip:" + throttle.Bucket(directory.clientIP(r)))
		if err := throttle.Attempt(ctx, directory.store, clientKey, maxClientResends, resendWindow, ErrThrottled); err != nil {
			return err
		}
		directory.jobs.Go(func(ctx context.Context) {
			if err := directory.SendVerification(ctx, subject); err != nil {
				slog.ErrorContext(ctx, "access: verification email failed", "subject", subject, "error", err)
			}
		})

		return response.Status(w, http.StatusAccepted)
	})
}

// RequireVerified returns middleware that admits only users whose email is
// verified, judged by the "email_verified" claim of the identity's record. An
// anonymous request returns [auth.ErrUnauthenticated]; an unverified user
// [ErrEmailUnverified] (403). Install it after auth middleware. It reads the
// claim the directory sets, so it fits only applications that use the
// directory; with their own users, applications check their own state.
//
// The claim is the one the session recorded at login, so a user who verified
// the email since their last login passes only after logging in again. The
// handlers of the other packages that gate on verification (connect, tokens,
// two-factor setup) read the table at request time and are not affected.
//
// Example:
//
//	posts := web.With(auth.Require(), account.RequireVerified())
func RequireVerified() router.Middleware {
	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			identity, err := request.Identity(r)
			if err != nil {
				return err
			}
			if verified, _ := identity.Record().Claims["email_verified"].(bool); !verified {
				return ErrEmailUnverified
			}

			return next(w, r)
		}
	}
}

var _ auth.UserDriver = (*Directory)(nil)
