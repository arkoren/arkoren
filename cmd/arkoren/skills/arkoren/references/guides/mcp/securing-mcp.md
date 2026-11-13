---
title: Securing MCP
description: Protect the MCP HTTP boundary with verified identity, TLS, Host and Origin policy, and deliberate OAuth integration.
order: 40
summary:
  - Protect the HTTP endpoint with TLS, Host and Origin policy, and a verified typed user.
  - Distinguish endpoint admission, browser CORS, and cookie CSRF protection.
  - Bound execution and integrate remote OAuth without exposing private admission diagnostics.
---

# Securing MCP

A private MCP tool needs an authenticated caller before protocol dispatch.
`mcpserver` verifies no credentials and issues no tokens: keep admission in your
HTTP service. Tool availability decides which operations to offer after admission;
shared application services still authorize the records those operations use.

The [first server](first-server.md) intentionally exposes only a fixed public
label on loopback. Do not replace that result with private data without adding
the boundaries below.

## Transport Boundary

Keep private tools on a private network or loopback listener, and use TLS whenever
credentials cross a network. Before remote deployment, enforce allowed external
Host values and validate Origin using your gateway or outer HTTP middleware.
Bearer authentication does not replace Origin validation.

The pinned SDK enables localhost DNS-rebinding protection: requests arriving
through a loopback local address with a non-localhost Host receive 403. This is
not a public-host allowlist. The adapter does not enable the SDK's optional
cross-origin protection, so your outer Origin policy remains necessary.

With `router` imported from `arkoren.dev/core/http/router`, adapt your existing
`boundary func(http.Handler) http.Handler` before authentication and mounting:

```go
routes := router.New()
routes.Use(router.HTTPMiddleware(boundary))
```

Here `boundary` is application-owned middleware enforcing the deployment's
Host and Origin policy, not an Arkoren API. Behind a proxy, establish which
component validates the external values and trust forwarded headers only from
that proxy. A private proxy address alone does not make client headers trusted.

## Typed Authentication

Use your existing [bearer verifier](../authentication/api-tokens.md) to resolve
the application's user, not merely decode claims. Assume `User` is your user
type and `verifyAccessToken` has signature
`func(context.Context, string) (auth.Identity, error)`. It attaches the `User`
to the identity's record when policies need it.

Import `arkoren.dev/core/auth` and add authentication to the router above
before mounting the checked `endpoint` from [tool registration](tools.md#endpoint-registration):

```go
routes.Use(auth.Middleware(auth.Bearer(verifyAccessToken)))
routes.Use(auth.Require())
routes.Mount("/mcp", endpoint)
```

`auth.Middleware` stores a verified `auth.Identity`, but permits anonymous requests when
credentials are absent. `auth.Require()` rejects those requests. Invalid
credentials fail authentication. Configure middleware before `Mount` and serve
the router through your existing HTTP lifecycle.

The mount and adapter preserve request context for policies and handlers.
`auth.FromContext` returns the identity, and `auth.UserFromContext[User]` must use
the exact concrete type stored in its record at admission: `User` and `*User`
are different types. Mismatched lookups fail closed.

If existing `net/http` middleware already verifies callers, retain it. Reject
anonymous requests and inject the verified `auth.Identity` with `auth.NewContext`
before calling the endpoint rather than adding another MCP-specific bearer
parser.

## Endpoint Admission

Authentication need not grant everyone access to MCP. If your application has
an endpoint-wide rule, import `arkoren.dev/core/auth/gate` and insert it
after authentication but before the mount:

```go
routes.Use(gate.Require(canUseMCP))
```

`canUseMCP` is your existing `func(context.Context, User) (bool, error)` rule.
This admits a caller to the whole endpoint; it does not authorize individual
tools or records. [Capability Access](availability-and-authorization.md) handles
those separate decisions.

## Browser Access

For a trusted browser application on another origin, configure
[CORS](../browser-security/cors.md) before authentication and mounting. With
`cors` from `arkoren.dev/core/http/middleware/cors` and standard `net/http`:

```go
routes.Use(cors.Middleware(func(policy *cors.Builder) {
	policy.AllowedOrigins("https://app.example.com")
	policy.AllowedMethods(http.MethodPost)
	policy.AllowedHeaders("Accept", "Content-Type", "Authorization", "MCP-Protocol-Version", "Mcp-Method", "Mcp-Name")
}))
```

This is inserted before the authentication block, not after the completed
mount. CORS answers preflights and lets the trusted browser read responses;
it does not reject requests from disallowed origins. Keep the outer Origin
policy. The `Mcp-Method` and `Mcp-Name` headers support newer protocol clients.

A bearer-only endpoint does not rely on browser-attached credentials, so it
does not need cookie CSRF protection. If the endpoint also accepts session
cookies, add [CSRF protection](../browser-security/csrf-protection.md) before
authentication. Import `arkoren.dev/core/http/middleware/csrf` and trust only
the browser origin that needs access:

```go
routes.Use(csrf.Middleware(func(protection *csrf.Builder) {
	protection.TrustedOrigins("https://app.example.com")
}))
```

Cookies can accompany forged browser requests, so CORS alone does not prevent
their effects. If you intentionally permit credentialed cross-origin browser
requests, also configure `policy.AllowCredentials(true)` in the CORS callback
and an appropriate cookie policy; never combine credentials with a wildcard
origin.

## Execution Bounds

Retain listener header/read/write/idle timeouts and set operation deadlines in
outer middleware or application services. The adapter has no tool-duration
option. Pass request context into dependencies: a write timeout or client
disconnect is not a universal guarantee that work stops, and cancellation
does not undo completed effects.

The [transport contract](tools.md#transport-behavior) specifies the 4 MiB body
limit and protocol-dependent disconnect cancellation. Configure any stricter
body limit at your gateway or HTTP boundary. Managed error redaction protects
tool responses, not arbitrary admission responses or application logs.

## Remote OAuth

The bearer example assumes a client already has a token. A remote MCP OAuth
deployment also needs protected resource metadata, bearer challenges, and an
authorization server issuing tokens for this resource. A login flow or a
token verifier alone is not that complete integration.

`oauthauth.Client.Verify` from `arkoren.dev/ark/oauth/oauthauth` verifies
OIDC bearer access tokens with configured audiences and returns
`oauthauth.Identity`. Map it to your application `User` if your policies expect
that type; the [OIDC bearer guide](../authentication/oidc-bearer.md) owns verifier
configuration and lifecycle. The adapter is neither a token issuer nor a complete
MCP authorization server.

For native integration, the pinned SDK provides `RequireBearerToken` and
`ProtectedResourceMetadataHandler` in
`github.com/modelcontextprotocol/go-sdk/auth`. These are outer HTTP components,
not `mcpserver.Builder` settings. Configure a real token verifier, accurate public
resource metadata, and authorization-server URLs. Preserve required
`WWW-Authenticate` challenges rather than replacing them with ordinary JSON
authentication errors.

SDK `TokenInfo` is separate from Arkoren's identity context. Before managed
dispatch, read it with `TokenInfoFromContext`, resolve your application user,
and inject an `auth.Identity` carrying that user with `core/auth.NewContext`.
Attach the exact user type your tool policies use. The SDK requires nonzero token expiration by
default. Enforce endpoint scopes with `RequireBearerTokenOptions.Scopes`;
advertising supported scopes in metadata does not enforce them.

The SDK's bearer middleware returns verifier error text in HTTP failure
responses. Return only approved public errors from that verifier and retain
private diagnostics in protected local logs. Managed ToolError redaction does
not apply to this earlier boundary. Resource metadata is public configuration,
not a place for credentials; the SDK metadata handler does not validate its
fields.

Neither endpoint scopes nor metadata authorize a particular shipment or invoice.
Keep resource checks in shared services. The [CLI connection guide](../cli/connecting.md)
shows how to supply an existing token; the CLI does not log in or refresh it.

For endpoint settings, see the [MCP server API](https://pkg.go.dev/arkoren.dev/ark/mcp/mcpserver);
for tool policy declarations, see the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp).
