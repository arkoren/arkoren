---
title: Routes & Errors
nav: Routes & Errors
description: Look up every default IAM HTTP route, JSON media-type rule, login response shape, stable problem type, and how to mount handlers on custom paths.
order: 120
summary:
  - Look up every route registered by identity.Register and the configuration required to enable it.
  - Reference IAM's JSON contract, login response shapes, HTTP status codes, and stable problem types.
  - Mount individual IAM handlers on custom paths with OpenAPI annotations and custom provider targets.
---

# Routes & Errors

Calling `web.Group("/auth", identity.Register)` mounts every route supported by your configuration relative to `/auth` (use `"/"` for no prefix). Ensure `web` already carries `csrf.Middleware()` and `identity.Middleware()` as shown in [IAM Overview](overview.md#mounting-routes-and-protecting-endpoints).

This page is a quick reference for HTTP paths, payloads, error codes, and custom route wiring. For session, freshness, and verified-email requirements per action, see [Step-Up & Freshness](freshness.md#what-each-credential-route-requires).

## Default Routes

"Required configuration" controls whether `identity.Register` mounts the route, not whether a given request is authorized.

| Route | Required configuration |
| --- | --- |
| `POST /login` | `Password` |
| `POST /register` | `Password` with `Registration(true)` |
| `GET /login/pending` | Any registered second-factor method |
| `POST /login/challenge` | Any registered second-factor method |
| `POST /logout` | Always |
| `POST /confirm` | Any registered step-up method |
| `POST /password/forgot` | `Password` with `Reset` |
| `POST /password/reset` | `Password` with `Reset` |
| `PUT /password` | `Password` |
| `POST /email/verify` | Built-in directory with `Verification` |
| `POST /email/verify/resend` | Built-in directory with `Verification` |
| `POST /two-factor/setup` | `TwoFactor` |
| `POST /two-factor` | `TwoFactor` |
| `DELETE /two-factor` | `TwoFactor` |
| `POST /two-factor/recovery-codes` | `TwoFactor` |
| `GET /tokens` | `Tokens` |
| `DELETE /tokens/{id}` | `Tokens` |
| `POST /tokens` | `Tokens` with personal tokens enabled (default) |
| `POST /device-tokens` | `Tokens` with device tokens enabled (default) |
| `DELETE /device-tokens/current` | `Tokens` with device tokens enabled (default) |
| `GET /sessions` | Always |
| `DELETE /sessions/{id}` | Always |
| `DELETE /sessions` | Always |
| `GET /{name}` | `Provider` registered with that name |
| `GET /{name}/callback` | `Provider` registered with that name |
| `POST /{name}/callback` | `Provider` registered with that name |
| `GET /{name}/nonce` | Provider implements `iam.NativeProvider` |
| `POST /{name}/native` | Provider implements `iam.NativeProvider` |
| `GET /connections` | Any `Provider` registered |
| `POST /connections/{provider}` | Any `Provider` registered |
| `DELETE /connections/{id}` | Any `Provider` registered |
| `GET /me/permissions` | `Permissions` with `SelfService(true)` |
| `GET /users/{subject}/permissions` | `Permissions` with `Management(...)` |
| `PUT /users/{subject}/permissions/{name}` | `Permissions` with `Management(...)` |
| `DELETE /users/{subject}/permissions/{name}` | `Permissions` with `Management(...)` |
| `PUT /users/{subject}/roles/{name}` | `Permissions` with `Management(...)` |
| `DELETE /users/{subject}/roles/{name}` | `Permissions` with `Management(...)` |
| `GET /account` | `Account` |
| `DELETE /account` | Built-in directory |

`Password` registers `pwd` for step-up; `TwoFactor` registers `otp` and `recovery` for login challenges and `otp` for step-up. Native routes are detected structurally on `*oauthauth.Client`, so presets without native support still mount them and return `oauthauth.ErrUnsupported`; check the [provider matrix](../oauth/providers.md#compatibility-matrix) for actual support.

## JSON and Media-Type Rules

- All IAM-owned `POST`, `PUT`, and `PATCH` endpoints require `Content-Type: application/json` (parameters like `charset=utf-8` are allowed), even for bodyless requests such as `POST /logout`. Missing or other media types return `415`.
- `DELETE` handlers do not enforce the JSON media-type check.
- Body-reading IAM handlers cap JSON payloads at **64 KiB** (`413` above the limit; `/login/challenge` and `/confirm` return `400`).
- External provider routes (`/{name}` and `/{name}/callback`) use OAuth query/form parsing instead of JSON. See [Apple's Cross-Site Callback](federated-sign-in.md#apples-cross-site-callback) for Apple's CSRF exception.

All media-type, session, freshness, and verified-email checks live inside the handlers themselves, so custom paths keep the exact same protections.

## Login Response Shapes

Password login (`POST /login`) and native login (`POST /{name}/native`) return:

- **`204 No Content`**: Authentication is complete.
- **`202 Accepted`**: A second-factor challenge is required:

```json
{
  "second_factor": {
    "methods": ["otp", "recovery"]
  }
}
```

`GET /login/pending` returns `200` with a flat object:

```json
{
  "methods": ["otp", "recovery"]
}
```

Do not confuse this `202` with the empty `202` returned by `/register`, `/password/forgot`, or `/email/verify/resend`; those acknowledge queued work and never start a login. Browser OAuth callbacks redirect rather than returning JSON.

## Problem Types and Status Codes

Always branch on the problem's stable `type` URI rather than its human-readable `title` or `detail`. IAM problem types use the format:

```text
tag:arkoren.dev,2026-11:ark/access:<package>:<name>
```

| Status | Meaning |
| --- | --- |
| `400` | Malformed JSON (or oversized body on `/login/challenge` and `/confirm`) |
| `401` | Missing session or pending login, disallowed bearer token, or failed token auth |
| `403` | Freshness, verified email, provider membership, wrong verification account, scope ceiling, missing management permission, or grant escalation |
| `404` | Unknown or unowned token/connection, unknown provider, or unknown user in permission routes |
| `409` | Conflicting state, duplicate connection, last login method, or last holder of a `KeepOne` role |
| `413` | JSON body exceeds 64 KiB |
| `415` | Missing or unsupported `Content-Type` |
| `422` | Invalid input, failed password login, wrong enrollment code, or invalid email-link token |
| `429` | Rate-limited; inspect `Retry-After` (seconds) |
| `500` | Infrastructure or required-hook failure |

Core checks retain their own types, including `tag:arkoren.dev,2026-11:core:auth:step-up-required` (with a `methods` field), `tag:arkoren.dev,2026-11:core:auth:no-step-up-method`, and `tag:arkoren.dev,2026-09:core:http/middleware/contenttype:unsupported-media-type`. Note that wrong challenge or confirmation credentials return core's `403`, whereas wrong password login returns `422`.

IAM package problem names:

| Package | Names |
| --- | --- |
| `account` | `email-taken`, `invalid-email`, `invalid-token`, `email-unverified`, `verify-wrong-account`, `throttled` |
| `password` | `invalid-credentials`, `invalid-password`, `invalid-token`, `throttled` |
| `twofactor` | `invalid-code`, `already-enabled`, `not-enabled`, `not-enrolling`, `throttled` |
| `apitoken` | `invalid-request`, `scope-not-granted`, `not-found` |
| `federation` | `not-linked`, `no-account`, `email-unverified`, `linked-elsewhere`, `already-linked`, `last-link`, `not-found`, `unknown-provider` |
| `permission` | `unknown-grant`, `not-grantable`, `last-holder`, `user-not-found` |

## Custom Paths

To serve IAM endpoints at custom URLs, mount the individual handler methods on your `web` router instead of calling `identity.Register`. Pass each handler's matching operation so it appears in your [OpenAPI document](../openapi/built-in-handlers.md#other-annotations):

```go
web.Post("/accounts/sign-in", identity.LoginHandler(), password.LoginOperation)
web.Put("/accounts/password", identity.ChangePasswordHandler(), password.ChangeOperation)
```

Requesting a handler for a disabled feature panics at startup (unlike `Register`, which skips disabled features). Registering duplicate patterns panics through the router.

### Custom External Provider Paths

When mounting a provider on custom paths, keep both the client's `RedirectURL` and the connect-intent target map aligned with your routes:

```go
callback := google.CallbackHandler(identity.Guard(), identity.Resolve)
web.Get("/sso/google", google.LoginHandler(), oauthauth.LoginOperation)
web.Get("/sso/google/callback", callback, oauthauth.CallbackOperation)
web.Post("/sso/google/callback", callback, oauthauth.CallbackOperation)
```

`identity.Resolve` supports existing connections and unverified-account takeover, but **never opens sign-up**. To allow sign-up on a hand-mounted callback, build a resolver from `identity.Links()` using `federation` from `arkoren.dev/ark/iam/federation` and pass it in place of `identity.Resolve`:

```go
resolve, err := identity.Links().Resolver(func(resolver *federation.ResolverBuilder) {
	resolver.SignUp(true)
})
```

Finally, mount the connect handler with an explicit target map so `POST /connections/{provider}` redirects to your custom login path (and update your front-end navigation target to match):

```go
targets := map[string]string{"google": "/sso/google"}
web.Post("/connections/{provider}", identity.Links().ConnectHandler(identity.Guard(), targets), federation.ConnectOperation)
```

For the full list of handler methods and operations, see the [`iam` API reference](https://pkg.go.dev/arkoren.dev/ark/iam).
