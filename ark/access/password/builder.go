package password

import (
	"context"
	"errors"
	"net/http"
	"time"

	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/core/event"
)

// Builder configures [NewVerifier] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	verifier, err := password.NewVerifier(provider, db, store, hasher, users, func(passwords *password.Builder) {
//		passwords.Deliver(send).ResetTTL(30 * time.Minute)
//	})
type Builder struct {
	instance   string
	events     *event.Bus
	deliver    func(context.Context, Message) error
	resetTTL   time.Duration
	policy     func(context.Context, string) error
	clientIP   func(*http.Request) string
	registered func(context.Context, string, string) error
	changed    func(context.Context, string) error
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure password.BuilderFunc = func(passwords *password.Builder) {
//		passwords.Instance("tenant_a")
//	}
//	verifier, err := password.NewVerifier(provider, db, store, hasher, users, configure)
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

// Deliver sets the function that sends reset links. Without it
// [Verifier.SendReset] returns [ErrNoDelivery].
//
// Example:
//
//	passwords.Deliver(func(ctx context.Context, message password.Message) error {
//		return mailer.Send(ctx, message.Identifier, message.Link("https://app.example.com/reset"))
//	})
func (passwords *Builder) Deliver(deliver func(ctx context.Context, message Message) error) *Builder {
	passwords.deliver = deliver

	return passwords
}

// ResetTTL sets how long a reset token is valid; zero selects
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

// ClientIP sets how the client address of the login throttle is found. The
// default is the host of r.RemoteAddr, which behind a reverse proxy is the
// proxy: pass the same function given to the guard's ClientIP, such as
// ratelimit.TrustedProxyKey.
//
// Example:
//
//	passwords.ClientIP(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
func (passwords *Builder) ClientIP(find func(*http.Request) string) *Builder {
	passwords.clientIP = find

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
//		return directory.SendVerification(ctx, subject)
//	})
func (passwords *Builder) Registered(hook func(ctx context.Context, subject, identifier string) error) *Builder {
	passwords.registered = hook

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

	return nil
}
