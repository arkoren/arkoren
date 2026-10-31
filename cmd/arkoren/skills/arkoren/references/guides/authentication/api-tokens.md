---
title: API Tokens
description: Authenticate programs with bearer tokens, issue and store API keys safely, revoke them, and weigh self-contained tokens such as JWTs.
order: 30
---

# API Tokens

## Introduction

Programs do not fill in login forms. A script, a mobile app, or another service
proves who it is by sending a secret **token** with every request, in the
`Authorization` header:

```http
GET /invoices HTTP/1.1
Authorization: Bearer ak_PVBNIVKVQFQ7WTGJMC2GMVO2XQ
```

The `auth.Bearer` authenticator reads this header and passes the token to a
function you write, which checks it and returns the user it belongs to. What a
token is, and how it is checked, is up to you. This page covers the two common
designs: **API keys** that your service issues and looks up, and
**self-contained tokens** such as JWTs that carry their own proof. Tokens
issued by an identity provider are covered in
[OIDC Bearer Verification](oidc-bearer.md).

Accepting a credential and issuing one are separate tasks. If you already have
an API key, JWT, or OAuth access token, connect its verifier below; you do not
need to build a token-issuance endpoint. The Arkoren CLI likewise sends an
existing bearer credential, without acquiring or refreshing it. For MCP
transport setup and tool policy, see [Securing MCP](../mcp/securing-mcp.md).

## Accepting bearer tokens

Examples use `auth` from `arkoren.dev/core/http/auth`, `router` from
`arkoren.dev/core/http/router`, and the standard-library `context`,
`crypto/rand`, and `crypto/sha256` packages. `r` is your existing router;
`User`, `Keys`, and key persistence methods belong to your application.
Serve token-bearing requests only over HTTPS and keep tokens out of logs and URLs.

Give `auth.Bearer` a function that turns a token into a user, and add it to
the auth middleware:

```go
r.Use(auth.Middleware(auth.Bearer(keys.Verify)))
r.Use(auth.Require[User]())
```

Here `keys.Verify` returns your application's `User`. Use that exact type in
`auth.Require` and handlers; `User` and `*User` are not interchangeable.
`auth.Middleware` alone permits anonymous requests, so keep `auth.Require`
on routes that require credentials.

The authenticator handles the header for you, and decides the outcome before
and after your function runs:

- A request without an `Authorization` header has no credentials. The next
  authenticator tries, or the request continues anonymously.
- A header that is not a well-formed `Bearer` header is rejected with `401`,
  without calling your function.
- When your function returns an error other than `auth.ErrNoCredentials`, the
  request is rejected with `401`.
  The response carries a `WWW-Authenticate: Bearer error="invalid_token"`
  header, which tells clients that their token, not the request, is the
  problem.

The `401` response never includes your error's message, so it cannot leak why
a token failed. The error is attached to the response as its cause, so it
still appears in your [error logs](../http-fundamentals/error-handling.md).
Do not include the token in verifier error messages: attached causes can reach
those logs even though the HTTP response is redacted.

> [!WARNING]
> Do not return `auth.ErrNoCredentials` for an unknown or revoked token. It
> means "no credentials here", so the request would continue as anonymous
> instead of being rejected. Return any other error.

## API keys

An **API key** is a long random string that your service creates, shows to
the user once, and recognizes when it comes back. It is the simplest design to
build and to reason about: every request looks the key up, so revoking a key
takes effect immediately.

### Issuing a key

Generate the key with a cryptographically secure random source. `rand.Text`
from `crypto/rand` returns a string with 128 bits of randomness, enough that
nobody can guess a key. A short prefix makes your keys recognizable, for
example to secret scanners that look for leaked keys in public code. Here
`keys.Save` is your application's persistence method, and `Key` is its record
type containing the owner, hash, and display name:

```go
token := "ak_" + rand.Text()
sum := sha256.Sum256([]byte(token))

err := keys.Save(ctx, Key{UserID: user.ID, Hash: sum[:], Name: "CI deploy"})
```

Store only the hash of the key, never the key itself, and show the key to the
user once, right after creating it. If your database leaks, the hashes are of
no use to an attacker.

> [!NOTE]
> Passwords need a slow hash, because people choose short, guessable ones. A
> key from `rand.Text` cannot be guessed, so a fast SHA-256 hash is enough and
> keeps the lookup on every request cheap.

### Checking a key

To check a key, hash it the same way and look the hash up. A unique index on
the hash column keeps this to one fast query:

```go
func (keys *Keys) Verify(ctx context.Context, token string) (User, error) {
	sum := sha256.Sum256([]byte(token))

	return keys.userByKeyHash(ctx, sum[:])
}
```

Hashing the whole token needs no fixed-length slice. If your own key format
uses a lookup prefix or identifier, check the token's length and format before
extracting it. `auth.Bearer` checks the header's syntax, not your key format;
even a one-byte token can reach your verifier.

The application lookup should return `database.ErrNoRows` (from
`arkoren.dev/core/service/database`) for an unknown hash, which `auth.Bearer` turns into a
`401`. Any other failure, such as an unreachable database, is reported the same
way to the client, with the real cause in your logs.

Because the lookup compares hashes, not keys, its timing reveals nothing useful
about the key. Comparing raw secrets yourself is different: use
`subtle.ConstantTimeCompare`, never `==` or `bytes.Equal`, which return earlier
the sooner two values differ.

### Revoking a key

Delete the key's row, or mark it revoked and check the mark in `Verify`.
Because every request looks the key up, the next request with that key is
rejected. Storing when each key was last used, and a name the user chose,
helps people find and revoke keys they no longer need.

## Self-contained tokens

A **self-contained token**, usually a JSON Web Token (JWT), carries the user's
details and a signature over them. Your verify function checks the signature
and the expiry and builds the user from the token's claims, without a database
call. Use your chosen JWT library through an application verifier such as
`verifyJWT` below; this is not an Arkoren helper. It must validate the signature,
issuer, audience, and expiry before returning claims. Restrict accepted signing
algorithms and trusted keys in the verifier; do not trust the token's algorithm
header to select an unrestricted verification policy:

```go
r.Use(auth.Middleware(auth.Bearer(func(ctx context.Context, token string) (User, error) {
	claims, err := verifyJWT(token) // signature, issuer, audience, expiry
	if err != nil {
		return User{}, err
	}

	return User{ID: claims.UserID, Role: claims.Role}, nil
})))
```

This saves a lookup per request, and it lets other services check your tokens
without access to your database. The price is **revocation lag**: a token
cannot be taken back. It stays valid until it expires, even after the user is
deleted or their role changes. Keep token lifetimes short, a few minutes to an
hour, and issue new ones as needed. If you need instant revocation, use API
keys, or check a short list of revoked token IDs in the verify function.

## Mixing tokens and sessions

A service often has both a browser interface and an API. You can give the auth
middleware both authenticators, and each request is authenticated by
whichever credential it carries. These routes still need the
[session and CSRF middleware](session-login.md#setting-up). If both credentials
are present, the first successful authenticator wins:

```go
identity := auth.NewSessionIdentity(users.Load)
r.Use(auth.Middleware(
	identity.Authenticator(),
	auth.Bearer(keys.Verify),
))
```

Often it is cleaner to keep the browser routes and the API in separate groups,
with the session and CSRF middleware only on the browser group. For an API group
that accepts only explicit bearer credentials, authentication does not rely on
cookies the browser sends automatically. That group does not need CSRF protection
for bearer authentication; do not extend this exception to routes that also
accept session credentials:

```go
r.Group("/api", func(api *router.Router) {
	api.Use(auth.Middleware(auth.Bearer(keys.Verify)))
	api.With(auth.Require[User]()).Get("/invoices", invoices.list)
})
```

Both kinds of credential must produce the same user type, so that
`auth.Require` and your [authorization rules](../authorization/overview.md) work for
both. If keys carry extra information, such as the permissions granted to that
key, add a field for it to the user type.

## Testing

In tests, use an authenticator that accepts a fixed token, as shown in
[Auth tests](../testing/auth-tests.md#using-a-test-authenticator). To check
the authenticator built around your verify function, run that authenticator through
the shared suite described in
[Auth tests](../testing/auth-tests.md#checking-an-authenticator).

For every function and option, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth).
