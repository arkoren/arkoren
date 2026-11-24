package iam

import (
	"context"
	"errors"
	"log/slog"

	"arkoren.dev/core/auth"

	"arkoren.dev/ark/iam/account"
)

// UsersBuilder configures the application's own user store in a [Builder.Users]
// call. The facade then migrates no users table, never creates or deletes users
// itself, and talks to the application's users only through these functions.
// Callbacks run directly once, in order, on one fresh draft and methods return
// the same draft.
//
// Example:
//
//	identity.Users(provider, func(users *iam.UsersBuilder) {
//		users.Lookup(store.SubjectAndEmail)
//		users.Rotate(store.BumpVersion)
//		users.Verification(store.EmailVerified, store.MarkEmailVerified)
//		users.Create(store.Create)
//	})
//
// Email verification is the application's with its own users. The built-in
// directory refuses unverified users the actions an earlier holder of the
// address could abuse (connecting a provider, enrolling two-factor, creating
// tokens) and, when a password reset proves the mailbox, removes what such a
// holder attached. With your own users the facade does the same through
// [UsersBuilder.Verification], which is required whenever [UsersBuilder.Create]
// is set (new users start unverified); without it (users created elsewhere) the
// facade does neither, and the application must enforce the rule itself.
type UsersBuilder struct {
	provider   *auth.UserProvider
	lookup     func(ctx context.Context, identifier string) (subject, address string, err error)
	create     func(ctx context.Context, identifier string, verified bool) (subject string, err error)
	rotate     func(ctx context.Context, subject string) error
	verified   func(ctx context.Context, subject string) (bool, error)
	proveEmail func(ctx context.Context, subject string) (wasUnverified bool, err error)
}

// UsersBuilderFunc is the callback form of [UsersBuilder].
//
// Example:
//
//	var configure iam.UsersBuilderFunc = func(users *iam.UsersBuilder) {
//		users.Lookup(store.SubjectAndEmail).Rotate(store.BumpVersion)
//	}
//	identity.Users(provider, configure)
type UsersBuilderFunc = func(*UsersBuilder)

// Lookup sets the function that returns the subject of the user with
// identifier, whatever the application uses to log in (an email, a username),
// and the address to send mail to, as stored with the user and never the typed
// identifier. An unknown user must return an error wrapping
// [auth.ErrUserNotFound]. It is required with [Builder.Password].
//
// Example:
//
//	users.Lookup(store.SubjectAndEmail)
func (users *UsersBuilder) Lookup(lookup func(ctx context.Context, identifier string) (subject, address string, err error)) *UsersBuilder {
	users.lookup = lookup

	return users
}

// Create sets the function that adds a user and returns the subject. It serves
// registration (with verified false) and the first login with an external
// identity (the identifier is then the provider's email, possibly empty, and
// verified is [auth.External.EmailVerified]); its errors pass through to the
// client. Both are off by default: [password.Builder.Registration] and
// [ProviderBuilder.SignUp] must be enabled, and [New] ignores each, with a
// warning, when Create is unset.
//
// Create must fail on an existing identifier and never return the existing
// user: connecting an identity to someone by email is how accounts get taken
// over. For a taken identifier return an error wrapping [ErrIdentifierTaken]
// (the built-in directory's [account.ErrEmailTaken] is recognized too):
// registration then answers like a new user and notifies the owner, instead of
// answering 409. Requires [UsersBuilder.Verification].
//
// Example:
//
//	users.Create(store.Create)
func (users *UsersBuilder) Create(create func(ctx context.Context, identifier string, verified bool) (subject string, err error)) *UsersBuilder {
	users.create = create

	return users
}

// Rotate sets the function that bumps [auth.UserRecord.Version], which ends every
// session, pending login and API token of the user. A password reset calls it
// inside its transaction. It is required with [Builder.Password].
//
// Example:
//
//	users.Rotate(store.BumpVersion)
func (users *UsersBuilder) Rotate(rotate func(ctx context.Context, subject string) error) *UsersBuilder {
	users.rotate = rotate

	return users
}

// Verification wires email-verification checks and mailbox-proof cleanup.
//
// verified reports whether the user's email is verified. The handlers that
// connect a provider, enroll two-factor or create tokens then answer 403
// [account.ErrEmailUnverified] to unverified users, and [IAM.RequireVerified]
// checks it on each request.
//
// proveEmail runs in the transaction of a completed password reset because the
// link reached the mailbox. It marks the email verified and reports whether it
// was unverified before; if so the facade deletes the user's connections,
// two-factor enrollment, recovery codes and tokens, which may belong to whoever
// registered the address first. An error rolls the reset back.
//
// Both functions must be non-nil (or both nil to clear). Required whenever
// [UsersBuilder.Create] is set.
//
// Example:
//
//	users.Verification(store.EmailVerified, store.MarkEmailVerified)
func (users *UsersBuilder) Verification(
	verified func(ctx context.Context, subject string) (bool, error),
	proveEmail func(ctx context.Context, subject string) (wasUnverified bool, err error),
) *UsersBuilder {
	users.verified = verified
	users.proveEmail = proveEmail

	return users
}

// validate checks the functions the enabled features need; password tells
// whether [Builder.Password] is enabled.
func (users *UsersBuilder) validate(password bool) error {
	switch {
	case users.provider == nil:
		return errors.New("Users provider is required")
	case password && (users.lookup == nil || users.rotate == nil):
		return errors.New("UsersBuilder.Lookup and UsersBuilder.Rotate are required with Password")
	case (users.verified == nil) != (users.proveEmail == nil):
		return errors.New("UsersBuilder.Verification requires both verified and proveEmail")
	case users.create != nil && users.verified == nil:
		return errors.New("UsersBuilder.Verification is required with UsersBuilder.Create: new users start unverified")
	}

	return nil
}

// directoryUsers presents the built-in directory as [UsersBuilder], so the
// packages are wired the same way whoever owns the users.
func directoryUsers(directory *account.Directory) UsersBuilder {
	return UsersBuilder{
		provider: directory.Provider(),
		lookup:   directory.Lookup,
		create: func(ctx context.Context, email string, verified bool) (string, error) {
			if verified {
				return directory.CreateVerified(ctx, email)
			}

			return directory.Create(ctx, email)
		},
		rotate:     directory.Rotate,
		verified:   directory.Verified,
		proveEmail: directory.ProveEmail,
	}
}

// proveEmail returns the hook a password reset runs in its transaction to turn
// the proof of mailbox ownership into a verified email, purging the account
// when it was not verified before. It is nil when there is nothing to prove
// with: the application's own users without [UsersBuilder.Verification].
func (identity *IAM) proveEmail(prove func(context.Context, string) (bool, error)) func(context.Context, string) error {
	if prove == nil {
		return nil
	}

	return func(ctx context.Context, subject string) error {
		wasUnverified, err := prove(ctx, subject)
		if err != nil || !wasUnverified {
			return err
		}

		return identity.purge(ctx, subject)
	}
}

// purge deletes what whoever registered an unverified address first may have
// attached to the account: connected providers, the authenticator and recovery
// codes, tokens, then what [Builder.OnEmailProven] removes. The reset itself
// rotates the user and the handler ends the logins.
func (identity *IAM) purge(ctx context.Context, subject string) error {
	for _, remove := range identity.purgers() {
		if err := remove(ctx, subject); err != nil {
			return err
		}
	}

	return nil
}

// purgers lists the removals of [IAM.purge] for the enabled packages. It
// runs when a reset happens, because the packages are built after the hook.
func (identity *IAM) purgers() []func(context.Context, string) error {
	var removals []func(context.Context, string) error
	if identity.links != nil {
		removals = append(removals, identity.links.UnlinkAll)
	}
	if identity.factors != nil {
		removals = append(removals, identity.factors.Remove)
	}
	if identity.issuer != nil {
		removals = append(removals, identity.issuer.RevokeAll)
	}
	if identity.emailProven != nil {
		removals = append(removals, identity.emailProven)
	}

	return removals
}

// takeOver returns the [federation.Builder.TakeOver] function of the facade,
// built only with the built-in users (the application's own
// [UsersBuilder.Lookup] takes whatever it logs in with, which need not be an
// email): the owner of an email a provider verified takes over the user who
// registered that address and never verified it. It exists because registration
// with a password creates an unverified user: without it whoever typed an
// address first would keep the real owner out of the one login they can prove,
// and could have attached a password, a provider, an authenticator or tokens
// that stay valid after the owner arrives.
//
// Only positive evidence counts: the identity must have EmailVerified set (the
// ark/oauth presets set it only for a provider-verified address: Google's
// email_verified, GitHub's primary verified address, Microsoft's xms_edov, never
// Microsoft's mutable email claim alone), and the user must be unverified. The
// work is the one a password reset that proves the mailbox does, plus removing
// the password and rotating the user. It runs in the transaction of the login,
// so it is all or nothing and the order of its steps is not observable; it
// differs from "delete the password, purge, mark verified" only in marking the
// email first, because proveEmail is the atomic test of "was unverified": a
// user verified a moment earlier reports false and nothing is removed. An
// unknown or verified address returns "" and the login proceeds as usual.
func (identity *IAM) takeOver(users UsersBuilder) func(context.Context, auth.External) (string, error) {
	return func(ctx context.Context, external auth.External) (string, error) {
		if !external.EmailVerified || external.Email == "" {
			return "", nil
		}
		subject, _, err := users.lookup(ctx, external.Email)
		if errors.Is(err, auth.ErrUserNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		// Mark the email verified; only a user that was unverified is taken over.
		wasUnverified, err := users.proveEmail(ctx, subject)
		if err != nil || !wasUnverified {
			return "", err
		}
		// The password is the registrant's: delete it so it cannot log in again.
		if identity.verifier != nil {
			if err := identity.verifier.Remove(ctx, subject); err != nil {
				return "", err
			}
		}
		// Connections, authenticator, recovery codes, tokens and the application's
		// OnEmailProven hook: whatever the registrant attached.
		if err := identity.purge(ctx, subject); err != nil {
			return "", err
		}
		// Bump the version: every session and pending login of the registrant ends.
		if err := users.rotate(ctx, subject); err != nil {
			return "", err
		}
		// The new version already makes their sessions stale; this also deletes the
		// login records, so none of them is listed or revived. After the commit,
		// because it works on the cache, and best effort: an error from an
		// after-commit hook would surface from the login's transaction as a failure
		// of a takeover that is already committed, and the rotation has ended the
		// sessions either way. The guard is built after the links, so it is read
		// when the hook runs.
		_ = identity.db.AfterCommit(ctx, func(ctx context.Context) error {
			if err := identity.guard.RevokeAll(ctx, subject); err != nil {
				slog.ErrorContext(ctx, "access: delete login records after a takeover", "subject", subject, "error", err)
			}

			return nil
		})

		return subject, nil
	}
}
