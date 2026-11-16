package password

import (
	"context"
	"errors"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"

	"arkoren.dev/ark/access/internal/storage"
)

// Builder configures [NewVerifier] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	verifier, err := password.NewVerifier(provider, db, store, clientIP, hasher, users, func(passwords *password.Builder) {
//		passwords.Reset(signer, dispatch, deliver).ResetTTL(30 * time.Minute)
//	})
type Builder struct {
	instance     string
	events       *event.Bus
	registration bool
	resetSet     bool
	signer       *link.Signer
	dispatch     func(context.Context, ResetRequest) error
	deliver      func(context.Context, ResetMessage) error
	resetTTL     time.Duration
	policy       func(context.Context, string) error
	registered   func(context.Context, string, string) error
	taken        func(context.Context, string) error
	changed      func(context.Context, string) error
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure password.BuilderFunc = func(passwords *password.Builder) {
//		passwords.Instance("tenant_a")
//	}
//	verifier, err := password.NewVerifier(provider, db, store, clientIP, hasher, users, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: tables are prefixed "<instance>_" and the
// migration group gains ":<instance>". The name is 1 to 20 bytes of [a-z0-9_]
// starting with a letter; the default is the empty instance.
//
// Example:
//
//	passwords.Instance("tenant_a")
func (passwords *Builder) Instance(name string) *Builder {
	passwords.instance = name

	return passwords
}

// Events publishes [Changed] and [WasReset] on bus. A nil bus publishes nothing.
//
// Example:
//
//	passwords.Events(events)
func (passwords *Builder) Events(bus *event.Bus) *Builder {
	passwords.events = bus

	return passwords
}

// Registration enables self-service registration: [Verifier.RegisterHandler]
// creates users with [Users.Create] and the facade mounts POST /register. The
// default is false, because most applications add users themselves (an invite,
// an admin, a social sign-up) and an open registration route is an abuse
// surface they never asked for. With false the handler panics at composition.
// [NewVerifier] fails with [ErrInvalidConfig] when it is true and [Users.Create]
// is nil.
//
// Example:
//
//	passwords.Registration(true)
func (passwords *Builder) Registration(enabled bool) *Builder {
	passwords.registration = enabled

	return passwords
}

// Reset turns on forgotten-password recovery: [Verifier.ForgotHandler] and
// [Verifier.ResetHandler] work, and [Verifier.SendReset] exists for the worker.
// The package keeps no reset table and runs no goroutine; a reset link is a
// value that signer signs and that stops working by itself:
//
//   - It is bound to the user's current stored password hash
//     ([link.Bind]), so the first successful reset or password change
//     invalidates every link sent before, and it expires after
//     [Builder.ResetTTL].
//   - dispatch runs in the request, once per forgot-password request (and for
//     the notice to the owner of a taken identifier, with
//     [ResetRequest.Notice] set, see [Builder.Taken]). It must store the work durably (a queue job, a
//     workflow run, an outbox row) and return, or return an error, which answers
//     500 so the client retries. It receives the identifier exactly as typed and
//     is called whether or not the user exists, so the response and its timing
//     reveal nothing.
//   - The stored work later reaches [Verifier.SendReset] in a worker, which
//     looks the user up, signs the link and calls deliver with the
//     [ResetMessage] to send. deliver renders and sends the email; its error is
//     returned by SendReset so the queue retries, which means deliver may run
//     more than once for one request.
//
// signer, dispatch and deliver are all required: [NewVerifier] fails with
// [ErrInvalidConfig] when any is nil. Use a signer whose key is reserved for
// links, and share it only with code that signs distinct paths; a reset link is
// authenticated for its path, so links of other features never work here.
//
// Example:
//
//	passwords.Reset(signer, func(ctx context.Context, request password.ResetRequest) error {
//		return dispatcher.Dispatch(ctx, resetJob, request)
//	}, func(ctx context.Context, message password.ResetMessage) error {
//		return mailer.Send(ctx, resetEmail(message.Address, message.Link("https://app.example.com/reset-password")))
//	})
func (passwords *Builder) Reset(signer *link.Signer, dispatch func(ctx context.Context, request ResetRequest) error, deliver func(ctx context.Context, message ResetMessage) error) *Builder {
	passwords.signer = signer
	passwords.dispatch = dispatch
	passwords.deliver = deliver
	passwords.resetSet = true

	return passwords
}

// ResetTTL sets how long a reset link is valid; zero selects
// [DefaultResetTTL].
//
// Example:
//
//	passwords.ResetTTL(30 * time.Minute)
func (passwords *Builder) ResetTTL(ttl time.Duration) *Builder {
	passwords.resetTTL = ttl

	return passwords
}

// Policy replaces the password policy, which by default requires [MinLength] to
// [MaxLength] bytes. check returns an error wrapping [ErrInvalidPassword] to
// reject a password; any other error is an infrastructure failure.
//
// Example:
//
//	passwords.Policy(func(ctx context.Context, password string) error {
//		if len(password) < 12 {
//			return fmt.Errorf("too short: %w", passwordpkg.ErrInvalidPassword)
//		}
//		return nil
//	})
func (passwords *Builder) Policy(check func(ctx context.Context, password string) error) *Builder {
	passwords.policy = check

	return passwords
}

// Registered sets a function that runs after [Verifier.RegisterHandler]
// committed a new user and before it logs them in, for work that must follow a
// registration such as sending an email verification. An error is logged and
// does not fail the request: the user is registered either way, and a mail
// outage must not turn into a failed signup.
//
// Example:
//
//	passwords.Registered(func(ctx context.Context, subject, identifier string) error {
//		return directory.RequestVerification(ctx, subject)
//	})
func (passwords *Builder) Registered(hook func(ctx context.Context, subject, identifier string) error) *Builder {
	passwords.registered = hook

	return passwords
}

// Taken sets a function that runs when [Verifier.RegisterHandler] meets an
// identifier that already belongs to a user, with the identifier as typed. It is
// where the owner is told that someone tried to register: dispatch the same kind
// of work as [Builder.Registered] does for a new user, so the two paths cost the
// same and the answer, 202 for both, reveals nothing. [Verifier.RequestReset]
// with [ResetRequest.Notice] set is the reset notice. An error is logged and
// does not change the answer.
//
// Example:
//
//	passwords.Taken(func(ctx context.Context, identifier string) error {
//		return verifier.RequestReset(ctx, password.ResetRequest{Identifier: identifier, Notice: true})
//	})
func (passwords *Builder) Taken(hook func(ctx context.Context, identifier string) error) *Builder {
	passwords.taken = hook

	return passwords
}

// AfterChange sets a function that runs in [Verifier.ChangeHandler] after the new
// password is stored and before the caller's other logins end, for what must
// not survive a password change, such as the user's device tokens. An error
// fails the request; the password has changed by then, so the client may retry.
//
// Example:
//
//	passwords.AfterChange(issuer.RevokeDevices)
func (passwords *Builder) AfterChange(hook func(ctx context.Context, subject string) error) *Builder {
	passwords.changed = hook

	return passwords
}

// validate checks the settings no setter can check alone.
func (passwords *Builder) validate() error {
	if err := storage.ValidInstance(passwords.instance); err != nil {
		return err
	}
	if passwords.resetTTL < 0 {
		return errors.New("negative reset lifetime")
	}
	if passwords.resetSet && (passwords.signer == nil || passwords.dispatch == nil || passwords.deliver == nil) {
		return errors.New("Reset needs a signer, a dispatch function and a deliver function")
	}

	return nil
}
