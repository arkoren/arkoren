package password

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/internal/web"
)

// resetPath is the path every reset link is signed for. The signature covers
// it, so a link signed for another purpose with the same signer (an email
// verification, say) is never a reset link. The subject travels in the query as
// "subject"; it is only trusted after the signature verified.
const resetPath = "/password/reset"

// resetJobName is the base name of the reset job; a named instance appends its
// own name (see [storage.NewJob]).
const resetJobName = "iam-password-reset"

// ResetRequest is the payload of the reset job that every forgot-password
// request stores on the [Builder.Queue] dispatcher, and what [Verifier.SendReset]
// takes: the work of sending one reset email. It is plain data with JSON tags, so
// a queue job or a workflow input can carry it as is.
//
// Stored requests must stay decodable across upgrades: new fields are only ever
// added, never renamed, repurposed or removed, and a missing field keeps its
// zero value. Decode old payloads with the same type.
//
// Example:
//
//	request := password.ResetRequest{Identifier: "ada@example.com"}
//	err := verifier.SendReset(ctx, request)
type ResetRequest struct {
	// Identifier is exactly what the user typed, never looked up before the
	// request was dispatched: the response of the forgot-password route must
	// not depend on whether the user exists. [Verifier.SendReset] resolves it.
	Identifier string `json:"identifier"`

	// Notice marks the notice to the owner of an identifier that someone tried to
	// register ([Builder.Taken]): the same link, with an email that says so.
	Notice bool `json:"notice,omitzero"`
}

// ResetMessage describes one reset email to the function of [ResetBuilder.Email].
// Signed is a credential until it expires or the password changes; never log
// it.
//
// Example:
//
//	href := message.Link("https://app.example.com/reset-password")
type ResetMessage struct {
	// Address is where to send the email: the address [Users.Lookup] returned
	// for the user, never the text of the request, so a request cannot send a
	// victim's link elsewhere.
	Address string

	// Signed is the signed link value that [Verifier.Reset] accepts. It is a URL
	// (relative, or absolute under the signer's origin) that the front end posts
	// back verbatim; it is not a page address.
	Signed string

	// ExpiresAt is when the link stops working.
	ExpiresAt time.Time

	// Notice is true for the notice to the owner of an identifier that someone
	// tried to register ([ResetRequest.Notice]): the email should say so, and
	// that the link sets a new password if it was not them. It is false for a
	// request of the owner.
	Notice bool
}

// Link returns base with the signed value in the URL fragment, escaped, as
// base#<value>. A fragment is never sent to the server, so the link stays out of
// access logs, proxies and the Referer header of the page it opens. The page
// reads location.hash, decodes it with decodeURIComponent after removing the
// leading "#", removes it from the address bar and posts it as the "token" of
// POST /password/reset. base must not already carry a fragment.
//
// Example:
//
//	message := password.ResetMessage{Signed: "/password/reset?subject=u1"}
//	fmt.Println(message.Link("https://app.example.com/reset")) // https://app.example.com/reset#%2Fpassword%2Freset%3Fsubject%3Du1
func (message ResetMessage) Link(base string) string {
	return base + "#" + url.QueryEscape(message.Signed)
}

// ResetEnabled reports whether [Builder.Reset] was configured, which the
// facade reads to decide whether to mount the forgot and reset routes.
//
// Example:
//
//	if verifier.ResetEnabled() {
//		web.Post("/password/forgot", verifier.ForgotHandler())
//	}
func (verifier *Verifier) ResetEnabled() bool {
	return verifier.signer != nil
}

// RequestReset stores a reset job for request on the [Builder.Queue]
// dispatcher, in the caller's request, which is how the forgot-password route
// and a [Builder.Taken] hook ask for a reset email. The email is sent later by
// the worker that runs [Verifier.RegisterJobs]. It returns the dispatch error,
// which means the work was not stored, or one wrapping [ErrInvalidConfig] without
// [Builder.Reset].
//
// Example:
//
//	err := verifier.RequestReset(ctx, password.ResetRequest{Identifier: "ada@example.com", Notice: true})
func (verifier *Verifier) RequestReset(ctx context.Context, request ResetRequest) error {
	if !verifier.ResetEnabled() {
		return fmt.Errorf("%w: RequestReset requires Builder.Reset", ErrInvalidConfig)
	}
	if err := verifier.dispatcher.Dispatch(ctx, verifier.job, request); err != nil {
		return fmt.Errorf("password: dispatch reset: %w", err)
	}

	return nil
}

// RegisterJobs registers the handler of the reset job on consumer, which the
// worker process runs. It registers nothing, and returns nil, without
// [Builder.Reset]. The job is named "iam-password-reset", with "-<instance>"
// appended (underscores written as hyphens) for a named [Builder.Instance], and
// runs on the queue "iam": a consumer restricted with queue.ConsumerBuilder.Queues
// must list it. It returns an error wrapping [queue.ErrDuplicateName] when the
// handler is already registered. Through the facade, call IAM.RegisterJobs
// instead, which registers the jobs of every enabled package.
//
// Example:
//
//	if err := verifier.RegisterJobs(consumer); err != nil {
//		return err
//	}
func (verifier *Verifier) RegisterJobs(consumer *queue.Consumer) error {
	if !verifier.ResetEnabled() {
		return nil
	}

	return consumer.Handle(verifier.job, verifier.SendReset)
}

// SendReset is the worker side of a password reset: it looks request.Identifier
// up with [Users.Lookup], signs a link bound to the user's current password hash
// and sends the reset email through the mailer of [Builder.Reset] to the address
// that Lookup returned, never the text of the identifier. An unknown or disabled
// user gets nothing and the call returns nil, so a retry never repeats pointless
// work. A mailer error is returned as is, so a queue retries; because of that,
// one request may send more than once, and the links of the attempts all work
// until the password changes or they expire. An email that fails validation
// (see [ResetEmailFunc]) is returned wrapped with [queue.Permanent], because
// retrying cannot fix it. An error is also returned when [Users.Lookup] returns
// no address. It does not throttle: [Verifier.ForgotHandler] does, before
// dispatching. Without [Builder.Reset] it returns an error wrapping
// [ErrInvalidConfig].
//
// It has the shape func(context.Context, ResetRequest) error, so a workflow step
// can use it directly; a queue worker uses [Verifier.RegisterJobs].
//
// Example:
//
//	err := verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"})
func (verifier *Verifier) SendReset(ctx context.Context, request ResetRequest) error {
	if !verifier.ResetEnabled() {
		return fmt.Errorf("%w: SendReset requires Builder.Reset", ErrInvalidConfig)
	}
	subject, address, err := verifier.resetTarget(ctx, request.Identifier)
	if errors.Is(err, errNoTarget) {
		return nil
	}
	if err != nil {
		return err
	}
	stored, _, err := verifier.load(ctx, subject)
	if err != nil {
		return err
	}
	signed, err := verifier.signer.Temporary(resetLink(subject), verifier.resetTTL, link.Bind(stored))
	if err != nil {
		return fmt.Errorf("password: sign reset link: %w", err)
	}
	message := ResetMessage{
		Address:   address,
		Signed:    signed,
		ExpiresAt: time.Now().Add(verifier.resetTTL),
		Notice:    request.Notice,
	}
	email := verifier.resetEmail(message, message.Link(verifier.resetURL))
	if _, err := verifier.mailer.Send(ctx, email); err != nil {
		if errors.Is(err, mail.ErrInvalidMessage) {
			return queue.Permanent(fmt.Errorf("password: reset email: %w", err))
		}

		return fmt.Errorf("password: send reset email: %w", err)
	}

	return nil
}

// defaultResetEmail is the reset email when [ResetBuilder.Email] is not used: a
// plain-text message with the link and its expiry. The notice for the owner of an
// address someone else tried to register says so, because the owner did nothing.
func defaultResetEmail(message ResetMessage, href string) mail.Message {
	subject := "Reset your password"
	reason := "We received a request to reset the password of your account."
	if message.Notice {
		subject = "Someone tried to register with your email address"
		reason = "Someone tried to register an account with this email address, which already has one. If it was you, you can set a new password with the link below."
	}
	body := reason + "\n\n" + href + "\n\nThe link works until " + message.ExpiresAt.UTC().Format(time.RFC1123) + " and only once. If you did not ask for this, you can ignore this email."

	return mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: message.Address}}}, mail.Body{Text: body}, func(letter *mail.MessageBuilder) {
		letter.Subject(subject)
	})
}

// resetLink returns the path and query that a reset link of subject is signed
// for.
func resetLink(subject string) string {
	return (&url.URL{Path: resetPath, RawQuery: url.Values{"subject": {subject}}.Encode()}).String()
}

// errNoTarget is returned by [Verifier.resetTarget] for an unknown or disabled
// user, who gets no reset link.
var errNoTarget = errors.New("password: no reset target")

// resetTarget returns the subject of identifier and the address stored with
// the user, or [errNoTarget] when the user is unknown or disabled.
func (verifier *Verifier) resetTarget(ctx context.Context, identifier string) (subject, address string, err error) {
	subject, address, err = verifier.users.Lookup(ctx, identifier)
	if errors.Is(err, auth.ErrUserNotFound) {
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

// Reset checks a signed reset link and sets the password, calls
// [Users.ProveEmail] when set and then [Users.Rotate], in one transaction, and
// returns the subject. The link only reached someone who controls the mailbox,
// so the reset is the proof of ownership an unverified registration lacks.
// Rotation ends every session, pending login and API token of the user; call
// [auth.Guard.RevokeAll] to remove their login records as well, as
// [Verifier.ResetHandler] does. Reset never logs the user in, so it cannot
// bypass a second factor.
//
// The steps run in this order. A password that fails the policy is
// [ErrInvalidPassword] and is checked first, so a weak password does not use up
// the link. The signed value is parsed and its subject read, the user's current
// hash is loaded and the signature is verified against it. The password is then
// stored by a conditional write inside the transaction: an update that matches
// only the hash the link was signed for (or an insert that does nothing on
// conflict for a user without a password, such as one who signed up with a
// provider). Zero affected rows means a concurrent reset or password change won,
// so of two concurrent submits of one link exactly one succeeds. Ownership
// proof, rotation and the other effects run only when the row changed. Invalid,
// forged, expired, used and replaced links are all [ErrInvalidToken].
//
// Example:
//
//	subject, err := verifier.Reset(ctx, signed, "a new long password")
//	if errors.Is(err, password.ErrInvalidToken) {
//		// ask for a new link
//	}
func (verifier *Verifier) Reset(ctx context.Context, signed, password string) (string, error) {
	if !verifier.ResetEnabled() {
		return "", fmt.Errorf("%w: Reset requires Builder.Reset", ErrInvalidConfig)
	}
	if err := verifier.policy(ctx, password); err != nil {
		return "", err
	}
	target, subject, ok := web.ParseSigned(signed, resetPath)
	if !ok {
		return "", ErrInvalidToken
	}
	stored, has, err := verifier.load(ctx, subject)
	if err != nil {
		return "", err
	}
	if err := verifier.signer.VerifyURL(target, link.Bind(stored)); err != nil {
		return "", ErrInvalidToken
	}
	encoded, err := verifier.encode(password)
	if err != nil {
		return "", err
	}
	err = verifier.db.WithTransaction(ctx, func(ctx context.Context) error {
		return verifier.applyReset(ctx, subject, stored, has, encoded)
	})
	if errors.Is(err, ErrInvalidToken) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", fmt.Errorf("password: reset: %w", err)
	}
	verifier.emit(ctx, WasReset, subject)

	return subject, nil
}

// applyReset is the transaction of [Verifier.Reset]: it swaps the hash and only
// when a row changed proves the email and rotates the user, so a link already
// used is [ErrInvalidToken] and has no other effect.
func (verifier *Verifier) applyReset(ctx context.Context, subject string, stored []byte, has bool, encoded string) error {
	changed, err := verifier.swapHash(ctx, subject, stored, has, encoded)
	if err != nil {
		return err
	}
	if !changed {
		return ErrInvalidToken
	}
	if verifier.users.ProveEmail != nil {
		if err := verifier.users.ProveEmail(ctx, subject); err != nil {
			return err
		}
	}

	return verifier.users.Rotate(ctx, subject)
}

// swapHash stores encoded as the hash of subject if and only if the stored hash
// is still the one the link was signed for, and reports whether a row changed.
// It is the single-use gate of a reset link: the update matches the old hash,
// and a user who had no hash gets an insert that does nothing when a concurrent
// reset created the row first. It runs in the caller's transaction.
func (verifier *Verifier) swapHash(ctx context.Context, subject string, stored []byte, has bool, encoded string) (bool, error) {
	now := time.Now().Unix()
	var affected int64
	var err error
	if has {
		affected, err = verifier.db.Exec(ctx, query.Table(verifier.hashes, func(statement *query.Builder) {
			statement.Where("subject", subject).Where("hash", base64.StdEncoding.EncodeToString(stored)).Update(map[string]any{"hash": encoded, "changed_at": now})
		}))
	} else {
		affected, err = verifier.db.Exec(ctx, query.Table(verifier.hashes, func(statement *query.Builder) {
			statement.Insert(map[string]any{"subject": subject, "hash": encoded, "changed_at": now})
			statement.OnConflict("subject").DoNothing()
		}))
	}
	if err != nil {
		return false, fmt.Errorf("password: store hash: %w", err)
	}

	return affected > 0, nil
}
