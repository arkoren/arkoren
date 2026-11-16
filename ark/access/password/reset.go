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
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/internal/web"
)

// resetPath is the path every reset link is signed for. The signature covers
// it, so a link signed for another purpose with the same signer (an email
// verification, say) is never a reset link. The subject travels in the query as
// "subject"; it is only trusted after the signature verified.
const resetPath = "/password/reset"

// ResetRequest is what [Builder.Reset]'s dispatch function receives for every
// forgot-password request, and what [Verifier.SendReset] takes: the work of
// sending one reset email. It is plain data with JSON tags, so a queue job or a
// workflow input can carry it as is.
//
// Stored requests must stay decodable across upgrades: new fields are only ever
// added, never renamed, repurposed or removed, and a missing field keeps its
// zero value. Decode old payloads with the same type.
//
// Example:
//
//	job := queue.NewJob[password.ResetRequest]("send-reset")
//	err := dispatcher.Dispatch(ctx, job, password.ResetRequest{Identifier: "ada@example.com"})
type ResetRequest struct {
	// Identifier is exactly what the user typed, never looked up before the
	// request was dispatched: the response of the forgot-password route must
	// not depend on whether the user exists. [Verifier.SendReset] resolves it.
	Identifier string `json:"identifier"`

	// Notice marks the notice to the owner of an identifier that someone tried to
	// register ([Builder.Taken]): the same link, with an email that says so.
	Notice bool `json:"notice,omitzero"`
}

// ResetMessage is what [Builder.Reset]'s deliver function receives to write one
// reset email. Signed is a credential until it expires or the password changes;
// never log it.
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

// RequestReset calls the dispatch function of [Builder.Reset] with request, in
// the caller's request, which is how the forgot-password route and a [Builder.Taken]
// hook ask for a reset email. The email is sent later by the worker that runs
// [Verifier.SendReset]. It returns the error of dispatch, which means the work
// was not stored, or one wrapping [ErrInvalidConfig] without [Builder.Reset].
//
// Example:
//
//	err := verifier.RequestReset(ctx, password.ResetRequest{Identifier: "ada@example.com", Notice: true})
func (verifier *Verifier) RequestReset(ctx context.Context, request ResetRequest) error {
	if !verifier.ResetEnabled() {
		return fmt.Errorf("%w: RequestReset requires Builder.Reset", ErrInvalidConfig)
	}
	if err := verifier.dispatch(ctx, request); err != nil {
		return fmt.Errorf("password: dispatch reset: %w", err)
	}

	return nil
}

// SendReset is the worker side of a password reset: it looks request.Identifier
// up with [Users.Lookup], signs a link bound to the user's current password hash
// and calls the deliver function of [Builder.Reset] with the address that
// Lookup returned, never the text of the identifier. An unknown or disabled
// user gets nothing and the call returns nil, so a retry never repeats pointless
// work. The error of deliver is returned as is, so a queue retries; because of
// that, deliver may run more than once for one request, and the links of the
// attempts all work until the password changes or they expire. An error is also
// returned when [Users.Lookup] returns no address. It does not throttle:
// [Verifier.ForgotHandler] does, before dispatching. Without
// [Builder.Reset] it returns an error wrapping [ErrInvalidConfig].
//
// It has the shape func(context.Context, ResetRequest) error, so it plugs into
// a queue consumer or a workflow step directly.
//
// Example:
//
//	consumer.Handle(resetJob, verifier.SendReset)
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

	return verifier.deliver(ctx, ResetMessage{
		Address:   address,
		Signed:    signed,
		ExpiresAt: time.Now().Add(verifier.resetTTL),
		Notice:    request.Notice,
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
