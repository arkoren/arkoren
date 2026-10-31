---
title: Securing MCP
description: Authenticate callers before dispatch and enforce TLS, Host, Origin, and remote OAuth boundaries outside the MCP adapter.
order: 40
---

# Securing MCP

## Introduction

The [first server](first-server.md) exposes a fixed status result on loopback.
Private tools need an authenticated caller before protocol dispatch. Keep that
boundary in your existing HTTP service: `mcpserver` verifies no credentials and
issues no tokens. Tool availability and resource authorization build on this
identity; neither replaces HTTP admission.

The snippets use `auth` from `arkoren.dev/core/http/auth`, `gate` from
`arkoren.dev/core/http/auth/gate`, `router` from `arkoren.dev/core/http/router`,
`cors` from `arkoren.dev/core/http/middleware/cors`, `csrf` from
`arkoren.dev/core/http/middleware/csrf`, and standard packages `context` and
`net/http`. `User` is your application's existing identity type.

## Require your typed application user

Use your existing [bearer authenticator](../authentication/api-tokens.md).
Here `verifyAccessToken` is your application's verifier with signature
`func(context.Context, string) (User, error)`. It validates the credential and
resolves your application `User`, not merely decoded claims. Authenticate requests
before dispatching to the checked `endpoint` from
[MCP Tools](tools.md#register-and-mount-explicitly):

```go
routes := router.New()
routes.Use(auth.Middleware(auth.Bearer(verifyAccessToken)))
routes.Use(auth.Require[User]())
routes.Mount("/mcp", endpoint)
```

Serve `routes` using your existing server lifecycle. Configure middleware before
mounting. `auth.Middleware` stores the
verified `User` but permits anonymous requests when credentials are absent;
`auth.Require[User]()` rejects those requests. Invalid credentials fail
authentication. The router mount and adapter preserve the request context for
`auth.FromContext[User]`, availability policies, and handlers.

Use the same concrete identity type everywhere. `User`, `*User`, and a separate
identity type do not share an authentication slot; a mismatched lookup fails
closed. If you already use `net/http` authentication middleware, keep it: reject
anonymous requests and inject the verified user with `auth.NewContext` before
calling the endpoint. Do not introduce another bearer parser solely for MCP.

## Restrict admission when needed

If only some authenticated users may reach MCP at all, add an existing Gate rule
after authentication and before `Mount`:

```go
routes.Use(gate.Require(canUseMCP))
```

`canUseMCP` has signature `func(context.Context, User) (bool, error)`.
This endpoint-wide decision does not authorize individual tools or records.
Continue with [Availability and Authorization](availability-and-authorization.md)
for plan/administrator capabilities and ownership checks in shared services.

## Protect credentials and browser requests

Keep private tools on a private network or loopback listener. Use TLS whenever
credentials cross a network. Before public deployment, enforce an allowlist of
external Host values and validate Origin at the outer HTTP boundary, using your
existing gateway or application middleware.

For example, if `allowedHost` is your existing middleware with signature
`func(http.Handler) http.Handler`, add it before the other middleware and before
mounting:

```go
routes.Use(router.HTTPMiddleware(allowedHost))
```

Bearer-only authentication does not need [CSRF protection](../browser-security/csrf-protection.md):
browsers do not attach the `Authorization` header automatically. MCP's Origin
validation is a separate transport boundary; keep it in your gateway or outer
HTTP middleware even when authentication uses bearer tokens.

If a trusted browser application calls MCP from another origin, configure
[CORS](../browser-security/cors.md) before authentication and mounting:

```go
routes.Use(cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins("https://app.example.com")
	policy.AllowedMethods(http.MethodPost)
	policy.AllowedHeaders("Accept", "Content-Type", "Authorization", "MCP-Protocol-Version")
}))
```

CORS answers preflights before authentication and lets that origin read
responses. It does not reject requests from disallowed origins, so it does not
replace the outer Origin policy.

If the endpoint also accepts session cookies, add CSRF protection before
authentication. Trust the same browser origin if it needs cross-origin access:

```go
routes.Use(csrf.Middleware(func(protection *csrf.Builder) {
	protection.TrustedOrigins("https://app.example.com")
}))
```

Unlike bearer tokens, cookies can accompany forged browser requests. CORS alone
does not stop those requests from changing state.

The pinned SDK's localhost DNS-rebinding protection remains enabled: when the
local connection address is loopback, a non-loopback Host receives 403. This is
not a public-host allowlist. Behind a proxy, decide which component validates
the external Host and Origin, and trust forwarded headers only from that proxy.
The adapter does not enable the SDK's optional cross-origin protection; keep
your outer policy in place.

## Bound request work

Keep listener read/header/write/idle timeouts and set execution deadlines in
outer middleware or services. The adapter has no tool-duration setting. Pass
request context into dependencies: write timeouts and disconnected clients are
not universal cancellation guarantees, and cancellation does not roll back
completed effects. The [transport reference](tools.md#transport-and-protocol-versions)
defines body limits and protocol-version-dependent disconnect behavior.

## Recognize the remote OAuth boundary

The bearer example assumes a client already has a valid token. It is not a
complete remote MCP OAuth deployment. Remote clients can also require protected
resource metadata and OAuth bearer challenges. Publish those separately and
use an authorization server that issues tokens for your resource.

`oauthauth.Client.Verify` in `arkoren.dev/driver/oauth/oauthauth` verifies OIDC
bearer access tokens with configured audiences and returns `oauthauth.Identity`.
Map that identity to your application `User` when your policies expect `User`;
the [OIDC bearer guide](../authentication/oidc-bearer.md) explains the
verification boundary. This driver is not a token issuer or complete MCP
authorization integration.

For native SDK OAuth integration, the pinned SDK provides `auth.RequireBearerToken`
and `auth.ProtectedResourceMetadataHandler` in
`github.com/modelcontextprotocol/go-sdk/auth`. Configure a real token verifier,
the resource metadata, and its authorization servers. These are outer HTTP
components, not `mcpserver.Builder` settings. Preserve their required challenges.

Before managed dispatch, map verified `TokenInfo` from `TokenInfoFromContext` to
your application user. SDK token information alone does not populate
`core/http/auth`'s typed identity. The SDK requires nonzero token expiration by
default. If endpoint admission requires OAuth scopes, configure them in
`auth.RequireBearerTokenOptions.Scopes`; advertising supported scopes in resource
metadata does not enforce them. Neither endpoint scope checks nor metadata
authorize access to an individual invoice: keep those checks in the shared
application service.

The [CLI connection guide](../cli/connecting.md)
shows how to supply an existing bearer token. The CLI does not log in or refresh
tokens for you.

For server construction and exact transport behavior, see the
[MCP server API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver); for typed tool policies,
see the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp).
