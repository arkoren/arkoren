// Package apitoken issues opaque, hashed, revocable bearer tokens to users:
// personal access tokens (named, scoped, optionally expiring, shown once) and
// device tokens for first-party mobile and desktop apps (issued when a login is
// exchanged, revoked by the device's own logout, and ended after
// [DefaultDeviceIdleTTL] of disuse). It works with any user store:
// it only needs an [auth.UserProvider].
//
// # Model
//
// A token's text is "<prefix>_<id>_<secret>". The id is a public lookup key and
// the secret carries 130 random bits; only the SHA-256 hex of the secret is
// stored, so a database leak does not reveal usable tokens. Each token is bound
// to the user's [auth.UserRecord.Version] at issue time: a password reset, or
// any change of the version, ends the user's tokens with no extra wiring, while
// a password change (which keeps the version) does not.
//
// # What a token can do
//
// A token authenticates API requests and carries its scopes (check them with
// gate.HasScopes). It never carries factors and is never a browser session
// ([auth.Identity.Session] is false), so [auth.RequireLevel] and
// [auth.Guard.RequireFresh] reject it and every credential handler of this
// module refuses it: a stolen token cannot enroll a second factor, change a
// password or mint more tokens. Mount the credential routes on a router with
// the guard's authenticator and the API routes on one with [Issuer.Authenticator],
// or on one router with both; the handlers enforce the boundary either way.
//
// # Scopes and the grant ceiling
//
// [Builder.Grants] takes the scope catalog and a function that says which of
// them a user may hold. A personal token may carry any subset of that ceiling
// ([ErrScopeNotGranted] otherwise, [ErrInvalidRequest] for a scope outside the
// catalog) and a device token carries all of it. When a token authenticates, its
// stored scopes are narrowed again to what the user may hold then, so a demoted
// user loses the scope on the next request ([Builder.GrantTTL] zero), within a
// cache lifetime (positive) or never (negative: the scopes are fixed at
// creation). An error from the grant function or the cache fails the request.
// [AnyScope] grants the whole catalog and is right only when scopes never
// confer more than the user has. Without Grants tokens carry no scopes.
// [Builder.PersonalTokens] and [Builder.DeviceTokens] switch the two kinds off.
//
// # Mobile login
//
// A device keeps a cookie jar for the login only: POST /login (and the second
// factor, if any), then POST /device-tokens ([Issuer.ExchangeHandler]) trades
// the fresh login for a token and ends the session. Every later call sends
// "Authorization: Bearer ...", and DELETE /device-tokens/current logs out.
//
// # Storage
//
// The package never runs DDL. [Issuer.Migrations] describes the table
// "api_tokens" (prefixed "<instance>_" for a named [Builder.Instance]) in the
// group "arkoren.dev:ark/access:apitoken", and the application's migrator
// executes it. Every package of the module assumes the same
// [database.Database]; writes that span packages join the transaction carried
// by the context. Tokens and secrets never appear in events or logs.
//
// Example:
//
//	issuer, err := apitoken.NewIssuer(provider, db, store, func(tokens *apitoken.Builder) {
//		tokens.Grants(apitoken.AnyScope, "posts:read", "posts:write")
//	})
//	if err != nil {
//		return err
//	}
//	api := routes.With(auth.Middleware(issuer.Authenticator()), auth.Require())
//	api.With(gate.Require(gate.HasScopes("posts:write"))).Post("/api/posts", createPost)
package apitoken
