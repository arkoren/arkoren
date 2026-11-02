---
title: API Tokens
description: Accept bearer credentials and manage application-issued API keys with hashed storage and revocation.
order: 30
summary:
  - Connect a bearer verifier and reject presented invalid credentials without falling back.
  - Issue random API keys, store their hashes, and verify current key and account state.
  - Plan key revocation and understand how offline JWT verification changes that boundary.
---

# API Tokens

A program authenticates by sending a secret token with each request, rather
than signing in through a browser form. `auth.Bearer` extracts that token and
delegates verification to your application. It does not issue tokens, maintain
a token store, or implement a JWT verification policy.

If you already have a verifier, connect it directly. If your service issues its
own credentials, a random **API key** backed by a stored hash gives you a
per-key revocation point. For identity-provider JWTs, use
[OIDC Tokens](oidc-bearer.md) instead of building another verifier.

## Accept Bearer Credentials

Serve credential-bearing requests over HTTPS. Send the token in the header,
never in a URL, and keep it out of logs:

```http
GET /api/invoices HTTP/1.1
Authorization: Bearer ak_example_token
```

The value above illustrates the header, not a usable key. With your existing
router group `api` and `verifyKey func(context.Context, string) (User, error)`,
import `auth` from `arkoren.dev/core/http/auth` and register:

```go
api.Use(auth.Middleware(auth.Bearer(verifyKey)))
api.Use(auth.Require[User]())
```

`auth.Middleware` alone allows missing credentials; `Require` closes that path
for private routes. Your verifier must return the same exact `User` type used
by handlers and policies. A zero-valued user returned without an error still
counts as authenticated, so reject unknown or disabled accounts explicitly.

The bearer authenticator accepts a single `Authorization` header with a
case-insensitive `Bearer` scheme and a nonempty token containing no spaces.
No header returns `auth.ErrNoCredentials`. A malformed header is rejected before
verification; a verifier error normally produces `401` with
`WWW-Authenticate: Bearer error="invalid_token"`.

> [!WARNING]
> Never return `auth.ErrNoCredentials` for a presented invalid, expired, or revoked
> token. That error permits fallback or anonymous handling. Return a different
> error, such as `auth.ErrUnauthenticated`.

HTTP responses do not expose verifier error messages, but retain the error as
a cause. Database failures are also rendered as invalid-token responses through
this adapter; inspect controlled diagnostics to distinguish them from bad keys.
Do not put raw tokens or sensitive account details in those causes.

## Issue Keys

An API key is a bearer secret: anyone holding it can use its authority. Issue
it only after authenticating the owner and authorizing key creation. The owner
ID must come from that trusted identity, not from a submitted request field.
The application types below separate user input from the stored record:

```go
type User struct {
	ID int
}

type IssueKeyInput struct {
	Name string `json:"name"`
}

type APIKey struct {
	UserID  int
	Hash    []byte
	Name    string
	Revoked bool
}
```

With validated `in IssueKeyInput`, authenticated `owner User`, an operation
context `ctx`, and your application repository `keys`, generate a random key.
Import standard-library `crypto/rand` and `crypto/sha256`:

```go
token := "ak_" + rand.Text()
digest := sha256.Sum256([]byte(token))
err := keys.Save(ctx, APIKey{
	UserID: owner.ID,
	Hash:   digest[:],
	Name:   in.Name,
})
```

`rand.Text` provides at least 128 bits of randomness; the prefix only identifies
your key format. Store the hash, not the original token. Return the token once
only after `Save` succeeds, using an authenticated response that is not cached
or logged. It cannot be recovered from the stored record; lost keys need replacement.

A fast SHA-256 hash is suitable for a high-entropy random secret. It is not
appropriate for human-chosen passwords, which need
[password hashing](../hashing/overview.md). Hashed storage reduces exposure if
the key table leaks; it does not protect keys already stolen from clients or
exposed during issuance.

## Verify Stored Keys

Keep a unique lookup index on the stored hash. Your `keys.ByHash` application
method returns an `APIKey` or an error; absence must not be reported as
`auth.ErrNoCredentials`. Hash the entire presented token before looking it up:

```go
verifyKey := func(ctx context.Context, token string) (User, error) {
	digest := sha256.Sum256([]byte(token))
	stored, err := keys.ByHash(ctx, digest[:])
	if err != nil {
		return User{}, err
	}
	if stored.Revoked {
		return User{}, auth.ErrUnauthenticated
	}

	return loadActiveUser(ctx, stored.UserID)
}
```

`loadActiveUser` is your `func(context.Context, int) (User, error)` account lookup.
It returns an error for missing or disabled accounts, never a zero user with
nil error or `ErrNoCredentials`. If keys have expiry or granted permissions,
check expiry here and carry the permitted authority in a request-local identity;
[authorization](../authorization/overview.md) still decides which actions and
resources it allows.

`auth.Bearer` checks the header shape, not your key format. Very short tokens can
reach the verifier. If your format extracts a public lookup identifier or prefix,
validate lengths before slicing. When comparing raw secrets rather than looking
up a full digest, use `crypto/subtle.ConstantTimeCompare`, not ordinary equality.

## Revoke Keys

Delete a key record or mark it revoked, and ensure every verification checks
that state. The next uncached lookup rejects the key. If you cache verification
results, invalidate those entries too; otherwise cache lifetime delays revocation.
Already authenticated requests are not canceled by changing the record.

Let users name keys and revoke individual credentials without changing every
client. For replacement, issue a new key, move the client to it, then revoke the
old one. Scope key-management operations to the authenticated owner and apply
authorization separately; possession of an arbitrary key does not automatically
grant permission to manage other credentials.

## Choose Token Semantics

A signed JWT can carry identity claims without a per-request database lookup.
That saves shared-state access but changes the revocation boundary: a purely
offline verifier cannot observe an account deletion, permission change, or key
revocation until its verification policy makes the token unacceptable, commonly
at expiry. Short lifetimes reduce this window; a server-side denylist or account
lookup adds state back to verification.

If you choose JWTs, use a maintained verifier with an explicit trusted issuer,
intended audience, allowed algorithms and keys, expiry policy, and provider-specific
token-purpose checks. Decoding claims or checking a signature alone is not
authentication. Arkoren's core bearer adapter does not implement these checks or
provide a token endpoint, refresh flow, or authorization server.

## Separate Browser Routes

Prefer bearer-only API groups when scripts should not inherit browser session
authority. Such routes do not need CSRF protection for bearer authentication
because the credential is explicitly supplied, not automatically sent as a cookie.
Do not apply that exception to routes that also accept sessions.

If the same routes deliberately accept both credentials, configure the
[session and CSRF middleware](session-login.md#enable-login) and choose
[authenticator order](overview.md#resolve-credentials). Both authenticators must
return the same identity type; the first successful one wins even if the request
also carries another credential. For MCP, reuse the verifier and follow
[endpoint security](../mcp/securing-mcp.md) for protocol and tool-access rules.

For all functions and options, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth).
