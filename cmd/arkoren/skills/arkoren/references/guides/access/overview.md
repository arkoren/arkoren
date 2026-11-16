---
title: Access Overview
nav: Overview
description: Compose login, two-factor, tokens and provider sign-in, with optional registration, into one facade, then install its middleware and mount its routes.
order: 10
summary:
  - Decide when the access facade fits better than the hand-built login of core authentication.
  - Install the module, enable the features you need and install its middleware and routes.
  - Run its migrations, check readiness, send signed reset and verification links through your own queue or workflow and listen to its events.
---

# Access Overview

The [core authentication pages](../authentication/overview.md) show how to build
login from pieces: an authenticator, `auth.Guard` and your own routes. The
`access` module is the same pieces, composed. One facade builds the guard, the
stores for passwords, authenticators, tokens and connections, and the JSON routes
that use them, so you enable the features you want instead of writing each
handler.

Use it when you want login, two-factor, API tokens, provider sign-in and,
if you choose, registration working and tested. Keep the hand-built approach of
[Session Login](../authentication/session-login.md) when your login is unusual
enough that you would replace most of the handlers anyway. The module depends
only on core, Arkoren's foundation libraries and the standard library, and it
never imports the OAuth module: a provider joins through a small interface that
the [OAuth](../oauth/overview.md) clients satisfy.

## Installation

Run this from your application's module directory:

```sh
go get arkoren.dev/ark/access
```

## Composing

The application brings the database, the cache that holds sessions and counters,
a function that finds the client address, a [hasher](../hashing/overview.md) for
passwords and a [cipher](../encryption/overview.md) for two-factor secrets. The
database, the cache and the client address function are required positional
arguments of `New`; each feature is off until you enable it in the callback:

```go
// Behind a reverse proxy, list the prefixes of your proxies. With no proxy in
// front of the application, call ratelimit.TrustedProxyKey() with no arguments.
clientIP := ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8"))
scheme := totp.New("Acme") // the name authenticator apps show for this account
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Password(hasher, func(passwords *password.Builder) {
		passwords.Registration(true).Reset(signer, dispatchReset, deliverReset)
	})
	iam.Directory(func(accounts *account.Builder) {
		accounts.Verification(signer, dispatchVerification, deliverVerification)
	})
	iam.TwoFactor(scheme, cipher)
	iam.Tokens()
	iam.Provider(google, github)
	iam.Federation(func(links *federation.Builder) {
		links.SignUp(true)
	})
})
if err != nil {
	return err
}
```

`signer`, `dispatchReset`, `deliverReset`, `dispatchVerification` and
`deliverVerification` come from [Sending Email Links](#sending-email-links). The
module never sends mail itself: password reset and email verification stay off
until you give them those functions.

Registration and provider sign-up are the two ways a user can appear without
your code creating them, and both are off until you enable them as above. With
both off, you add users yourself through `iam.Directory()` or your own users; see
[Adding Users](accounts.md#adding-users).

`New` does no I/O. It returns an error wrapping `access.ErrInvalidConfig` when a
dependency is missing (the database, the cache or the client address function), a
provider name is used twice or contains anything but letters, digits, hyphens and underscores, or a package
rejects its settings, such as an invalid TOTP scheme, a nil cipher for
two-factor, registration or sign-up over users that cannot be created, registration with the
built-in users when neither reset nor verification is configured (a new user could
never prove their email), a scope catalog without a grant function, or tokens with
neither kind enabled. `New` never guesses the
client address, because the right source depends on the deployment (see
[Shared Settings](#shared-settings)).

| Feature | Builder method | Guide |
| --- | --- | --- |
| Login, change, and registration when enabled | `iam.Password(hasher)` | [Passwords](passwords.md) |
| Forgotten-password reset | `passwords.Reset(signer, dispatch, deliver)` | [Sending Email Links](#sending-email-links) |
| Email verification | `accounts.Verification(signer, dispatch, deliver)` | [Sending Email Links](#sending-email-links) |
| Authenticator app and recovery codes | `iam.TwoFactor(scheme, cipher)` | [Two-Factor](two-factor.md) |
| Personal and device tokens | `iam.Tokens()` | [API Tokens](tokens.md) |
| Sign-in with external providers, and sign-up when enabled | `iam.Provider(clients...)` | [Connections](connections.md) |

The facade also owns the built-in `users` table, which identifies users by
email; `iam.Directory(...)` configures it and `iam.Directory()` on the built
facade adds users. To keep your own users instead, see [Your Own Users](own-users.md).

## Protecting Routes

Install `iam.Middleware()` on every router that identifies users, then mount the
routes. The middleware runs the session middleware and authenticates browser
sessions and, when tokens are enabled, bearer tokens:

```go
web := routes.With(iam.Middleware())
iam.Mount(web)

api := routes.With(iam.Middleware(), auth.Require())
api.Get("/me", me)
```

Protect your own routes with `auth.Require()` as usual. The facade owns both the
session middleware and the guard and keeps them on one lifetime: set it with
`iam.SessionLifetime` (24 hours by default), not on each. It uses the default
session handle, which the OAuth provider handlers require. [Routes](routes.md)
lists what `Mount` registers.

Add core's CSRF middleware next to it, before `iam.Mount`:

```go
web := routes.With(csrf.Middleware(), iam.Middleware())
iam.Mount(web)
```

> [!WARNING]
> `iam.Middleware()` does not add [CSRF protection](../browser-security/csrf-protection.md),
> and the routes change state with a session cookie. Install `csrf.Middleware()`
> yourself, from `arkoren.dev/core/http/middleware/csrf`. Every `POST`, `PUT` and
> `PATCH` route of the facade also requires `Content-Type: application/json` and
> answers `415` otherwise, which stops a cross-site HTML form but is not CSRF
> protection: it does not cover `DELETE`, other request types or a site that
> sends JSON itself. Native clients that send no `Origin` header pass the CSRF
> middleware, and bearer tokens need no cookie.

## Migrations and Readiness

The facade never runs DDL. `iam.Migrations()` returns the migrations of the
enabled features only, so add them to your migrator and run it:

```go
if err := migrator.Add(iam.Migrations()...); err != nil {
	return err
}
```

`iam.Ping(ctx)` checks, read-only, that every enabled table exists. When one is
missing it returns an error wrapping `access.ErrNotMigrated` that names the
table, so it works as a readiness check.

## Sending Email Links

A user who asks for a password reset or an email verification must receive a link
that proves they control the mailbox. The facade never sends mail and starts no
goroutines for it. You give each feature two functions:

- `dispatch` runs inside the request. It stores the work durably, for example as
  a queue job, a workflow run or an outbox row, and returns, or returns an error.
- `deliver` runs later, in your worker. It receives the address and the signed
  link, renders the email and sends it.

The link is a signed value, so the module keeps no token tables. It is bound to the
state the action changes, which makes it single use without storing anything: a
reset link is bound to the user's current password hash, so it stops working as
soon as the password changes, and a verification link is bound to the user's email
and whether it is verified. Both also expire, after an hour for a reset
(`passwords.ResetTTL`) and 24 hours for a verification (`accounts.VerifyTTL`).

### Build the Signer

Reset and verification links share one signer, built from a random 32-byte key
that you reserve for links; see [Signed Links](../http-capabilities/secure-links.md)
for provisioning it. Give it your public origin so the signed value is an absolute
URL:

```go
secret, err := key.LoadShared("link-key.key")
if err != nil {
	return err
}
signer, err := link.New(secret, func(links *link.Builder) {
	links.Origin("https://app.example.com")
})
```

Without an origin the signed value is a relative path and query. Both forms verify,
as long as the same signer verifies what it signed. Each link is signed for its own
path, so a verification link never works as a reset link.

### Dispatch with a Queue

`dispatch` receives a plain request value with JSON tags, so it is the payload of a
[queue job](../queues/overview.md):

```go
resetJob := queue.NewJob[password.ResetRequest]("send-reset")
verifyJob := queue.NewJob[account.VerifyRequest]("send-verification")

dispatchReset := func(ctx context.Context, request password.ResetRequest) error {
	return dispatcher.Dispatch(ctx, resetJob, request)
}
dispatchVerification := func(ctx context.Context, request account.VerifyRequest) error {
	return dispatcher.Dispatch(ctx, verifyJob, request)
}
```

`password.ResetRequest` carries the identifier exactly as the user typed it,
because the forgot-password route never looks the user up: whether the user exists
must not change the response or its timing. `account.VerifyRequest` carries the
subject. Request values only ever gain fields, so jobs stored before an upgrade
still decode.

The worker passes each stored request to the facade, which does the lookup, signs
the link and calls your `deliver`. `iam.SendReset` and `iam.SendVerification` have
the shape of a job handler:

```go
if err := consumer.Handle(resetJob, iam.SendReset); err != nil {
	return err
}
if err := consumer.Handle(verifyJob, iam.SendVerification); err != nil {
	return err
}
```

An unknown identifier, a deleted user or a user whose email is already verified is
not an error: nothing is sent and the call returns nil, so the queue does not
retry it. If `deliver` fails, its error is returned and the queue retries, so
`deliver` can run more than once for one request. Each attempt signs a new link and
all of them work until the state they are bound to changes or they expire, so a
repeated email is harmless.

### Dispatch with a Workflow

A [workflow](../workflows/overview.md) fits when you want retries and history
under its control. Wrap the facade's function in a step and start a run from
`dispatch`. `dispatch` runs while the facade is being built and the step needs the
facade, so declare the workflow variable first and assign it after `access.New`:

```go
var resetEmail *workflow.Workflow[password.ResetRequest, struct{}]

dispatchReset := func(ctx context.Context, request password.ResetRequest) error {
	_, err := client.Start(ctx, resetEmail, rand.Text(), request)

	return err
}
```

Once `iam` exists, define the step and the workflow, and register both on the
worker with `workflow.Handlers(resetEmail, sendReset)`:

```go
sendReset := workflow.NewStep("send-reset", func(ctx context.Context, request password.ResetRequest) (struct{}, error) {
	return struct{}{}, iam.SendReset(ctx, request)
})
resetEmail = workflow.New("reset-email", func(c *workflow.Context, request password.ResetRequest) (struct{}, error) {
	return c.Step(sendReset, request)
}, func(definition *workflow.Builder) {
	definition.MaxDuration(time.Hour)
})
```

Verification is the same with a second step around `iam.SendVerification`.

### Deliver with Mail

`deliver` receives the recipient, the signed value and when it expires. The address
is the one stored with the user, never the text of a request, so a request cannot
send someone else's link elsewhere. `message.Link(base)` returns the signed value
in the URL fragment, as `https://app.example.com/reset-password#<escaped value>`:

```go
deliverReset := func(ctx context.Context, message password.ResetMessage) error {
	text := "Set a new password: " + message.Link("https://app.example.com/reset-password")
	if message.Notice {
		text = "Someone tried to register with this address. If it was not you, set a new password: " +
			message.Link("https://app.example.com/reset-password")
	}
	email := mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: message.Address}}}, mail.Body{Text: text}, func(letter *mail.MessageBuilder) {
		letter.Subject("Reset your password")
	})
	_, err := mailer.Send(ctx, email)

	return err
}
```

`message.Notice` is true for the notice that goes to the owner of an address when
someone tries to register with it. Verification is the same with an
`account.VerifyMessage`, which has no `Notice`. The signed value is a credential
until it is used or expires, so never log it.

### Read the Link in the Page

A browser never sends the fragment to a server, so the link stays out of access
logs, proxies and the `Referer` header of the page it opens. Your page reads it
from `location.hash`, removes it from the address bar and posts it as the `token`:

```js
const token = decodeURIComponent(location.hash.slice(1));
history.replaceState(null, "", location.pathname + location.search);
await fetch("/password/reset", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ token, password }),
});
```

The verification page posts the same `token` to `/email/verify`. A reset answers
`422` with the invalid-token problem for a link that is malformed, forged, expired
or already used, and the same for a verification link, so a client learns nothing
about which.

### What Mount Does Without Them

Without `passwords.Reset`, `Mount` skips `POST /password/forgot` and
`POST /password/reset`. Without `accounts.Verification`, or with your own users,
it skips the email verification routes. The matching handler methods, such as
`iam.ForgotPasswordHandler()`, panic at composition instead of returning a handler
that cannot work.

In tests, make `dispatch` call `iam.SendReset` or `iam.SendVerification` directly,
so the email is delivered before the request returns and no queue is needed.

## Events

Pass your event bus with `iam.Events(bus)` and listen like any other
[event](../events/listeners.md). Events carry subjects only, never tokens, codes
or email addresses, so listeners cannot leak them. Use them for notifications
such as "your password changed" or "two-factor was turned off".

| Package | Events |
| --- | --- |
| `account` | `UserCreated`, `EmailVerified`, `UserDeleted` |
| `password` | `Changed`, `WasReset` |
| `twofactor` | `Enabled`, `Disabled`, `Regenerated`, `RecoveryUsed` |
| `apitoken` | `Issued`, `Revoked` |
| `federation` | `LinkCreated`, `LinkDeleted` |

Core's `auth.CredentialEnrolled` is published when two-factor is enabled, and
core's `auth.UserForgotten` when the built-in users are removed with their rows.

## Storage and Instances

Each enabled feature keeps its own tables and migration group, named
`arkoren.dev:ark/access:<package>`. Every package assumes the one
`*database.Database` you pass to `New`, so writes that span packages, such as
registering a user and storing their password, or deleting an account, join the
transaction carried by the context.

A named instance prefixes every table and group, so two facades can share one
database:

```go
iam.Instance("tenant_a")
```

The name is 1 to 20 bytes of lowercase letters, digits and underscores, and it
starts with a letter.

## Shared Settings

These builder methods configure what the facade shares between features; the
client address function is the third argument of `New`:

- `iam.SessionLifetime(d)` sets the absolute lifetime of a login for the session
  middleware and the guard together.
- The client address function, passed to `New`, finds the client address for
  login records and throttles. Behind a reverse proxy pass
  `ratelimit.TrustedProxyKey(prefixes...)` with your proxies' prefixes: it trusts
  `X-Forwarded-For` only from a request that arrives from those prefixes. An
  application that faces the internet directly passes
  `ratelimit.TrustedProxyKey()` with no prefixes, which returns the host of
  `r.RemoteAddr`. Both reduce an IPv6 address to its `/64` prefix.
- `iam.Guard(configure)` customizes the guard, such as the step-up window with
  `FreshFor`. The facade applies its own settings after your callback, so the
  session lifetime, the session handle and the client address function you passed
  to `New` always win. Register credentials with `iam.SecondFactor`
  and `iam.StepUp`, not there: the registries only append.
- `iam.Session(configure)` customizes the session cookie, such as its name. The
  facade applies the maximum lifetime and the default handle after your callback,
  so they cannot be overridden.

`iam.SecondFactor`, `iam.StepUp` and `iam.Authenticator` add credentials and
authenticators from other modules, such as a passkey package, after the built-in
ones. `iam.MustAccept(auth.MethodOTP)` panics at composition unless a method you
expect is registered, so a typo fails at startup.

For code that logs users in itself, the facade exposes what it built:
`iam.Guard()`, `iam.UserProvider()`, `iam.Directory()`, `iam.Verifier()`,
`iam.Factors()`, `iam.Issuer()` and `iam.Links()`. Each returns nil when its
feature is off.

For every function and option, see
[the `access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
