---
title: Session Login
description: Check application-owned credentials, rotate the browser session, and revoke account sessions.
order: 20
summary:
  - Find current accounts through a user provider and reject deleted or disabled accounts.
  - Check passwords without revealing account existence and log in through one entry point that rotates the session.
  - End local logins, revoke older account sessions, list and revoke devices, and select the intended session context.
---

# Session Login

A browser login checks credentials once, then stores the account's subject in
the session. On later requests, `auth.Login` asks your **user provider** whether
that subject still exists and publishes an `auth.Identity` whose subject is the
stored one, whose factors say how the user logged in and whose user record holds
the loaded account, so `request.User[Account]` returns it. You own the accounts,
password checks, and account-status policy; [Sessions](../http-capabilities/sessions.md#enable-sessions)
owns cookie configuration, storage, and response-time persistence.

The examples assume an existing router `web`, session store `store` (a
`*cache.Cache`), configured [`hash.Hasher`](../hashing/overview.md), and
application repository `accounts`. Import `auth` from `arkoren.dev/core/auth`,
`session` and `response` from `arkoren.dev/core/http/`, `csrf` from
`arkoren.dev/core/http/middleware/csrf`, and standard-library `context`,
`errors`, and `net/http` where used.

## Load Accounts

Keep the session small: store a subject, not a snapshot of permissions. The
provider can then reject a deleted or disabled account on the next request. This
application keeps its account in one type:

```go
type Account struct {
	ID               int
	Name             string
	PasswordHash     []byte
	Disabled         bool
	CredentialsStamp string
}
```

Here `accounts.ByID(ctx, id)` returns `Account`; the application sentinel
`ErrAccountNotFound` means absence, while other errors retain storage failures.
Wrap the loader in a user driver and the driver in a provider. The identifier
type (`int`) must be a string, an integer or a text type, because it becomes the
subject:

```go
driver, err := auth.NewUserDriver(
	func(ctx context.Context, id int) (Account, error) {
		account, err := accounts.ByID(ctx, id)
		if errors.Is(err, ErrAccountNotFound) {
			return Account{}, auth.ErrUserNotFound
		}

		return account, err
	},
	func(account Account) int { return account.ID },
	func(driver *auth.UserDriverBuilder[Account]) {
		driver.Disabled(func(account Account) bool { return account.Disabled })
		driver.Version(func(account Account) string { return account.CredentialsStamp })
	},
)
if err != nil {
	return err
}

provider, err := auth.NewUserProvider(driver)
if err != nil {
	return err
}
```

The provider runs on every request with a usable stored subject. Absence
(`auth.ErrUserNotFound`) and disabled status (`auth.ErrUserDisabled`) become
anonymous requests; a storage outage instead stops the request. If you cache
accounts, your invalidation policy determines how quickly account changes take
effect. The record's `Value` is the loaded `Account`, hash included, so read only
the fields a handler needs and never render the whole value in a response.

## Enable Login

Construct one login and share it between middleware and handlers:

```go
login, err := auth.NewLogin(provider, store)
if err != nil {
	return err
}

web.Use(session.Middleware(store))
web.Use(csrf.Middleware())
web.Use(auth.Middleware(login.Authenticator()))
```

Session middleware must run first. Keep [CSRF protection](../browser-security/csrf-protection.md)
on login, logout, and other state-changing browser routes: browsers send session
cookies automatically, including on requests another site can initiate. Add
`auth.Require()` only to routes requiring a signed-in account, not to the login
route itself.

`NewLogin` does no I/O. It returns an error for a nil provider or store and for a
non-positive pending-login lifetime or attempt limit. Missing sessions, absent
session records, records of another shape and sessions written by earlier
versions (under `arkoren:auth:user`) are anonymous requests, not errors.

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

After the credential checks succeed, call `Begin` with the account's subject and
the factor you verified. Do it before writing response headers:

```go
subject, err := driver.Subject(account)
if err != nil {
	return err
}

factor := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}
complete, err := login.Begin(r, subject, factor)
if err != nil {
	return err
}
if !complete {
	return response.Status(w, http.StatusAccepted) // a second factor is needed
}

return response.Status(w, http.StatusNoContent)
```

`Begin` is the single entry for every login method, including OAuth, SAML and
passkeys. It looks the account up through the provider (a deleted or disabled
account is `auth.ErrUnauthenticated`), writes one session record with the
subject, factors and credential version, and then regenerates the session ID.
Rotation protects against **session fixation**, where an attacker knows an ID
planted before the victim signs in. If a write fails, no rotation occurs and you
must not report success. Begin preserves other session values, creation time and
expiry; configure idle and absolute limits in
[session lifetimes](../http-capabilities/sessions.md#set-lifetimes).

Without a registered second-factor check `Begin` always returns `true`. See
[Two-Factor Login](two-factor.md) for the pending state, levels and step-up
middleware.

This call changes the request's session, not persistent storage. On a non-5xx
response, session middleware saves changes and retires the old ID before
committing the response. Failed persistence or retirement can leave an old ID
usable; configure and monitor the middleware's error handling rather than
treating `Begin` success as proof of durable revocation. See
[Save Changes](../http-capabilities/sessions.md#save-changes) for the commit and
concurrency boundaries.

## End Sessions

Mount logout as a CSRF-protected `POST`, not a `GET`, and remove the login before
sending a response:

```go
if err := login.Logout(r); err != nil {
	return err
}

return response.Status(w, http.StatusNoContent)
```

Logout removes the session record, its [login record](#devices) and any pending
login, even stale or unreadable ones, and rotates the ID after successful
deletion. Other values, such as a
language preference, remain. Use `request.MustSession(r).Destroy()` instead when
the whole session must end. Both paths still depend on response-time
persistence; a failed retirement can leave the old ID valid.

## Revoke Account Sessions

To end sessions across devices after a password change or compromise, give the
account a credentials stamp that changes with its password, as `Version` above
does with `CredentialsStamp`. `Begin` stores a hash of the version in the session
record, and the authenticator compares it in constant time with the provider's
current value on every request. A mismatch makes the session anonymous on that
device's next request; no session enumeration is required. This includes the
device that changed the password, which must log in again; a login waiting for
its second factor when the version changes is discarded too. Calling `Begin` there
instead would re-run the second-factor checks and start the factors over, so an
enrolled user would be parked as pending.

Existing in-flight requests are not retroactively canceled, and account caching
can delay the effect. A provider that leaves `Version` empty never ends sessions
this way. Only disabling or deleting the account does.

## Devices

Every login creates a login record: a random ID, the creation and last-seen
times, the user agent and the client address. All records of a user live in one
entry of the `store` you gave `auth.NewLogin`, and the session stores only the
record's ID, which stays the same when the session ID rotates. The authenticator
reads the entry on every request (one extra cache read), and a session whose
record is gone is anonymous, so revoking a record logs that device out on its
next request.

```go
web.Get("/devices", func(w http.ResponseWriter, r *http.Request) error {
	records, err := login.Logins(r) // Current marks this browser.
	if err != nil {
		return err
	}

	return response.JSON(w, http.StatusOK, records)
})

web.Post("/devices/revoke-others", func(w http.ResponseWriter, r *http.Request) error {
	if err := login.RevokeOthers(r); err != nil {
		return err
	}

	return response.Status(w, http.StatusNoContent)
})
```

Use `login.Revoke(ctx, subject, ids...)` to log out chosen devices,
`login.RevokeAll(ctx, subject)` for all of them and `login.LoginsOf(ctx, subject)`
for an administrator's view. Mount the revoking routes as CSRF-protected `POST`s.
`Logout` removes the current record, and forgetting a user with
`UserProvider.Forget` revokes all of theirs.

Behavior to know:

- Records live as long as the maximum session lifetime. `session.Middleware`
  does not expose it, so tell the login with `policy.SessionLifetime(d)` if you
  changed `MaxLifetime` from the 24-hour default (see
  [Sessions](../http-capabilities/sessions.md)); the two must match.
- `SeenAt` is refreshed at most once every five minutes, on a best-effort basis:
  a failed refresh write never fails the request, so it can lag by more. Ordinary
  requests stay read-only.
- A device whose record was revoked is anonymous, so it cannot call `Logins` or
  `RevokeOthers` either. Records of sessions that expired without a logout stay
  listed until the record list expires or the 50-record limit drops them.
- `Begin`, `Complete` and `Logout` return cache and lock errors from the record
  store; the session is unchanged when they do.
- At most 50 records are kept per user (`policy.MaxLogins(n)`). A new login
  beyond that drops the oldest record and logs that device out.
- The user agent is cut to 255 bytes. The address is the host of `r.RemoteAddr`;
  behind a reverse proxy that is the proxy, so set
  `policy.ClientIP(ratelimit.TrustedProxyKey(prefixes...))` with your proxy
  prefixes from `arkoren.dev/core/http/middleware/ratelimit`. It trusts
  `X-Forwarded-For` only from those prefixes and reduces IPv6 addresses to their
  `/64` prefix, which is what the record stores. Both are personal
  data: `policy.Devices(false)` stores neither, and records never go into
  telemetry or events.
- A flushed or replaced cache logs every user out.

## Select Sessions

If you configured a nondefault session context slot, share one handle between
the middleware and the login:

```go
handle := session.NewHandle()
login, err := auth.NewLogin(provider, store, func(policy *auth.LoginBuilder) {
	policy.Handle(handle)
})
web.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.Handle(handle)
}))
```

Register CSRF and authentication after this session middleware as before. Every
`Login` method selects only this slot and returns `session.ErrNoSession` for an
absent one (the authenticator returns `auth.ErrNoCredentials`); it never falls
back to the default slot. A zero handle selects the default.

A handle is not cookie or storage isolation. Logins writing to the same physical
session share `arkoren:auth:session` and `arkoren:auth:pending`: the last login
replaces the other's record, and logout removes it. Independent browser sessions
also need distinct cookie and storage configuration in
[Sessions](../http-capabilities/sessions.md). For authentication tests, combine
[direct identity tests](../testing/auth-tests.md) with a real cookie-based
login/logout test that exercises persistence.

For all functions and options, see the
[`auth` API reference](https://pkg.go.dev/arkoren.dev/core/auth).
