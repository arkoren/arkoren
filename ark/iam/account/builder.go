package account

import (
	"errors"
	"net/url"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/queue"

	"arkoren.dev/ark/iam/internal/storage"
)

// Builder configures [NewDirectory] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft, and
// retaining it cannot change a published directory.
//
// Example:
//
//	directory, err := account.NewDirectory(db, store, clientIP, func(accounts *account.Builder) {
//		accounts.Instance("tenant_a").VerifyTTL(time.Hour)
//	})
type Builder struct {
	instance   string
	events     *event.Bus
	verifySet  bool
	signer     *link.Signer
	mailer     *mail.Mailer
	verifyURL  string
	email      VerifyEmailFunc
	dispatcher *queue.Dispatcher
	verifyTTL  time.Duration
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure account.BuilderFunc = func(accounts *account.Builder) {
//		accounts.Instance("tenant_a")
//	}
//	directory, err := account.NewDirectory(db, store, clientIP, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: tables are prefixed "<instance>_" and the
// migration group gains ":<instance>", so two directories can share one
// database. The name is 1 to 20 bytes of [a-z0-9_] starting with a letter;
// the default is the empty instance. Two instances without distinct names
// collide when their migrations are added.
//
// Example:
//
//	accounts.Instance("tenant_a")
func (accounts *Builder) Instance(name string) *Builder {
	accounts.instance = name

	return accounts
}

// Events publishes [UserCreated], [EmailVerified] and [UserDeleted] on bus, and
// the core [auth.UserForgotten] event of the provider. A nil bus publishes
// nothing.
//
// Example:
//
//	accounts.Events(events)
func (accounts *Builder) Events(bus *event.Bus) *Builder {
	accounts.events = bus

	return accounts
}

// Queue sets the dispatcher that stores the module's background work.
// [Builder.Verification] needs it: a registration and the resend route store one
// job each on it and return, and the worker that runs [Directory.RegisterJobs]
// sends the email. Through the facade, iam.Builder.Queue sets it for every
// package, so only code that builds a [Directory] by itself calls it. The jobs
// run on the queue "iam"; see [Directory.RegisterJobs]. A later call replaces the
// earlier one.
//
// Example:
//
//	accounts.Queue(dispatcher)
func (accounts *Builder) Queue(dispatcher *queue.Dispatcher) *Builder {
	accounts.dispatcher = dispatcher

	return accounts
}

// Verification turns email verification on: calling it makes
// [Directory.VerifyHandler] and [Directory.ResendHandler] work, and
// [Directory.SendVerification] and [Directory.RegisterJobs] exist for the
// worker. Verification is off when the method is never called; to switch it from
// configuration, wrap the call in an if. A later call replaces the earlier one.
// The package keeps no verification table and runs no goroutine; a verification
// link is a value that signer signs and that stops working by itself:
//
//   - It is bound to the user's current email and verified flag ([link.Bind]),
//     so changing the email, or verifying it by any means (a completed password
//     reset, say), invalidates every link sent before, and it expires after
//     [Builder.VerifyTTL].
//   - A registration of a new address and each resend store one job on the
//     [Builder.Queue] dispatcher and return. A failing dispatch on a resend
//     answers 500; on registration it is logged and the answer stays 202.
//   - The stored job later reaches [Directory.SendVerification] in a worker,
//     which signs the link and sends the email through mailer. Its error is
//     returned so the queue retries, which means one request may send more than
//     once.
//
// verifyURL is the page of the application that the link opens, for example
// "https://app.example.com/verify-email". The signed value travels in the URL
// fragment ([VerifyMessage.Link]), so the page reads it from location.hash and
// posts it to POST /email/verify. It must be an absolute URL without a
// fragment. The email is a plain-text message with a default subject and
// wording; configure may replace it with [VerificationBuilder.Email]. The
// callbacks run directly once, in order, on one fresh [VerificationBuilder].
//
// Signer, mailer, verifyURL and a [Builder.Queue] dispatcher are all required:
// [NewDirectory] fails with [ErrInvalidConfig] when any is missing or invalid.
// Use a signer whose key is reserved for links; a verification link is
// authenticated for its path, so the links of other features never verify here.
//
// Example:
//
//	accounts.Queue(dispatcher).Verification(signer, mailer, "https://app.example.com/verify-email", func(verification *account.VerificationBuilder) {
//		verification.Email(verifyEmail)
//	})
func (accounts *Builder) Verification(signer *link.Signer, mailer *mail.Mailer, verifyURL string, configure ...VerificationBuilderFunc) *Builder {
	accounts.verifySet = true
	accounts.signer = signer
	accounts.mailer = mailer
	accounts.verifyURL = verifyURL

	draft := VerificationBuilder{}
	for _, apply := range configure {
		apply(&draft)
	}

	accounts.email = draft.email

	return accounts
}

// VerificationBuilder configures the email of [Builder.Verification]. Callbacks
// run directly once, in order, on one fresh draft; methods return the same
// draft.
//
// Example:
//
//	accounts.Verification(signer, mailer, verifyURL, func(verification *account.VerificationBuilder) {
//		verification.Email(verifyEmail)
//	})
type VerificationBuilder struct {
	email VerifyEmailFunc
}

// VerificationBuilderFunc is the callback form of [VerificationBuilder].
//
// Example:
//
//	var configure account.VerificationBuilderFunc = func(verification *account.VerificationBuilder) {
//		verification.Email(verifyEmail)
//	}
//	accounts.Verification(signer, mailer, verifyURL, configure)
type VerificationBuilderFunc = func(*VerificationBuilder)

// VerifyEmailFunc writes the email of an email verification: message describes
// the request and href is the page link to put in the body, the application's
// verification URL with the signed value in its fragment. The result needs the
// recipient message.Address and a body; the sender comes from the mailer's
// defaults unless the message sets it. A message that fails validation is a
// permanent failure of the job, because retrying cannot fix it.
//
// Example:
//
//	var verifyEmail account.VerifyEmailFunc = func(message account.VerifyMessage, href string) mail.Message {
//		return mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: message.Address}}}, mail.Body{Text: href}, func(letter *mail.MessageBuilder) {
//			letter.Subject("Confirm your email address")
//		})
//	}
type VerifyEmailFunc = func(message VerifyMessage, href string) mail.Message

// Email replaces the default verification email, a short plain-text message that
// shows the link and when it expires. compose runs in the worker for every send,
// once per attempt, and must not keep the message: its Signed value is a
// credential. A nil compose restores the default.
//
// Example:
//
//	verification.Email(func(message account.VerifyMessage, href string) mail.Message {
//		return mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: message.Address}}}, mail.Body{Text: href})
//	})
func (verification *VerificationBuilder) Email(compose VerifyEmailFunc) *VerificationBuilder {
	verification.email = compose

	return verification
}

// VerifyTTL sets how long a verification link is valid; zero selects
// [DefaultVerifyTTL].
//
// Example:
//
//	accounts.VerifyTTL(48 * time.Hour)
func (accounts *Builder) VerifyTTL(ttl time.Duration) *Builder {
	accounts.verifyTTL = ttl

	return accounts
}

// validate checks the settings no setter can check alone.
func (accounts *Builder) validate(job queue.Job[VerifyRequest]) error {
	if err := storage.ValidInstance(accounts.instance); err != nil {
		return err
	}
	if accounts.verifyTTL < 0 {
		return errors.New("negative verification lifetime")
	}
	if !accounts.verifySet {
		return nil
	}
	if accounts.signer == nil || accounts.mailer == nil || accounts.dispatcher == nil {
		return errors.New("Verification needs a signer, a mailer and a queue dispatcher (Builder.Queue, or Builder.Queue of the iam facade)")
	}
	if page, err := url.Parse(accounts.verifyURL); err != nil || !page.IsAbs() || page.Host == "" || page.Fragment != "" {
		return errors.New("Verification needs the verification page as an absolute URL without a fragment")
	}

	return job.Err()
}
