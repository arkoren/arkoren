---
title: Session Login
description: Check application-owned credentials, rotate the browser session, and revoke account sessions.
order: 20
summary:
  - Report each user's identifier, load current accounts from it, and reject deleted or disabled accounts.
  - Check passwords without revealing account existence and rotate the session after verified login.
  - End local logins, revoke older account sessions, and select the intended session context.
---

# Session Login

A browser login checks credentials once, then stores an account identifier in
the session. On later requests, `auth.SessionIdentity` loads the current account
from that identifier. You own the accounts, password checks, and account-status
policy; [Sessions](../http-capabilities/sessions.md#enable-sessions) owns cookie
configuration, storage, and response-time persistence.

The examples assume an existing router `web`, session store `store`, configured
[`hash.Hasher`](../hashing/overview.md), and application repository `accounts`.
Import `auth`, `session`, and `response` from their `arkoren.dev/core/http/`
packages, `csrf` from `arkoren.dev/core/http/middleware/csrf`, and standard-library
`context`, `errors`, and `net/http` where used.

## Load Accounts

Keep the session small: store an account ID, not a snapshot of permissions.
The loader can then reject a deleted or disabled account on the next request.
This application separates the handler identity from its password record. The
user reports its identifier through `AuthID`, which `Login` stores and the
loader below accepts:

```go
type User struct {
	ID   int
	Name string
}

func (user User) AuthID() int { return user.ID }

type Account struct {
	User
	PasswordHash   []byte
	Disabled       bool
	SessionVersion int
}
```

Here `accounts.ByID(ctx, id)` returns `Account`; the application sentinel
`ErrAccountNotFound` means absence, while other errors retain storage failures.
Bind the loader to the identifier type `int`. The compiler checks that the
identifier `User.AuthID` returns is the one the loader accepts:

```go
loadUser := func(ctx context.Context, id int) (User, error) {
	account, err := accounts.ByID(ctx, id)
	if errors.Is(err, ErrAccountNotFound) {
		return User{}, auth.ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if account.Disabled {
		return User{}, auth.ErrNoCredentials
	}

	return account.User, nil
}
```

The loader runs on every request with a usable stored identifier. Absence and
disabled status become anonymous requests; `auth.ErrNotFound` wraps
`auth.ErrNoCredentials`, so both are anonymous, but only `ErrNotFound` tells code
that cleans up after deleted users that the account is gone. Return plain
`ErrNoCredentials` for a disabled account so its data is never swept. A storage outage instead stops the
request. Returning a zero user with nil error would authenticate it, so do not
use that result for absence. If you cache accounts, your invalidation policy
determines how quickly account changes take effect.

## Enable Login

Construct one identity definition and share it between middleware and handlers:

```go
identity := auth.NewSessionIdentity(loadUser)
web.Use(session.Middleware(store))
web.Use(csrf.Middleware())
web.Use(auth.Middleware(identity.Authenticator()))
```

Session middleware must run first. Keep [CSRF protection](../browser-security/csrf-protection.md)
on login, logout, and other state-changing browser routes: browsers send session
cookies automatically, including on requests another site can initiate. Add
`auth.Require[User]()` only to routes requiring a signed-in account, not to the
login route itself.

Construction does not call the loader. Check `identity.Err()` during setup
when configuration is assembled dynamically; a zero definition or nil loader
returns `auth.ErrInvalidSessionIdentity`, not an anonymous result. Missing
sessions, missing identity fields, and incompatible stored types are anonymous.
Persisted JSON that remains compatible can still decode after an identifier
type change; a type change is not a revocation mechanism.

## Check Passwords

Start with [validated request input](../http-fundamentals/requests.md), not an
arbitrary email or account ID from a header. A login request has only the fields
needed to check credentials:

```go
type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
```

Use the same rejection for an unknown email, a wrong password, and a disabled
account. Otherwise login becomes an account-enumeration endpoint. The following
fragment runs in your login handler with validated `in LoginInput` and a
configured `hasher *hash.Hasher`. `accounts.ByEmail` returns the same application
absence sentinel as `ByID`:

```go
account, err := accounts.ByEmail(r.Context(), in.Email)
if err != nil && !errors.Is(err, ErrAccountNotFound) {
	return err
}

found := err == nil
storedHash := dummyHash
if found {
	storedHash = account.PasswordHash
}
```

`dummyHash` is a hash of a non-secret placeholder, computed once at startup
with the same hasher and cost as real passwords. Check it on the missing-account
path so that path does not skip the deliberately expensive password check:

```go
matches, err := hasher.Check(in.Password, storedHash)
if err != nil {
	return err
}
if !found || !matches || account.Disabled {
	return auth.ErrUnauthenticated
}
```

The explicit `!found` check prevents a password matching the dummy hash from
creating a login. This reduces a timing difference; it does not guarantee equal
response times. Preserve storage and hash-check errors rather than classifying
them as bad passwords, and keep passwords and hashes out of error messages.
Apply [login rate limits](../abuse-prevention/rate-limiting.md) before credential
checks to limit guessing and hashing work.

Create password hashes at registration using the [hashing guide](../hashing/overview.md).
Keep the dummy hash aligned with [cost changes](../hashing/cost.md). For old
hashes, follow [hash upgrades](../hashing/upgrading.md), including its conditional
write that avoids overwriting a concurrent password reset.

## Start Sessions

After the credential checks succeed, resolve the identity's selected session and
store the verified user's identifier. Do both before writing response headers:

```go
current, err := identity.Session(r.Context())
if err != nil {
	return err
}
if err := identity.Login(current, account.User); err != nil {
	return err
}

return response.Status(w, http.StatusNoContent)
```

`Login` writes the identifier and then regenerates the session ID. Rotation
protects against **session fixation**, where an attacker knows an ID planted
before the victim signs in. If the field write fails, no rotation occurs and
you must not report success. Login preserves other session values, creation
time, and expiry; configure idle and absolute limits in
[session lifetimes](../http-capabilities/sessions.md#set-lifetimes).

This call changes the request's session, not persistent storage. On a non-5xx
response, session middleware saves changes and retires the old ID before
committing the response. Failed persistence or retirement can leave an old ID
usable; configure and monitor the middleware's error handling rather than
treating `Login` success as proof of durable revocation. See
[Save Changes](../http-capabilities/sessions.md#save-changes) for the commit and
concurrency boundaries.

## End Sessions

Mount logout as a CSRF-protected `POST`, not a `GET`. Resolve the same identity's
session and remove its login before sending a response:

```go
current, err := identity.Session(r.Context())
if err != nil {
	return err
}
if err := identity.Logout(current); err != nil {
	return err
}

return response.Status(w, http.StatusNoContent)
```

Logout removes even a stale, wrongly typed identifier and rotates the ID after
successful deletion. Other values, such as a language preference, remain.
Use `current.Destroy()` instead when the whole session must end. Both paths
still depend on response-time persistence; a failed retirement can leave the
old ID valid.

## Revoke Account Sessions

To end sessions across devices after a password change or compromise, store an
account version with the ID. This is an alternative identity definition, not
an additional identity entry:

```go
type SessionUser struct {
	ID      int
	Version int
}
```

Move `SessionVersion` from `Account` to the embedded `User` (the loader can still
read it as `account.SessionVersion`) and report it from `AuthID`, which now
returns `SessionUser`:

```go
func (user User) AuthID() SessionUser {
	return SessionUser{ID: user.ID, Version: user.SessionVersion}
}
```

Change your loader to `func(context.Context, SessionUser) (User, error)`.
After loading the account and handling absence or storage errors as above,
reject a stale version:

```go
if account.Disabled || account.SessionVersion != saved.Version {
	return User{}, auth.ErrNoCredentials
}
return account.User, nil
```

Here `saved` is the loader's `SessionUser` argument. Reconstruct the definition
with that loader; `Login` now stores both values from the user after successful
credential checks:

```go
if err := identity.Login(current, account.User); err != nil {
	return err
}
```

A struct identifier works for sessions but cannot be converted to a subject text
reference, which packages that attach data to users require; see
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth).

Increment the persisted `SessionVersion` to invalidate older versions on each
device's next account lookup. No session enumeration is required. Existing
in-flight requests are not retroactively canceled, and account caching can
delay the effect.

## Select Sessions

If you configured a nondefault session context slot, share one handle between
the middleware and the identity:

```go
handle := session.NewHandle()
identity := auth.NewSessionIdentity(loadUser, func(binding *auth.SessionBuilder) {
	binding.Handle(handle)
})
web.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.Handle(handle)
}))
```

Register CSRF and authentication after this session middleware as before.
`identity.Session(ctx)` selects only this slot and returns `session.ErrNoSession`
for an absent or nil session; it never falls back to the default slot. A zero
handle selects the default. Explicit `Login` and `Logout` arguments are not
checked against the handle, so use `identity.Session` to select them consistently.

A handle is not cookie or storage isolation. Definitions writing to the same
physical session share `arkoren:auth:user`: the last login replaces the identity,
and logout removes it. Independent browser sessions also need distinct cookie
and storage configuration in [Sessions](../http-capabilities/sessions.md).
For authentication tests, combine [direct identity tests](../testing/auth-tests.md)
with a real cookie-based login/logout test that exercises persistence.

For all functions and options, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth).
