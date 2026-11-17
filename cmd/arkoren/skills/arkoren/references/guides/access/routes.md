---
title: Routes
description: Read the default route table of the access facade, the responses and problems clients receive, and how to serve routes on other paths.
order: 20
summary:
  - See which routes each enabled feature mounts and which features each route needs.
  - Read the 204 and 202 login responses and the problem statuses and stable types clients receive.
  - Serve a route on another path with its handler method, and avoid the mounting mistakes that panic.
---

# Routes

`iam.Mount(r)` registers the default JSON routes of every enabled feature, so a
front end or a mobile app can talk to the facade without you writing a handler.
Only the routes of enabled features are mounted. `r` must already run
`iam.Middleware()`.

## Route Table

| Area | Routes | Needs |
| --- | --- | --- |
| Login | `POST /login` | Passwords |
| Login | `POST /register` | Passwords with `Registration(true)` |
| Login | `POST /login/challenge`, `GET /login/pending` | A second factor |
| Login | `POST /logout` | Always |
| Step-up | `POST /confirm` | A step-up method |
| Email | `POST /email/verify`, `POST /email/verify/resend` | Built-in users with `accounts.Verification` |
| Password | `POST /password/forgot`, `POST /password/reset` | Passwords with `passwords.Reset` |
| Password | `PUT /password` | Passwords |
| Two-factor | `POST /two-factor/setup`, `POST /two-factor`, `DELETE /two-factor`, `POST /two-factor/recovery-codes` | Two-factor |
| Tokens | `GET /tokens`, `DELETE /tokens/{id}` | Tokens |
| Tokens | `POST /tokens` | Tokens with personal tokens on (the default) |
| Tokens | `POST /device-tokens`, `DELETE /device-tokens/current` | Tokens with device tokens on (the default) |
| Sessions | `GET /sessions`, `DELETE /sessions/{id}`, `DELETE /sessions` | Always |
| Providers | `GET /auth/{name}`, `GET` or `POST /auth/{name}/callback` | A provider |
| Providers | `GET /auth/{name}/nonce`, `POST /auth/{name}/native` | A provider with native login |
| Connections | `GET /connections`, `POST /connections/{provider}`, `DELETE /connections/{id}` | A provider |
| Account | `DELETE /account` | Built-in users |

Providers mount their routes whether or not sign-up is on for them; sign-up, which
you set for each provider, only decides whether an unknown identity creates a user
(`403` otherwise).

`POST /connections/{provider}` is wrapped in the `application/json` check like the
other `POST` routes and answers `303` to the provider's login route. A browser
therefore connects a provider with `fetch` and the JSON content type, then
navigates to `/auth/{provider}` itself; a plain HTML form gets `415`. See
[Connections](connections.md#connecting-a-provider).

A step-up method exists when passwords or two-factor are enabled, or when you add
one with `iam.StepUp`. A second factor exists when two-factor is enabled or you
add one with `iam.SecondFactor`. Each feature has its own page for what its
routes accept and answer: [Passwords](passwords.md), [Two-Factor](two-factor.md),
[API Tokens](tokens.md), [Connections](connections.md) and
[Sessions and Accounts](accounts.md).

## Responses

Request and response bodies are JSON, and handlers read at most 64 KiB of body.
Every `POST`, `PUT` and `PATCH` route requires the media type `application/json`,
with or without a `charset`, even routes that read no body such as `/logout`;
a form or a missing `Content-Type` answers `415` with the problem of core's
[content type middleware](../http-fundamentals/content-type.md)
(`tag:arkoren.dev,2026-09:core:http/middleware/contenttype:unsupported-media-type`).
`DELETE` routes
are not checked, because a form cannot send `DELETE`. The check is built into
each handler, not into `Mount`, so it also holds when you [serve a route on your
own path](#custom-paths). The provider routes under `/auth/{name}` are exempt,
because identity providers redirect with a query or post a form (`form_post`).
The check is not a replacement for
[CSRF protection](../browser-security/csrf-protection.md), which you add next to
`iam.Middleware()`.
A login that is complete answers `204`. When the user must also answer a second
factor it answers `202`:

```json
{ "second_factor": { "methods": ["otp", "recovery"] } }
```

The client then posts the method and its code to `/login/challenge`, and
`GET /login/pending` lists the methods again:

```json
{ "method": "otp", "code": "123456" }
```

Methods appear in registration order, and the user picks any method they have
enrolled.

## Problems

Failures are problem responses with a stable `type` of the form
`tag:arkoren.dev,2026-11:ark/access:<package>:<name>`, so clients can switch on
the type instead of the text. For example, a wrong password answers
`tag:arkoren.dev,2026-11:ark/access:password:invalid-credentials`. The status
groups are:

| Status | Meaning |
| --- | --- |
| 401 | No login, no pending login, or a credential route reached with a bearer token; also `federation:not-linked`. |
| 403 | Step-up needed (the problem lists the methods the user can confirm with), `account:email-unverified`, a wrong account, `federation:no-account` (an unknown provider identity with sign-up off), `federation:email-unverified` (a sign-up without a proven email) or `apitoken:scope-not-granted`. |
| 404 | An unknown token, connection or provider. |
| 409 | A conflict: two-factor already on, the last way to log in, `account:email-taken`, `federation:linked-elsewhere` or `federation:already-linked`. |
| 422 | A wrong secret or invalid input, such as a wrong password or a weak one. |
| 429 | Throttled, with `Retry-After` in seconds. |

Core's own challenge and confirm routes answer a wrong code with `403`, not
`422`, because the attempt counter lives in core. The names under each package
are:

| Package | Problem names |
| --- | --- |
| `account` | `email-taken`, `invalid-email`, `invalid-token`, `email-unverified`, `verify-wrong-account`, `throttled` |
| `password` | `invalid-credentials`, `invalid-password`, `invalid-token`, `throttled` |
| `twofactor` | `invalid-code`, `already-enabled`, `not-enabled`, `not-enrolling`, `throttled` |
| `apitoken` | `invalid-request`, `scope-not-granted`, `not-found` |
| `federation` | `not-linked`, `no-account`, `email-unverified`, `linked-elsewhere`, `already-linked`, `last-link`, `not-found`, `unknown-provider` |

## Custom Paths

To serve a route on another path, register its handler yourself instead of
calling `Mount`. Every route has a handler method on the facade:

```go
web.Post("/accounts/sign-up", iam.RegisterHandler())
web.Post("/accounts/sign-in", iam.LoginHandler())
```

The methods are `RegisterHandler`, `LoginHandler`, `ChallengeHandler`,
`PendingHandler`, `LogoutHandler`, `ConfirmHandler`, `VerifyEmailHandler`,
`ResendVerificationHandler`, `ForgotPasswordHandler`, `ResetPasswordHandler`,
`ChangePasswordHandler`, `SetupTwoFactorHandler`, `EnableTwoFactorHandler`,
`DisableTwoFactorHandler`, `RecoveryCodesHandler`, `ListTokensHandler`,
`CreateTokenHandler`, `DeleteTokenHandler`, `ExchangeHandler`,
`DeleteDeviceTokenHandler`, `ListSessionsHandler`, `DeleteSessionHandler`,
`DeleteOtherSessionsHandler`, `ListConnectionsHandler`, `ConnectHandler`,
`DeleteConnectionHandler` and `DeleteAccountHandler`. Build a handler for a
feature you did not enable and it panics at composition, so the mistake surfaces
at startup and not on the first request.

The verified-email, fresh-login, browser-session and `application/json` checks
live in the handlers themselves, so they hold on custom paths too.

## Mounting Rules

Mount at the root. The connect handler redirects to `/auth/{name}` and the
provider callbacks are mounted there, so a prefix would break provider login.
If a provider must live at another path, mount its handlers yourself and build the
connect handler with the targets you chose:

```go
web.Get("/sso/google", google.LoginHandler())
web.Get("/sso/google/callback", google.CallbackHandler(iam.Guard(), iam.Resolve))
web.Post("/connections/{provider}", iam.Links().ConnectHandler(iam.Guard(), map[string]string{
	"google": "/sso/google",
}))
```

`iam.Resolve` maps identities through the connection store without ever signing a
user up. For a hand-mounted provider that should, make its resolver with
`iam.Links().Resolver`; `federation` is `arkoren.dev/ark/access/federation`:

```go
resolve, err := iam.Links().Resolver(func(resolver *federation.ResolverBuilder) {
	resolver.SignUp(true)
})
if err != nil {
	return err
}
web.Get("/sso/google/callback", google.CallbackHandler(iam.Guard(), resolve))
```

`Mount` skips a route whose requirement is missing instead of panicking: no
forgot or reset routes without `passwords.Reset` and no email verification routes
without `accounts.Verification`, because no user could receive a link, and no
`POST /register` unless registration is enabled. It never
panics for a configuration `New` accepted, except for one mistake: mounting
twice, or on a router that already has one of these patterns, panics like any
duplicate route.

`iam.MustAccept(auth.MethodOTP)` fails startup the same way when a credential
method you rely on is not registered.

For every function and option, see
[the `access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
