package account

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
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

// verifyPath is the path every verification link is signed for. The signature
// covers it, so a link signed for another purpose with the same signer (a
// password reset, say) is never a verification link. The subject travels in the
// query as "subject"; it is only trusted after the signature verified.
const verifyPath = "/email/verify"

// VerifyRequest is what [Builder.Verification]'s dispatch function receives, and
// what [Directory.SendVerification] takes: the work of sending one verification
// email. It is plain data with a JSON tag, so a queue job or a workflow input can
// carry it as is.
//
// Stored requests must stay decodable across upgrades: new fields are only ever
// added, never renamed, repurposed or removed, and a missing field keeps its
// zero value.
//
// Example:
//
//	job := queue.NewJob[account.VerifyRequest]("send-verification")
//	err := dispatcher.Dispatch(ctx, job, account.VerifyRequest{Subject: subject})
type VerifyRequest struct {
	// Subject is the user to verify. It comes from the application or the
	// session, never from the text of a request.
	Subject string `json:"subject"`
}

// VerifyMessage is what [Builder.Verification]'s deliver function receives to
// write one verification email. Signed is a credential until it expires or the
// user's email or verified state changes; never log it.
//
// Example:
//
//	href := message.Link("https://app.example.com/verify-email")
type VerifyMessage struct {
	// Address is where to send the email: the email stored with the user.
	Address string

	// Signed is the signed link value that [Directory.VerifyHandler] accepts. It
	// is a URL (relative, or absolute under the signer's origin) that the front
	// end posts back verbatim; it is not a page address.
	Signed string

	// ExpiresAt is when the link stops working.
	ExpiresAt time.Time
}

// Link returns base with the signed value in the URL fragment, escaped, as
// base#<value>. A fragment is never sent to the server, so the link stays out of
// access logs, proxies and the Referer header of the page it opens. The page
// reads location.hash, decodes it with decodeURIComponent after removing the
// leading "#", removes it from the address bar and posts it as the "token" of
// POST /email/verify. base must not already carry a fragment.
//
// Example:
//
//	message := account.VerifyMessage{Signed: "/email/verify?subject=u1"}
//	fmt.Println(message.Link("https://app.example.com/verify")) // https://app.example.com/verify#%2Femail%2Fverify%3Fsubject%3Du1
func (message VerifyMessage) Link(base string) string {
	return base + "#" + url.QueryEscape(message.Signed)
}

// VerificationEnabled reports whether [Builder.Verification] was configured,
// which the facade reads to decide whether to mount the verification routes and
// whether a registration sends one.
//
// Example:
//
//	if directory.VerificationEnabled() {
//		web.Post("/email/verify", directory.VerifyHandler())
//	}
func (directory *Directory) VerificationEnabled() bool {
	return directory.signer != nil
}

// RequestVerification calls the dispatch function of [Builder.Verification]
// with a [VerifyRequest] for subject, in the caller's request, which is how the
// resend route and a registration ask for a verification email. The email is
// sent later by the worker that runs [Directory.SendVerification]. It returns
// the error of dispatch, or one wrapping [ErrInvalidConfig] without
// [Builder.Verification].
//
// Example:
//
//	if err := directory.RequestVerification(ctx, subject); err != nil {
//		return err
//	}
func (directory *Directory) RequestVerification(ctx context.Context, subject string) error {
	if !directory.VerificationEnabled() {
		return fmt.Errorf("%w: RequestVerification requires Builder.Verification", ErrInvalidConfig)
	}
	if err := directory.dispatch(ctx, VerifyRequest{Subject: subject}); err != nil {
		return fmt.Errorf("account: dispatch verification: %w", err)
	}

	return nil
}

// verifyBinding is the state a verification link is bound to: the user's email
// and whether it is verified. Changing either invalidates the links sent before,
// which is what makes a link single use without a table.
func verifyBinding(email string, verified bool) link.CallOption {
	return link.Bind([]byte(email + "\x00" + strconv.FormatBool(verified)))
}

// SendVerification is the worker side of an email verification: it signs a link
// bound to the user's current email and calls the deliver function of
// [Builder.Verification] with the address stored with the user. A user who is
// unknown (deleted since the request was dispatched) or who already verified
// their email gets nothing and the call returns nil, so a retry never repeats
// pointless work. The error of deliver is returned as is, so a queue retries;
// because of that, deliver may run more than once for one request, and the links
// of the attempts all work until the email is verified or changes, or they
// expire. Without [Builder.Verification] it returns an error wrapping
// [ErrInvalidConfig].
//
// It has the shape func(context.Context, VerifyRequest) error, so it plugs into
// a queue consumer or a workflow step directly.
//
// Example:
//
//	consumer.Handle(verifyJob, directory.SendVerification)
func (directory *Directory) SendVerification(ctx context.Context, request VerifyRequest) error {
	if !directory.VerificationEnabled() {
		return fmt.Errorf("%w: SendVerification requires Builder.Verification", ErrInvalidConfig)
	}
	row, err := directory.row(ctx, "id", request.Subject)
	if errors.Is(err, auth.ErrUserNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.VerifiedAt != 0 {
		return nil
	}
	path := (&url.URL{Path: verifyPath, RawQuery: url.Values{"subject": {row.ID}}.Encode()}).String()
	signed, err := directory.signer.Temporary(path, directory.verifyTTL, verifyBinding(row.Email, false))
	if err != nil {
		return fmt.Errorf("account: sign verification link: %w", err)
	}

	return directory.deliver(ctx, VerifyMessage{Address: row.Email, Signed: signed, ExpiresAt: time.Now().Add(directory.verifyTTL)})
}

// Verify checks a signed verification link and marks the user's email
// verified. It returns the subject and publishes [EmailVerified] after the
// commit. A link that is malformed, forged, expired, or no longer matches the
// user (the email changed, or was verified already, so a second click lands
// here) is [ErrInvalidToken]. Without [Builder.Verification] it returns an error
// wrapping [ErrInvalidConfig].
//
// Example:
//
//	subject, err := directory.Verify(ctx, signed)
//	if errors.Is(err, account.ErrInvalidToken) {
//		// show the expired-link page
//	}
func (directory *Directory) Verify(ctx context.Context, signed string) (string, error) {
	return directory.verify(ctx, "", signed)
}

// VerifyFor is [Directory.Verify] for a link that must belong to subject, the
// logged-in user: a link of anyone else returns [ErrWrongAccount] and changes
// nothing. [Directory.VerifyHandler] uses
// it, because a link opened by someone who is not logged in as the account
// proves nothing about who holds the account's password: an attacker can
// register a victim's address, and the victim's click on the genuine email must
// not hand the attacker a verified account. Only a password reset, which also
// removes whatever the registrant attached, proves ownership of such an
// account.
//
// Example:
//
//	_, err := directory.VerifyFor(ctx, identity.Subject(), signed)
func (directory *Directory) VerifyFor(ctx context.Context, subject, signed string) (string, error) {
	return directory.verify(ctx, subject, signed)
}

// verify implements [Directory.Verify] and [Directory.VerifyFor]; only is the
// subject the link must belong to, or empty for any. The order matters: the
// link is authenticated against the user's current state before its subject is
// compared with only, so a forged link never reaches [ErrWrongAccount], and the
// conditional update at the end is the single-use gate.
func (directory *Directory) verify(ctx context.Context, only, signed string) (string, error) {
	if !directory.VerificationEnabled() {
		return "", fmt.Errorf("%w: Verify requires Builder.Verification", ErrInvalidConfig)
	}
	target, subject, ok := web.ParseSigned(signed, verifyPath)
	if !ok {
		return "", ErrInvalidToken
	}
	row, err := directory.row(ctx, "id", subject)
	if errors.Is(err, auth.ErrUserNotFound) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", err
	}
	if err := directory.signer.VerifyURL(target, verifyBinding(row.Email, row.VerifiedAt != 0)); err != nil {
		return "", ErrInvalidToken
	}
	if only != "" && subject != only {
		return "", fmt.Errorf("account: verification link of another user: %w", ErrWrongAccount)
	}
	// The conditional update is the single-use gate: it matches only the state
	// the link was signed for, so of two concurrent clicks exactly one changes
	// the row, and the other (or a click after the email changed) is invalid.
	affected, err := directory.db.Exec(ctx, query.Table(directory.users, func(statement *query.Builder) {
		statement.Where("id", subject).Where("email", row.Email).Where("verified_at", 0).Update(map[string]any{"verified_at": time.Now().Unix()})
	}))
	if err != nil {
		return "", fmt.Errorf("account: verify email: %w", err)
	}
	if affected == 0 {
		return "", ErrInvalidToken
	}
	directory.emit(ctx, EmailVerified, subject)

	return subject, nil
}

// VerifyHandler returns the handler of POST /email/verify. The body is
// {"token": "..."}, where the token is the signed value of
// [VerifyMessage.Signed]; success is 204. It needs a browser session
// ([auth.Identity.Session]) of the user the link belongs to, as GitHub's links
// do: the user opens the link while logged in. Without a session the answer is
// [auth.ErrUnauthenticated] (401); a session of another user is
// [ErrWrongAccount] (403). Either way nothing changes, so the owner of an
// address cannot be tricked into verifying an account someone else registered
// and holds the password of (a link opened while logged out proves nothing about
// who controls the account). A bad, expired or used link, which includes a
// second click on the same link, is [ErrInvalidToken] (422). It panics at
// composition without [Builder.Verification].
//
// Example:
//
//	web.Post("/email/verify", directory.VerifyHandler())
func (directory *Directory) VerifyHandler() router.Handler {
	if !directory.VerificationEnabled() {
		panic("account: VerifyHandler requires Builder.Verification")
	}

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
// browser session ([auth.Identity.Session]) and asks for a new verification of
// the logged-in user with [Directory.RequestVerification], which calls the
// dispatch function of [Builder.Verification]; the answer is 202 whether or not
// anything will be sent, because the user may already be verified and the
// worker decides. A dispatch error is returned (500), because the work was not
// stored.
//
// Requests are counted per user and per client address before anything is
// dispatched: more than 5 per user or 20 per client address in 15 minutes is
// [ErrThrottled] (429, with Retry-After). It panics at composition without
// [Builder.Verification].
//
// Example:
//
//	web.Post("/email/verify/resend", directory.ResendHandler())
func (directory *Directory) ResendHandler() router.Handler {
	if !directory.VerificationEnabled() {
		panic("account: ResendHandler requires Builder.Verification")
	}

	return web.JSON(func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Session(r)
		if err != nil {
			return err
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
		if err := directory.RequestVerification(ctx, subject); err != nil {
			return err
		}

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
