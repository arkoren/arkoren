package apitoken

import (
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"

	"arkoren.dev/ark/access/internal/storage"
)

// ErrNotMigrated is returned by [Issuer.Ping] when the table does not exist.
// Add [Issuer.Migrations] to the application's migrator and run them.
//
// Example:
//
//	if errors.Is(issuer.Ping(ctx), apitoken.ErrNotMigrated) {
//		// run the migrator
//	}
var ErrNotMigrated = storage.ErrNotMigrated

// ErrInvalidRequest is returned when a token request is invalid: an empty or
// overlong name, a scope outside the catalog set with [Builder.Grants], or an
// expiry in the past or beyond [Builder.MaxTTL]. It is a problem with status
// 422.
//
// Example:
//
//	_, _, err := issuer.Issue(ctx, subject, apitoken.Request{Name: ""})
//	fmt.Println(errors.Is(err, apitoken.ErrInvalidRequest)) // true
var ErrInvalidRequest = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:apitoken:invalid-request")
	details.Title("Invalid token request")
	details.Detail("The token name, scopes or expiry are not valid.")
	details.Status(http.StatusUnprocessableEntity)
})

// ErrScopeNotGranted is returned when a token request names a scope of the
// catalog that the user may not hold: the grant function of [Builder.Grants]
// does not list it. It is a problem with status 403, distinct from
// [ErrInvalidRequest] (422), which is a scope the catalog does not know. The
// detail does not name the scope.
//
// Example:
//
//	_, _, err := issuer.Issue(ctx, subject, apitoken.Request{Name: "CI", Scopes: []string{"users:write"}})
//	fmt.Println(errors.Is(err, apitoken.ErrScopeNotGranted)) // true for a user who may not write users
var ErrScopeNotGranted = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:apitoken:scope-not-granted")
	details.Title("Scope not granted")
	details.Detail("You may not grant a token a scope you do not hold.")
	details.Status(http.StatusForbidden)
})

// ErrNotFound is returned when a token to revoke does not exist or belongs to
// another user; the two look alike. It is a problem with status 404.
//
// Example:
//
//	if errors.Is(err, apitoken.ErrNotFound) {
//		// the token does not exist or is not the caller's
//	}
var ErrNotFound = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:apitoken:not-found")
	details.Title("Token not found")
	details.Detail("No such token.")
	details.Status(http.StatusNotFound)
})

// ErrInvalidConfig is wrapped by the errors of [NewIssuer] for an unusable
// configuration.
//
// Example:
//
//	_, err := apitoken.NewIssuer(nil, nil, nil)
//	fmt.Println(errors.Is(err, apitoken.ErrInvalidConfig)) // true
var ErrInvalidConfig = errors.New("apitoken: invalid configuration")
