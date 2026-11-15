package account

import (
	"context"
	"errors"
	"net/http"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"

	"arkoren.dev/ark/access/internal/storage"
)

// Builder configures [NewDirectory] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft, and
// retaining it cannot change a published directory.
//
// Example:
//
//	directory, err := account.NewDirectory(db, func(accounts *account.Builder) {
//		accounts.Instance("tenant_a").VerifyTTL(time.Hour)
//	})
type Builder struct {
	instance  string
	events    *event.Bus
	deliver   func(context.Context, Message) error
	verifyTTL time.Duration
	store     *cache.Cache
	clientIP  func(*http.Request) string
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure account.BuilderFunc = func(accounts *account.Builder) {
//		accounts.Instance("tenant_a")
//	}
//	directory, err := account.NewDirectory(db, configure)
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

// Deliver sets the function that sends verification links. It receives the
// [Message] to put in an email; the token in it is a secret and must never be
// logged. Without it, [Directory.SendVerification] returns [ErrNoDelivery] and
// registration still works but sends nothing.
//
// Example:
//
//	accounts.Deliver(func(ctx context.Context, message account.Message) error {
//		return mailer.Send(ctx, message.Email, message.Link("https://app.example.com/verify"))
//	})
func (accounts *Builder) Deliver(deliver func(ctx context.Context, message Message) error) *Builder {
	accounts.deliver = deliver

	return accounts
}

// Throttle sets the cache that counts verification resends and how the client
// address is found. [Directory.ResendHandler] needs it and panics without it,
// because an unlimited resend route lets one user flood a mailbox. A nil
// clientIP uses the host of r.RemoteAddr, which behind a reverse proxy is the
// proxy: pass the function given to the guard, such as ratelimit.TrustedProxyKey.
//
// Example:
//
//	accounts.Throttle(store, ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
func (accounts *Builder) Throttle(store *cache.Cache, clientIP func(*http.Request) string) *Builder {
	accounts.store = store
	accounts.clientIP = clientIP

	return accounts
}

// VerifyTTL sets how long a verification token is valid; zero selects
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

	return nil
}
