package access

import (
	"context"
	"errors"

	"arkoren.dev/core/auth"

	"arkoren.dev/ark/access/account"
)

// Users replaces the built-in users table with the application's own. The
// facade then migrates no users table, never creates or deletes users itself,
// and talks to the application's users only through these functions.
//
// Example:
//
//	iam.Users(access.Users{
//		Provider: provider,
//		Lookup:   func(ctx context.Context, email string) (string, string, error) { return store.SubjectAndEmail(ctx, email) },
//		Create:   func(ctx context.Context, email string, verified bool) (string, error) { return store.Create(ctx, email, verified) },
//		Rotate:   func(ctx context.Context, subject string) error { return store.BumpVersion(ctx, subject) },
//	})
//
// Email verification is the application's with its own users. The built-in
// directory refuses unverified users the actions an earlier holder of the
// address could abuse (connecting a provider, enrolling two-factor, creating
// tokens) and, when a password reset proves the mailbox, removes what such a
// holder attached. With your own users the facade does the same through
// [Users.Verified] and [Users.ProveEmail], which are given together or not at
// all and are required whenever [Users.Create] is set (new users start
// unverified); without them (users created elsewhere) it does neither, and the
// application must enforce the rule itself.
type Users struct {
	// Provider serves the application's users to the guard. Its driver's
	// UserRecord.Version must change when Rotate runs.
	Provider *auth.UserProvider

	// Lookup returns the subject of the user with identifier, whatever the
	// application uses to log in (an email, a username), and the address to
	// send mail to, as stored with the user and never the typed identifier. An
	// unknown user must return an error wrapping [auth.ErrUserNotFound]. It is
	// required with [Builder.Password].
	Lookup func(ctx context.Context, identifier string) (subject, address string, err error)

	// Create adds a user and returns the subject. It serves registration (with
	// verified false) and the first login with an external identity (the
	// identifier is then the provider's email, possibly empty, and verified is
	// [auth.External.EmailVerified]); its errors pass through to the client.
	// It must fail on an existing identifier and never return the existing
	// user: connecting an identity to someone by email is how accounts get
	// taken over. For a taken identifier return an error wrapping
	// [ErrIdentifierTaken] (the built-in directory's [account.ErrEmailTaken] is
	// recognized too): registration then answers like a new user and notifies the
	// owner, instead of answering 409. It may be nil when users are created elsewhere.
	Create func(ctx context.Context, identifier string, verified bool) (subject string, err error)

	// Rotate bumps [auth.UserRecord.Version], which ends every session, pending
	// login and API token of the user. A password reset calls it. It is required
	// with [Builder.Password].
	Rotate func(ctx context.Context, subject string) error

	// Verified reports whether the user's email is verified. The handlers that
	// connect a provider, enroll two-factor or create tokens then answer 403
	// [account.ErrEmailUnverified] to unverified users. It must be given with
	// ProveEmail, and is required when Create is set; without it those routes
	// are open to unverified users.
	Verified func(ctx context.Context, subject string) (bool, error)

	// ProveEmail is given together with Verified. A completed password reset calls it, in the
	// reset's transaction, because the link reached the mailbox. It marks the
	// email verified and reports whether it was unverified before; if so the
	// facade deletes the user's connections, two-factor enrolment, recovery
	// codes and tokens, which may belong to whoever registered the address first.
	// An error rolls the reset back.
	ProveEmail func(ctx context.Context, subject string) (wasUnverified bool, err error)
}

// validate checks the functions the enabled features need; password tells
// whether [Builder.Password] is enabled.
func (users *Users) validate(password bool) error {
	switch {
	case users.Provider == nil:
		return errors.New("Users.Provider is required")
	case password && (users.Lookup == nil || users.Rotate == nil):
		return errors.New("Users.Lookup and Users.Rotate are required with Password")
	case (users.Verified == nil) != (users.ProveEmail == nil):
		return errors.New("Users.Verified and Users.ProveEmail are given together or not at all")
	case users.Create != nil && users.Verified == nil:
		return errors.New("Users.Verified and Users.ProveEmail are required with Users.Create: new users start unverified")
	}

	return nil
}

// directoryUsers presents the built-in directory as [Users], so the packages
// are wired the same way whoever owns the users.
func directoryUsers(directory *account.Directory) Users {
	return Users{
		Provider: directory.Provider(),
		Lookup:   directory.Lookup,
		Create: func(ctx context.Context, email string, verified bool) (string, error) {
			if verified {
				return directory.CreateVerified(ctx, email)
			}

			return directory.Create(ctx, email)
		},
		Rotate:     directory.Rotate,
		Verified:   directory.Verified,
		ProveEmail: directory.ProveEmail,
	}
}

// proveEmail returns the hook a password reset runs in its transaction to turn
// the proof of mailbox ownership into a verified email, purging the account
// when it was not verified before. It is nil when there is nothing to prove
// with: the application's own users without [Users.ProveEmail].
func (iam *Access) proveEmail(prove func(context.Context, string) (bool, error)) func(context.Context, string) error {
	if prove == nil {
		return nil
	}

	return func(ctx context.Context, subject string) error {
		wasUnverified, err := prove(ctx, subject)
		if err != nil || !wasUnverified {
			return err
		}

		return iam.purge(ctx, subject)
	}
}

// purge deletes what whoever registered an unverified address first may have
// attached to the account: connected providers, the authenticator and recovery
// codes, tokens, then what [Builder.OnEmailProven] removes. The reset itself
// rotates the user and the handler ends the logins.
func (iam *Access) purge(ctx context.Context, subject string) error {
	for _, remove := range iam.purgers() {
		if err := remove(ctx, subject); err != nil {
			return err
		}
	}

	return nil
}

// purgers lists the removals of [Access.purge] for the enabled packages. It
// runs when a reset happens, because the packages are built after the hook.
func (iam *Access) purgers() []func(context.Context, string) error {
	var removals []func(context.Context, string) error
	if iam.links != nil {
		removals = append(removals, iam.links.UnlinkAll)
	}
	if iam.factors != nil {
		removals = append(removals, iam.factors.Remove)
	}
	if iam.issuer != nil {
		removals = append(removals, iam.issuer.RevokeAll)
	}
	if iam.emailProven != nil {
		removals = append(removals, iam.emailProven)
	}

	return removals
}
