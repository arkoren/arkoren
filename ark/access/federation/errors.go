package federation

import (
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"

	"arkoren.dev/ark/access/internal/storage"
)

// ErrNotMigrated is returned by [Links.Ping] when the table does not exist. Add
// [Links.Migrations] to the application's migrator and run them.
//
// Example:
//
//	if errors.Is(links.Ping(ctx), federation.ErrNotMigrated) {
//		// run the migrator
//	}
var ErrNotMigrated = storage.ErrNotMigrated

// ErrNotLinked is returned when an external identity belongs to no user and
// none may be created for it (no [Builder.Create] function, or a lookup that
// never creates). It is a problem with status 401.
//
// Example:
//
//	_, err := links.Lookup(ctx, "https://accounts.google.com", "1089")
//	if errors.Is(err, federation.ErrNotLinked) {
//		// ask the user to log in and connect the provider
//	}
var ErrNotLinked = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:federation:not-linked")
	details.Title("Account not linked")
	details.Detail("No account is connected to this identity. Log in and connect the provider from your settings.")
	details.Status(http.StatusUnauthorized)
})

// ErrLinkedElsewhere is returned when an external identity is already connected
// to another user. It is a problem with status 409.
//
// Example:
//
//	if errors.Is(err, federation.ErrLinkedElsewhere) {
//		// the identity belongs to another account
//	}
var ErrLinkedElsewhere = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:federation:linked-elsewhere")
	details.Title("Identity already connected")
	details.Detail("This identity is already connected to another account.")
	details.Status(http.StatusConflict)
})

// ErrAlreadyLinked is returned when the user already has a different account
// connected from the same issuer: a user holds one connection per issuer. It is
// a problem with status 409.
//
// Example:
//
//	if errors.Is(err, federation.ErrAlreadyLinked) {
//		// disconnect the current one first
//	}
var ErrAlreadyLinked = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:federation:already-linked")
	details.Title("Provider already connected")
	details.Detail("Another account from this provider is already connected. Disconnect it first.")
	details.Status(http.StatusConflict)
})

// ErrLastLink is returned when disconnecting would leave the user no way to log
// in: it is their only connection and [Builder.Fallback] reports no other
// method. It is a problem with status 409.
//
// Example:
//
//	if errors.Is(err, federation.ErrLastLink) {
//		// ask the user to set a password first
//	}
var ErrLastLink = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:federation:last-link")
	details.Title("Last way to log in")
	details.Detail("Set a password or connect another provider before disconnecting this one.")
	details.Status(http.StatusConflict)
})

// ErrNotFound is returned when a connection to remove does not exist or belongs
// to another user; the two look alike. It is a problem with status 404.
//
// Example:
//
//	if errors.Is(err, federation.ErrNotFound) {
//		// no such connection
//	}
var ErrNotFound = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:federation:not-found")
	details.Title("Connection not found")
	details.Detail("No such connection.")
	details.Status(http.StatusNotFound)
})

// ErrUnknownProvider is returned by the connect handler for a provider that is
// not mounted. It is a problem with status 404.
//
// Example:
//
//	if errors.Is(err, federation.ErrUnknownProvider) {
//		// no such provider
//	}
var ErrUnknownProvider = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:federation:unknown-provider")
	details.Title("Unknown provider")
	details.Detail("No such sign-in provider.")
	details.Status(http.StatusNotFound)
})

// ErrInvalidConfig is wrapped by the errors of [NewLinks] for an unusable
// configuration.
//
// Example:
//
//	_, err := federation.NewLinks(nil, nil)
//	fmt.Println(errors.Is(err, federation.ErrInvalidConfig)) // true
var ErrInvalidConfig = errors.New("federation: invalid configuration")
