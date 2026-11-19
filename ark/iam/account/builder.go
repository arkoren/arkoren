package account

import (
	"context"
	"errors"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"

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
	instance  string
	events    *event.Bus
	verifySet bool
	signer    *link.Signer
	dispatch  func(context.Context, VerifyRequest) error
	deliver   func(context.Context, VerifyMessage) error
	verifyTTL time.Duration
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

// Verification turns email verification on: calling it makes
// [Directory.VerifyHandler] and [Directory.ResendHandler] work, and
// [Directory.SendVerification] exists for the worker. Verification is off when
// the method is never called; to switch it from configuration, wrap the call in
// an if. A later call replaces the earlier one. The package keeps no verification table and runs no goroutine; a
// verification link is a value that signer signs and that stops working by
// itself:
//
//   - It is bound to the user's current email and verified flag ([link.Bind]),
//     so changing the email, or verifying it by any means (a completed password
//     reset, say), invalidates every link sent before, and it expires after
//     [Builder.VerifyTTL].
//   - dispatch runs in the request, for a registration of a new address and for
//     each resend. It must store the work durably (a queue job, a workflow run,
//     an outbox row) and return, or return an error. A failing dispatch on a
//     resend answers 500; on registration it is logged and the answer stays 202.
//   - The stored work later reaches [Directory.SendVerification] in a worker,
//     which signs the link and calls deliver with the [VerifyMessage] to send.
//     deliver renders and sends the email; its error is returned by
//     SendVerification so the queue retries, which means deliver may run more
//     than once for one request.
//
// Signer, dispatch and deliver are all required:
// [NewDirectory] fails with [ErrInvalidConfig] when any is nil. Use a signer whose key is reserved for
// links; a verification link is authenticated for its path, so the links of
// other features never verify here.
//
// Example:
//
//	accounts.Verification(signer, func(ctx context.Context, request account.VerifyRequest) error {
//		return dispatcher.Dispatch(ctx, verifyJob, request)
//	}, func(ctx context.Context, message account.VerifyMessage) error {
//		return mailer.Send(ctx, verifyEmail(message.Address, message.Link("https://app.example.com/verify-email")))
//	})
func (accounts *Builder) Verification(signer *link.Signer, dispatch func(ctx context.Context, request VerifyRequest) error, deliver func(ctx context.Context, message VerifyMessage) error) *Builder {
	accounts.verifySet = true
	accounts.signer, accounts.dispatch, accounts.deliver = signer, dispatch, deliver

	return accounts
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
func (accounts *Builder) validate() error {
	if err := storage.ValidInstance(accounts.instance); err != nil {
		return err
	}
	if accounts.verifyTTL < 0 {
		return errors.New("negative verification lifetime")
	}
	if accounts.verifySet && (accounts.signer == nil || accounts.dispatch == nil || accounts.deliver == nil) {
		return errors.New("Verification needs a signer, a dispatch function and a deliver function")
	}

	return nil
}
