package password

import (
	"context"
	"errors"
	"net/url"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/queue"

	"arkoren.dev/ark/iam/internal/storage"
	"arkoren.dev/ark/iam/internal/web"
)

// Builder configures [NewVerifier] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	verifier, err := password.NewVerifier(provider, db, store, clientIP, hasher, users, func(passwords *password.Builder) {
//		passwords.Queue(dispatcher).Reset(signer, mailer, "https://app.example.com/reset-password").ResetTTL(30 * time.Minute)
//	})
type Builder struct {
	instance     string
	events       *event.Bus
	registration bool
	resetSet     bool
	signer       *link.Signer
	mailer       *mail.Mailer
	resetURL     string
	resetEmail   ResetEmailFunc
	dispatcher   *queue.Dispatcher
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
// [NewVerifier] ignores it, with one warning, when it is true and [Users.Create]
// is nil: nothing could create the user. Through the facade with the built-in
// users, registration also needs [Builder.Reset]: iam.New ignores it, with a
// warning, without it,
// because the reset notice is how the owner of an address that someone else
// registered first learns of it and recovers the account.
//
// Example:
//
//	passwords.Registration(true).Reset(signer, mailer, "https://app.example.com/reset-password")
func (passwords *Builder) Registration(enabled bool) *Builder {
	passwords.registration = enabled

	return passwords
}

// Queue sets the dispatcher that stores the module's background work. Reset
// needs it: the forgot-password route stores one job per request on it and
// returns, and the worker that runs [Verifier.RegisterJobs] sends the email. Through
// the facade, iam.Builder.Queue sets it for every package, so only code that
// builds a [Verifier] by itself calls it. The jobs run on the
// queue "iam"; see [Verifier.RegisterJobs]. A later call replaces the earlier
// one.
//
// Example:
//
//	passwords.Queue(dispatcher)
func (passwords *Builder) Queue(dispatcher *queue.Dispatcher) *Builder {
	passwords.dispatcher = dispatcher

	return passwords
}

// Reset turns forgotten-password recovery on: calling it makes
// [Verifier.ForgotHandler] and [Verifier.ResetHandler] work, and
// [Verifier.SendReset] and [Verifier.RegisterJobs] exist for the worker.
// Recovery is off when the method is never called; to switch it from
// configuration, wrap the call in an if. A later call replaces the earlier one.
// The package keeps no reset table and runs no goroutine; a reset link is a
// value that signer signs and that stops working by itself:
//
//   - It is bound to the user's current stored password hash
//     ([link.Bind]), so the first successful reset or password change
//     invalidates every link sent before, and it expires after
//     [Builder.ResetTTL].
//   - The forgot-password route stores one job on the [Builder.Queue] dispatcher
//     per request (and the notice to the owner of a taken identifier, with
//     [ResetRequest.Notice] set, see [Builder.Taken]) and returns, or returns an
//     error, which answers 500 so the client retries. The job carries the
//     identifier exactly as typed and is stored whether or not the user exists,
//     so the response and its timing reveal nothing.
//   - The stored job later reaches [Verifier.SendReset] in a worker, which looks
//     the user up, signs the link and sends the email through mailer. Its error
//     is returned so the queue retries, which means one request may send more
//     than once.
//
// resetURL is the page of the application that the link opens, for example
// "https://app.example.com/reset-password". The signed value travels in the
// URL fragment ([ResetMessage.Link]), so the page reads it from location.hash
// and posts it to POST /password/reset. It must be an absolute URL without a
// fragment. The email is a plain-text message with a default subject and
// wording; configure may replace it with [ResetBuilder.Email]. The callbacks run
// directly once, in order, on one fresh [ResetBuilder].
//
// Signer, mailer, resetURL and a [Builder.Queue] dispatcher are all required:
// [NewVerifier] fails with [ErrInvalidConfig] when any is missing or invalid.
// Use a signer whose key is reserved for links, and share it only with code that
// signs distinct paths; a reset link is authenticated for its path, so links of
// other features never work here.
//
// Example:
//
//	passwords.Queue(dispatcher).Reset(signer, mailer, "https://app.example.com/reset-password", func(reset *password.ResetBuilder) {
//		reset.Email(resetEmail)
//	})
func (passwords *Builder) Reset(signer *link.Signer, mailer *mail.Mailer, resetURL string, configure ...ResetBuilderFunc) *Builder {
	passwords.resetSet = true
	passwords.signer = signer
	passwords.mailer = mailer
	passwords.resetURL = resetURL

	draft := ResetBuilder{}
	for _, apply := range configure {
		apply(&draft)
	}

	passwords.resetEmail = draft.email

	return passwords
}

// ResetBuilder configures the email of [Builder.Reset]. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	passwords.Reset(signer, mailer, resetURL, func(reset *password.ResetBuilder) {
//		reset.Email(resetEmail)
//	})
type ResetBuilder struct {
	email ResetEmailFunc
}

// ResetBuilderFunc is the callback form of [ResetBuilder].
//
// Example:
//
//	var configure password.ResetBuilderFunc = func(reset *password.ResetBuilder) {
//		reset.Email(resetEmail)
//	}
//	passwords.Reset(signer, mailer, resetURL, configure)
type ResetBuilderFunc = func(*ResetBuilder)

// ResetEmailFunc writes the email of a password reset: message describes the
// request and href is the page link to put in the body, the application's reset
// URL with the signed value in its fragment. The result needs the recipient
// message.Address and a body; the sender comes from the mailer's defaults unless
// the message sets it. A message that fails validation is a permanent failure of
// the job, because retrying cannot fix it.
//
// Example:
//
//	var resetEmail password.ResetEmailFunc = func(message password.ResetMessage, href string) mail.Message {
//		return mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: message.Address}}}, mail.Body{Text: href}, func(letter *mail.MessageBuilder) {
//			letter.Subject("Reset your password")
//		})
//	}
type ResetEmailFunc = func(message ResetMessage, href string) mail.Message

// Email replaces the default reset email, a short plain-text message that says
// who asked, shows the link and when it expires. compose runs in the worker for
// every send, once per attempt, and must not keep the message: its Signed value
// is a credential. A nil compose restores the default. Notice emails (see
// [ResetMessage.Notice]) go through the same function, so a custom email must
// handle both wordings.
//
// Example:
//
//	reset.Email(func(message password.ResetMessage, href string) mail.Message {
//		return mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: message.Address}}}, mail.Body{Text: href})
//	})
func (reset *ResetBuilder) Email(compose ResetEmailFunc) *ResetBuilder {
	reset.email = compose

	return reset
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

// Registered adds a function that runs after [Verifier.RegisterHandler]
// committed a new user, for work that must follow a registration such as
// sending an email verification. The handler never logs the user in; it answers
// 202 and the user logs in afterwards. An error is logged and
// does not fail the request: the user is registered either way, and a mail
// outage must not turn into a failed signup. Hooks add up instead of replacing
// each other, so the verification the facade requests survives an application
// hook: they run in the order added, all of them, and their errors are logged
// together.
//
// Example:
//
//	passwords.Registered(func(ctx context.Context, subject, identifier string) error {
//		return directory.RequestVerification(ctx, subject)
//	})
func (passwords *Builder) Registered(hook func(ctx context.Context, subject, identifier string) error) *Builder {
	passwords.registered = web.Both(passwords.registered, hook)

	return passwords
}

// Taken adds a function that runs when [Verifier.RegisterHandler] meets an
// identifier that already belongs to a user, with the identifier as typed. It is
// where the owner is told that someone tried to register: dispatch the same kind
// of work as [Builder.Registered] does for a new user, so the two paths cost the
// same and the answer, 202 for both, reveals nothing. [Verifier.RequestReset]
// with [ResetRequest.Notice] set is the reset notice. An error is logged and
// does not change the answer. Hooks add up instead of replacing each other: they
// run in the order added, all of them, and their errors are logged together.
//
// Example:
//
//	passwords.Taken(func(ctx context.Context, identifier string) error {
//		return verifier.RequestReset(ctx, password.ResetRequest{Identifier: identifier, Notice: true})
//	})
func (passwords *Builder) Taken(hook func(ctx context.Context, identifier string) error) *Builder {
	passwords.taken = web.Then(passwords.taken, hook)

	return passwords
}

// AfterChange adds a function that runs in [Verifier.ChangeHandler] after the new
// password is stored and before the caller's other logins end, for what must
// not survive a password change, such as the user's device tokens. An error
// fails the request; the password has changed by then, so the client may retry.
// Hooks add up instead of replacing each other, so the token revocation the
// facade registers survives an application hook: they run in the order added,
// all of them even when one fails, and their errors are joined.
//
// Example:
//
//	passwords.AfterChange(issuer.RevokeDevices)
func (passwords *Builder) AfterChange(hook func(ctx context.Context, subject string) error) *Builder {
	passwords.changed = web.Then(passwords.changed, hook)

	return passwords
}

// validate checks the settings no setter can check alone.
func (passwords *Builder) validate(job queue.Job[ResetRequest]) error {
	if err := storage.ValidInstance(passwords.instance); err != nil {
		return err
	}
	if passwords.resetTTL < 0 {
		return errors.New("negative reset lifetime")
	}
	if !passwords.resetSet {
		return nil
	}
	if passwords.signer == nil || passwords.mailer == nil || passwords.dispatcher == nil {
		return errors.New("Reset needs a signer, a mailer and a queue dispatcher (Builder.Queue, or Builder.Queue of the iam facade)")
	}
	if page, err := url.Parse(passwords.resetURL); err != nil || !page.IsAbs() || page.Host == "" || page.Fragment != "" {
		return errors.New("Reset needs the reset page as an absolute URL without a fragment")
	}

	return job.Err()
}
