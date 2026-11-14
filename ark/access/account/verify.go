package account

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/ark/access/internal/web"
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

// Link returns base with the token as its "token" query parameter, escaped.
//
// Example:
//
//	message := account.Message{Token: "abc"}
//	fmt.Println(message.Link("https://app.example.com/verify")) // https://app.example.com/verify?token=abc
func (message Message) Link(base string) string {
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
	var subject string
	hashed := storage.Hash(token)
	now := time.Now().Unix()
	err := directory.db.WithTransaction(ctx, func(ctx context.Context) error {
		pending, err := directory.db.Find[struct {
			Subject string `db:"subject"`
		}](ctx, query.Table(directory.verifs, func(statement *query.Builder) {
			statement.Select("subject").Where("token_hash", hashed).Where("expires_at", ">", now).Limit(1)
		}))
		if errors.Is(err, database.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		// The delete is the single-use gate: of two concurrent requests with
		// the same token only one removes the row.
		affected, err := directory.db.Exec(ctx, query.Table(directory.verifs, func(statement *query.Builder) {
			statement.Where("token_hash", hashed).Delete()
		}))
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrInvalidToken
		}
		subject = pending.Subject
		_, err = directory.db.Exec(ctx, query.Table(directory.users, func(statement *query.Builder) {
			statement.Where("id", subject).Where("verified_at", 0).Update(map[string]any{"verified_at": now})
		}))

		return err
	})
	if err != nil {
		if errors.Is(err, ErrInvalidToken) {
			return "", ErrInvalidToken
		}

		return "", fmt.Errorf("account: verify email: %w", err)
	}
	directory.emit(ctx, EmailVerified, subject)

	return subject, nil
}

// VerifyHandler returns the handler of POST /email/verify. The body is
// {"token": "..."}; success is 204. It needs no login, because the link is
// usually opened on another device. A bad token is [ErrInvalidToken] (422).
//
// Example:
//
//	web.Post("/email/verify", directory.VerifyHandler())
func (directory *Directory) VerifyHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		body, err := web.Decode[struct {
			Token string `json:"token"`
		}](r)
		if err != nil {
			return err
		}
		if _, err := directory.Verify(r.Context(), body.Token); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// ResendHandler returns the handler of POST /email/verify/resend. It needs a
// browser session ([auth.Identity.Session]) and sends a new verification to the
// logged-in user: 202, whether or not anything was sent. Without a delivery
// function the request fails with [ErrNoDelivery] (500).
//
// Example:
//
//	web.Post("/email/verify/resend", directory.ResendHandler())
func (directory *Directory) ResendHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
		}
		if err := directory.SendVerification(r.Context(), identity.Subject()); err != nil {
			return err
		}

		return response.Status(w, http.StatusAccepted)
	}
}

// RequireVerified returns middleware that admits only users whose email is
// verified, judged by the "email_verified" claim of the identity's record. An
// anonymous request returns [auth.ErrUnauthenticated]; an unverified user
// [ErrEmailUnverified] (403). Install it after auth middleware. It reads the
// claim the directory sets, so it fits only applications that use the
// directory; with their own users, applications check their own state.
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
