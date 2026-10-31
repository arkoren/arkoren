---
title: Session Login
description: Sign people in from a browser with an email and password, keep them signed in with the session, sign them out, and handle stolen sessions.
order: 20
---

# Session Login

## Introduction

The classic way to sign in to a website is a form with an email and a
password. After the password is checked, the server remembers the person in a
**session**, and the browser sends the session cookie on every following
request. Arkoren builds this from three parts you already have: the
[session middleware](../http-capabilities/sessions.md) keeps the session, the
[hasher](../hashing/overview.md) checks the password, and the `auth` package stores the
user's identifier in the session and uses your loader to resolve the current user
on later requests.

This page walks through the whole flow: the setup, the login and logout
handlers, and the cases that need care, such as an account deleted while its
session is still alive.

## Setting up

Examples import `auth`, `request`, `response`, `router`, and `session` from
their `arkoren.dev/core/http/` packages, `csrf` from
`arkoren.dev/core/http/middleware/csrf`, `database` from
`arkoren.dev/core/service/database`, and `query` from `arkoren.dev/foundation/query`.
`context`, `errors`, `log/slog`, and `net/http` are standard-library packages.
`User` is your application's account type; the following tasks add fields such
as `PasswordHash []byte` and `SessionVersion int` when needed.

You need a [session store](../http-capabilities/sessions.md), named `store` below, and an
application user lookup, `users.Load`, described in the next section. Register
one shared identity definition, then the session middleware, CSRF protection,
and auth middleware in that order:

```go
identity := auth.NewSessionIdentity(users.Load)
r.Use(session.Middleware(store))
r.Use(csrf.Middleware())
r.Use(auth.Middleware(identity.Authenticator()))
```

Keep `identity` in your application's account service so login and logout
handlers use the same definition as middleware. Below, `users.identity` is that
`auth.SessionIdentity[int, User]` value. Construction does not call the loader.
Check `identity.Err()` during setup if the loader is assembled dynamically:
a zero definition or nil loader produces plain `auth.ErrInvalidSessionIdentity`.
Every operation checks that configuration first, even on an anonymous request.

The session cookie is sent by the browser automatically, also on requests that
another site triggers. CSRF protection makes sure that such a forged request
cannot act as the signed-in user, so it is required whenever you sign people
in with a session. [CSRF protection](../browser-security/csrf-protection.md) explains how it
works.

## Custom session store

If your application selects a nondefault session context slot, allocate one
opaque handle and share it between session middleware, the identity definition,
and request lookup. Independent browser sessions also require distinct cookies
and appropriate storage settings; a handle provides neither isolation nor
authorization. With the store and loader from the setup above:

```go
applicationSession := session.NewHandle()
identity := auth.NewSessionIdentity(users.Load, func(binding *auth.SessionBuilder) {
	binding.Handle(applicationSession)
})
r.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.Handle(applicationSession)
}))
r.Use(csrf.Middleware())
r.Use(auth.Middleware(identity.Authenticator()))
```

After verifying credentials in a handler, resolve that same slot and check both
lookup and mutation before writing the response:

```go
current, ok := request.Session(r, request.WithSessionHandle(applicationSession))
if !ok {
	return session.ErrNoSession
}
if err := identity.Login(current, user.ID); err != nil {
	return err
}
```

Copies of a handle select the same slot; separate `NewHandle` calls select
different slots. A later zero `session.Handle{}` restores default lookup.
`identity.Session(ctx)` resolves only its selected slot and returns
`session.ErrNoSession` when absent, including a stored nil pointer; it never
falls back to the default slot. Explicit `Login` and `Logout` arguments are not
checked against the handle, so callers must pass the intended session. All
definitions writing to the same physical session still share its identity entry.

## Loading the user

`identity.Authenticator()` reads the user's identifier from the session and passes it to
your load function, which returns the user. The identifier's type comes from
the function, here an `int`. This application's `Users` store holds its database
in `users.db`; a missing row must become `auth.ErrNoCredentials`:

```go
func (users *Users) Load(ctx context.Context, id int) (User, error) {
	user, err := users.db.Find[User](ctx, query.Table("users", func(statement *query.Builder) {
		statement.Where("id", id)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return User{}, auth.ErrNoCredentials
	}

	return user, err
}
```

The load function runs on every request that has a signed-in session, so keep
it to one indexed lookup.

### Deleted accounts

A session can outlive the account it belongs to: an administrator deletes a
user who still has a browser tab open. Returning `auth.ErrNoCredentials` when
the user no longer exists, as above, turns the request into an anonymous one.
Protected routes then return `401`, or your application's login redirect if you
configured one, rather than a `500`. Treat disabled accounts the same way. Any
other error, such as a database outage, is returned as is and stops the request.

If the session has no identifier, an in-memory identifier has the wrong Go type,
or a persisted JSON identifier cannot decode as the loader's identifier type,
the request is anonymous too. The shared `SessionIdentity[K, U]` definition binds
the identifier accepted by `Login` to the loader's identifier type `K`.
Compatible persisted JSON can still decode
after a type change, including changes between named numeric types, so session
authentication does not detect every type or schema change.

## Logging in

The login handler must validate the input and check the credentials before
calling `users.identity.Login`. Here `credentials` is your email-and-password
[validation schema](../validation/overview.md). On its input type, tag `Email`
with `json:"email"` and `Password` with `json:"password"`: JSON v2 matches names
case-sensitively, so untagged fields do not read those lower-case JSON members.
`users.authenticate` is the application method shown in the next section.
Keep both error checks: failed validation or authentication must stop the login:

```go
func (users *Users) login(w http.ResponseWriter, r *http.Request) error {
	in, err := request.ParseJSON(r, credentials)
	if err != nil {
		return err
	}

	user, err := users.authenticate(r.Context(), in.Email, in.Password)
	if err != nil {
		return err
	}

	if err := users.identity.Login(request.MustSession(r), user.ID); err != nil {
		return err
	}

	return response.Status(w, http.StatusNoContent)
}
```

`identity.Login` stores the identifier in the request's session and gives it a new
ID only after the field write succeeds; return any error before reporting
success. Rotation protects against **session fixation**, an attack in which someone
plants a session ID they know in the victim's browser and waits for the victim
to sign in with it.

Call `Login` before writing response headers, as above. The session middleware
saves the new record and retires the old ID before committing a non-5xx response;
`Login` itself does no persistence I/O. A save or retirement failure can leave
the old session valid, so monitor the session middleware's persistence errors.
See [session persistence](../http-capabilities/sessions.md) for failure handling.

### Checking the password

Passwords are never stored, only their hashes. At sign-up, hash the password
with the [hasher](../hashing/overview.md) and store the result. At login, find the user
by email and check the password the person typed against the stored hash.
Here `users.byEmail` returns `database.ErrNoRows` for an unknown email and
preserves other database errors. `users.dummyHash` is a hash of a non-secret
placeholder, computed once at startup with the same hasher and cost as real
passwords. Check it when the email is unknown so that path still performs a
password check:

```go
func (users *Users) authenticate(ctx context.Context, email, password string) (User, error) {
	user, err := users.byEmail(ctx, email)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return User{}, err
	}

	found := err == nil
	hash := users.dummyHash
	if found {
		hash = user.PasswordHash
	}

	ok, err := users.hasher.Check(password, hash)
	if err != nil {
		return User{}, err
	}
	if !found || !ok {
		return User{}, auth.ErrUnauthenticated
	}

	return user, nil
}
```

An unknown email and a wrong password both return `auth.ErrUnauthenticated`,
so clients see the same `401`. A different message for each case would tell an
attacker which email addresses have accounts. Database outages and hash-check
errors are not bad credentials: preserve those errors instead of replacing
them with `auth.ErrUnauthenticated`.

### Hiding which accounts exist

The message is not the only thing that can give an account away. Checking a
password hash is deliberately slow, so skipping it for an unknown email can
make those requests noticeably faster. The dummy check above reduces this
timing difference; it does not guarantee identical request times. Keep the
dummy hash's cost aligned when you change password-hashing settings, and keep
the explicit `!found` rejection even if the submitted password happens to
match the dummy hash.

### Upgrading old hashes

Hashing settings get stronger over time. When you raise them, existing hashes
keep working, and the moment a person logs in is the only time you have their
password to hash again. After a successful check, ask the hasher whether the
stored hash uses old settings.

Make the application-owned `savePasswordHash` method accept the original hash
and update only if it still matches. Otherwise a concurrent password reset can
be overwritten with a hash of the old password. A failed conditional match
leaves the newer password unchanged.

```go
if users.hasher.NeedsRehash(user.PasswordHash) {
	fresh, err := users.hasher.Hash(in.Password)
	if err == nil {
		err = users.savePasswordHash(r.Context(), user.ID, user.PasswordHash, fresh)
	}
	if err != nil {
		slog.WarnContext(r.Context(), "rehash password", "user", user.ID, "error", err)
	}
}
```

This fragment runs after credential verification. It logs a rehash error and
continues login; the old hash remains usable and the next login can retry.

### Slowing down guessing

A login form is the natural target of password guessing. Put a strict
[rate limit](../abuse-prevention/rate-limiting.md) on the login route, keyed
by the client address, so that each client can only make a few attempts per
minute.

## Logging out

`identity.Logout` removes the user from the session and gives the session a new
ID:

```go
if err := users.identity.Logout(request.MustSession(r)); err != nil {
	return err
}
```

Logout rotates the ID only after successfully removing the identity. Other
values in the session, such as a language preference or a shopping cart,
stay. To clear everything, call `Destroy` on the session instead, before writing
response headers. On a non-5xx response, the middleware expires the browser's
cookie and replaces the old record with a tombstone, rather than physically
deleting it. If retirement fails, the old ID can remain valid; monitor the same
persistence errors as for login.

Register logout as a `POST` route. A `GET` logout can be triggered by any image
tag on another site, and CSRF protection only checks requests that change
state.

## Staying signed in

A signed-in session lasts as long as the session itself, which is set by the
session middleware's idle timeout and lifetime. For a "remember me" option,
the usual approach is a longer session lifetime for everyone, rather than a
second cookie: it keeps a single mechanism to secure and revoke.
[Sessions](../http-capabilities/sessions.md#expiry) explains both settings.

## Signing out everywhere

Sessions live in the cache, keyed by an ID only the browser knows, so there is
no list of a user's sessions to delete when a password is changed or an
account is compromised. A version number on the user solves this. The
identifier accepted by `NewSessionIdentity` can be any type that encodes to JSON.
For this alternative, change the loader to accept `SessionUser`, declare
`users.identity` as `auth.SessionIdentity[SessionUser, User]`, and reconstruct it
with `auth.NewSessionIdentity(users.Load)`. Store the version next to the ID:

```go
type SessionUser struct {
	ID      int
	Version int
}

if err := users.identity.Login(request.MustSession(r), SessionUser{ID: user.ID, Version: user.SessionVersion}); err != nil {
	return err
}
```

The load function then receives both, and treats a session with an old version
as signed out:

```go
func (users *Users) Load(ctx context.Context, id SessionUser) (User, error) {
	user, err := users.db.Find[User](ctx, query.Table("users", func(statement *query.Builder) {
		statement.Where("id", id.ID)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return User{}, auth.ErrNoCredentials
	}
	if err != nil {
		return User{}, err
	}
	if user.SessionVersion != id.Version {
		return User{}, auth.ErrNoCredentials
	}

	return user, nil
}
```

Increasing the user's version ends every one of their sessions at once. Because
the load function runs on every request, it takes effect on each device's next
request.

## Testing

The definition binds types, not separate identity slots. Definitions targeting
the same physical session use the same `arkoren:auth:user` entry: the last login
wins and logout removes that shared identity. Persisted raw JSON is unchanged.
For multiple browser sessions, use appropriate distinct cookies and storage;
optional [session handles](#custom-session-store) select
context slots only. Explicit `Login`/`Logout` sessions are not checked against a
definition's handle; nil sessions return `session.ErrNoSession`. Authenticated
users remain context-keyed by exact `U`, not by definition.

Most tests do not need the real login: put a user in the request's context, as
shown in [Auth tests](../testing/auth-tests.md). Keep one test that goes
through the real form with a cookie jar, as described in
[HTTP tests](../testing/http-tests.md#requests-across-several-calls), to
check the wiring.

For every function and option, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth).
