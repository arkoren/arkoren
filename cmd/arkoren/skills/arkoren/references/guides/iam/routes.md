---
title: Routes
description: Look up IAM's default routes, availability rules, response problems, and the steps for registering individual handlers on custom paths.
order: 120
summary:
  - Find the routes registered for each enabled feature and distinguish availability from request authorization.
  - Interpret JSON requirements, login responses, and stable problem types.
  - Register custom handlers without losing their security checks or breaking provider redirects.
---

# Routes

`web.Group("/", identity.Register)` registers the default routes below. `web`
must already run the [IAM middleware and appropriate CSRF
protection](overview.md#register-routes). Register once at the root, never under
a path prefix; register individual handlers instead if your paths must differ.

This page is a lookup reference. The feature guides explain request bodies
and workflows; [Fresh Logins](freshness.md#credential-routes) compares their
session, freshness, and verified-email requirements.

## Default Routes

“Required feature” determines whether a route is registered, not whether a
particular request is authorized.

| Route | Required feature |
| --- | --- |
| `POST /login` | Passwords |
| `POST /register` | Passwords with registration enabled |
| `GET /login/pending` | A registered second-factor method |
| `POST /login/challenge` | A registered second-factor method |
| `POST /logout` | Always |
| `POST /confirm` | A registered step-up method |
| `POST /password/forgot` | Passwords with reset configured |
| `POST /password/reset` | Passwords with reset configured |
| `PUT /password` | Passwords |
| `POST /email/verify` | Built-in directory with verification configured |
| `POST /email/verify/resend` | Built-in directory with verification configured |
| `POST /two-factor/setup` | Two-factor |
| `POST /two-factor` | Two-factor |
| `DELETE /two-factor` | Two-factor |
| `POST /two-factor/recovery-codes` | Two-factor |
| `GET /tokens` | Tokens |
| `DELETE /tokens/{id}` | Tokens |
| `POST /tokens` | Personal tokens enabled |
| `POST /device-tokens` | Device tokens enabled |
| `DELETE /device-tokens/current` | Device tokens enabled |
| `GET /sessions` | Always |
| `DELETE /sessions/{id}` | Always |
| `DELETE /sessions` | Always |
| `GET /auth/{name}` | That provider registered |
| `GET /auth/{name}/callback` | That provider registered |
| `POST /auth/{name}/callback` | That provider registered |
| `GET /auth/{name}/nonce` | Provider implements `iam.NativeProvider` |
| `POST /auth/{name}/native` | Provider implements `iam.NativeProvider` |
| `GET /connections` | A provider registered |
| `POST /connections/{id}` | A provider registered |
| `DELETE /connections/{id}` | A provider registered |
| `GET /me/permissions` | Permissions with self-service |
| `GET /users/{subject}/permissions` | Permissions with management |
| `PUT /users/{subject}/permissions/{name}` | Permissions with management |
| `DELETE /users/{subject}/permissions/{name}` | Permissions with management |
| `PUT /users/{subject}/roles/{name}` | Permissions with management |
| `DELETE /users/{subject}/roles/{name}` | Permissions with management |
| `GET /account` | `Account()` |
| `DELETE /account` | Built-in directory |

Passwords register `pwd` for step-up. Two-factor registers `otp`, then
`recovery`, for login challenges, and `otp` for step-up.
`identity.SecondFactor` and `identity.StepUp` can append application credentials.
Registration, email recovery, and email verification are opt-in.
Provider sign-up is configured per provider and defaults to off; it does not
control whether that provider's routes exist.

Native route detection is structural. The shared OAuth client implements the
native methods even for presets without native support; those handlers return
`oauthauth.ErrUnsupported`. Google and Apple support native ID-token login
with explicit audiences. Route presence alone is not a provider-capability
check; use the [provider comparison](../oauth/providers.md#compatibility-matrix).

## Account

`GET /account` answers `200` with what IAM knows about the user of the request,
for a session or a token. Anonymous requests, and logins that still wait for a
second factor, answer `401`. Without an `Account` call the route does not exist
and the router answers `404`. `identity.Account(ctx)` returns the same
`iam.AccountView` as a Go value, with or without the route.

It always carries `subject` and the email facts. With the built-in users these
come from the directory: `email`, `email_verified`, `verified_at` and
`created_at`. With your own users they come from the `email` and `email_verified`
claims of the user record, and there are no timestamps. IAM never encodes your
user's value. The `Account` callback adds parts:

| Method | Adds |
| --- | --- |
| `Permissions(true)` | `roles` and `permissions`, as `GET /me/permissions` |
| `TwoFactor(true)` | `two_factor`: whether an authenticator is confirmed |
| `Login(true)` | `login`: `session`, `level` and `methods` |

Built-in users with everything on:

```json
{
  "subject": "3f9c2a7e1b8d4c6fa0e5d7b9c1a3f2e4",
  "email": "ada@example.com",
  "email_verified": true,
  "verified_at": "2026-11-02T09:14:00Z",
  "created_at": "2026-10-28T17:40:12Z",
  "roles": ["editor"],
  "permissions": ["posts:read", "posts:write"],
  "two_factor": true,
  "login": { "session": true, "level": "multi", "methods": ["pwd", "otp"] }
}
```

Your own users, with only the claims their record holds:

```json
{ "subject": "42", "email": "ada@example.com", "email_verified": true }
```

A request that carries a token has no factors, so everything else stays and
`login` is `{"session": false, "level": "none", "methods": []}`.

`verified_at` is left out while the email is unverified, and `email_verified` is
then `false`. A part that is off is absent; a part that is on and empty is `[]`
or `false`. `login.level` is `single` or `multi`, and `methods` lists the core
codes `pwd`, `otp`, `hwk` and `swk`. `Permissions(true)` without the permission
store and `TwoFactor(true)` without two-factor are ignored with a warning.
[Own Users](own-users.md#show-your-users) shows how to build your own `GET /me`.

## JSON Contract

IAM-owned handlers require `Content-Type: application/json` for `POST`,
`PUT`, and `PATCH`, including bodyless operations such as logout.
Media-type parameters such as `charset=utf-8` are allowed; form data,
missing media types, and different JSON media types return `415`.
Body-reading IAM handlers limit JSON input to 64 KiB.
`DELETE` handlers are not wrapped in the JSON media-type check.

The provider protocol routes under `/auth/{name}` follow the OAuth client's
own parsing rules: callbacks can arrive as a query or a form.
The [Apple callback](federated-sign-in.md#apple-callback) additionally needs its
precise CSRF exception. Do not disable CSRF for IAM's JSON routes.

The JSON checks and credential safeguards live in the handler constructors,
not just in `Register`. Custom paths retain them.

## Login Responses

Password and native login return `204` with no body when complete.
`202` means a second-factor challenge is still required:

```json
{
  "second_factor": {
    "methods": ["otp", "recovery"]
  }
}
```

`GET /login/pending` instead returns `200` with a flat methods object:

```json
{
  "methods": ["otp", "recovery"]
}
```

Methods appear in registration order; the client submits one offered method
to `/login/challenge`.

Do not confuse this `202` with registration, forgot-password, or verification
resend acceptance. Those return an empty body and do not create a login.
Browser provider callbacks redirect instead of using the JSON login response.

## Problem Responses

Branch on a problem's stable `type`, not its human-readable title or detail.
IAM types have this form:

```text
tag:arkoren.dev,2026-11:ark/access:<package>:<name>
```

For example, password-login failure is
`tag:arkoren.dev,2026-11:ark/access:password:invalid-credentials`.
Core-owned checks keep their core types.

| Status | Typical meaning |
| --- | --- |
| `400` | Malformed JSON; challenge and confirm also use this for oversized bodies |
| `401` | Missing session or pending login, a disallowed bearer identity, or failed token authentication |
| `403` | Freshness, email verification, provider membership, wrong verification account, scope ceiling, missing management permission, or grant escalation refused the action |
| `404` | Unknown or unowned token/connection, unknown provider, or unknown user in a permission route |
| `409` | Conflicting account/credential state, duplicate connection, last login method, or last holder of a protected role |
| `413` | Body limit exceeded |
| `415` | Required JSON media type absent or unsupported |
| `422` | Invalid input, password-login failure, enrollment code failure, or invalid email-link token |
| `429` | Throttled; inspect `Retry-After` in seconds |
| `500` | Infrastructure or required-hook failure; consult the feature's partial-success behavior |

Wrong core challenge and confirmation credentials are `403`, not the password-login
route's `422`. A missing or unknown method is `422`; an exhausted challenge
budget can discard the pending login and return `401`. Freshness uses
`tag:arkoren.dev,2026-11:core:auth:step-up-required` with a `methods`
extension, or `tag:arkoren.dev,2026-11:core:auth:no-step-up-method`.
The media-type check uses
`tag:arkoren.dev,2026-09:core:http/middleware/contenttype:unsupported-media-type`.

For package-specific problem names:

| Package | Names |
| --- | --- |
| `account` | `email-taken`, `invalid-email`, `invalid-token`, `email-unverified`, `verify-wrong-account`, `throttled` |
| `password` | `invalid-credentials`, `invalid-password`, `invalid-token`, `throttled` |
| `twofactor` | `invalid-code`, `already-enabled`, `not-enabled`, `not-enrolling`, `throttled` |
| `apitoken` | `invalid-request`, `scope-not-granted`, `not-found` |
| `federation` | `not-linked`, `no-account`, `email-unverified`, `linked-elsewhere`, `already-linked`, `last-link`, `not-found`, `unknown-provider` |
| `permission` | `unknown-grant`, `not-grantable`, `last-holder`, `user-not-found` |

## Custom Paths

If you need different application URLs, register the matching facade handlers
instead of calling `Register`:

```go
web.Post("/accounts/sign-in", identity.LoginHandler(), password.LoginOperation)
web.Put("/accounts/password", identity.ChangePasswordHandler(), password.ChangeOperation)
```

Pass the matching operation, as `Register` does, so the route appears in the router's
[OpenAPI document](../openapi/built-in-handlers.md#other-annotations). The operations live
beside the handlers: `password`, `twofactor`, `apitoken`, `federation`, `permission` and
`account` (from `arkoren.dev/ark/iam/...`) export one per handler, and `iam` exports
those of the session, account, login-challenge and provider routes. Operations that declare
path parameters use the names of the default paths.

Keep the facade middleware and CSRF protection on `web`.
Session, freshness, verified-email, and JSON checks remain inside the handlers.
Requesting a disabled feature's handler panics during composition; `Register`
instead skips unavailable features. Duplicate route patterns, including a
second `Register`, panic through the router.

Provider paths need additional care: both the client's registered redirect
URL and the connect-intent redirect must match your chosen routes.
For a configured Google client whose `RedirectURL` is
`https://app.example.com/sso/google/callback`, register its handlers:

```go
callback := google.CallbackHandler(identity.Guard(), identity.Resolve)
web.Get("/sso/google", google.LoginHandler(), oauthauth.LoginOperation)
web.Get("/sso/google/callback", callback, oauthauth.CallbackOperation)
web.Post("/sso/google/callback", callback, oauthauth.CallbackOperation)
```

`identity.Resolve` uses the shared connection store, including built-in unverified
account takeover, but **never opens sign-up**. A hand-registered callback does
not inherit a registered provider's sign-up settings.

For custom callbacks that should create users, first build a resolver using
`federation` from `arkoren.dev/ark/iam/federation`:

```go
resolve, err := identity.Links().Resolver(func(resolver *federation.ResolverBuilder) {
	resolver.SignUp(true)
})
```

After handling the error, pass `resolve` in place of `identity.Resolve`.
The facade must already have a provider registered so `identity.Links()` exists.

Finally, define where a connection intent sends the browser:

```go
targets := map[string]string{
	"google": "/sso/google",
}
```

Register the connect handler with those targets:

```go
web.Post("/connections/{provider}", identity.Links().ConnectHandler(identity.Guard(), targets), federation.ConnectOperation)
```

That preserves the [explicit connection flow](federated-sign-in.md#connect-a-provider).
Update the front end's navigation target too; the default
`/auth/{provider}` target is no longer correct. If you customize Apple's path,
update only that callback's CSRF exception to match it.

For the full handler set, see the
[`iam` API reference](https://pkg.go.dev/arkoren.dev/ark/iam).
