---
title: OIDC Tokens
description: Verify provider-issued JWTs for an API and resolve the exact request identity your handlers need.
order: 50
summary:
  - Configure API audiences and discovery before accepting provider-issued JWTs.
  - Understand signature, issuer, key retrieval, clock, and token-purpose verification boundaries.
  - Require the exact external or local identity type and preserve safe failure handling.
---

# OIDC Tokens

An API can accept an existing JWT issued by a trusted OpenID Connect provider
without browser routes or a session. `oauthauth.Client.Verify` validates the
token and returns an external identity; `auth.Bearer` connects that result to
request authentication.

The provider or a separate client flow acquires and refreshes tokens. This
adapter does not implement a token endpoint, authorization server, opaque-token
introspection, or provider revocation checks. Plain OAuth2 adapters, including
GitHub, cannot use `Verify`. For browser sign-in, use [OAuth Login](oauth.md).

## Prepare Verification

Register an audience intended for this API with your provider. An **audience**
names a token's intended recipient. Prefer separate API and browser-client
audiences; do not accept a browser client ID merely because it is convenient.
Use a trusted issuer from application configuration, never from unverified
request claims.

Install `arkoren.dev/ark/oauth` as shown in
[OAuth client setup](oauth.md#configure-clients). With an existing
`cipher *crypto.Encrypter` from [Encryption](../encryption/overview.md) and your
registered `clientID`, configure the verifier:

```go
import oauth "arkoren.dev/ark/oauth/oauthauth"

client, err := oauth.New(
	oauth.OIDC("https://id.example.com/realms/invoices"),
	cipher,
	clientID,
	func(verification *oauth.ClientBuilder) {
		verification.Audiences("https://api.example.com/invoices")
	},
)
```

Handle the constructor error before use. The API-only path still requires a
non-nil encrypter and nonempty client ID because they are constructor inputs;
it needs no redirect URL or client secret. `Audiences` is required by `Verify`.
Without it, verification returns `ErrNoAudiences` rather than defaulting to the
browser client ID.

Construction performs no network I/O. Register `client.Start` in your existing
application composer, importing `arkoren.dev/core/application`:

```go
compose := func(composer *application.Composer) {
	composer.Startup(client.Start)
}
```

Startup discovers endpoints and prepares the verifier. Until it succeeds,
verification returns `ErrNotStarted`. Key retrieval can occur later during
verification, so successful discovery does not prove every request can reach
the signing-key endpoint. Keep the returned client pointer; it owns no stop
hook and does not stop its shared encrypter.

## Set Verification Boundaries

`Verify` checks the JWT signature, expiry, trusted issuer, and whether at least
one token audience matches a configured API audience. It requires a nonempty
final subject after any provider-specific mapping. Google and Microsoft apply
their [preset identity checks](oauth.md#choose-providers); generic OIDC does not
infer tenant restrictions or email-verification policy.

The discovered provider supplies signing keys through its JWKS endpoint, a
published set of verification keys. The underlying OIDC verifier caches keys
and can fetch them during signature verification. Accepted algorithms come
from supported discovery metadata, with an RS256 fallback when none are set;
the token header alone does not define the trust policy. Arkoren's builder does
not expose a custom algorithm allowlist or native-verifier configuration. Use a
different application verifier when your policy needs controls this adapter
does not expose.

Time checks use the service's wall clock. The current native verifier rejects
expired tokens and allows five minutes of tolerance for a future `nbf`
(not-before) claim; that is not a general five-minute expiry grace period.
There is no clock or skew setting on `ClientBuilder`. Keep service clocks
synchronized, and do not assume signature/expiry checks enforce a maximum token
age or every provider's additional time policy.

> [!WARNING]
> Audience and signature checks do not prove a JWT is an API access token.
> `Verify` rejects any payload `nonce` claim and a payload `typ` equal to `ID`
> case-insensitively, but an unmarked JWT with an accepted audience can pass.
> Enforce your provider's access-token purpose policy before granting API access.

These marker checks are heuristics, not a universal access-token profile; the
adapter does not require every provider to use `at+jwt`. If additional signed
claims must be checked, wrap `client.Verify` with your application checks or
use a provider-specific verifier. A successful verification still needs
[authorization](../authorization/overview.md).

Local JWT verification is not an online check that the provider still accepts
the token or account. A token can remain acceptable until expiry even after
provider-side revocation. A current local-account lookup can enforce your own
disabled-account policy, but cannot substitute for provider introspection.

## Require Identity

When handlers work directly with the provider identity, wrap `client.Verify` so
the verified `oauth.Identity` rides in the `auth.Identity` record. Import `auth`
from `arkoren.dev/core/auth` and `request` from `arkoren.dev/core/http/request`.
With your existing API router group `api`:

```go
verifyExternal := func(ctx context.Context, token string) (auth.Identity, error) {
	external, err := client.Verify(ctx, token)
	if err != nil {
		return auth.Identity{}, err
	}

	return auth.NewIdentity(external.Subject, func(principal *auth.IdentityBuilder) {
		principal.Scopes(external.Scopes...)
		principal.Record(auth.UserRecord{Subject: external.Subject, Value: external})
	}), nil
}

api.Use(auth.Middleware(auth.Bearer(verifyExternal)))
api.Use(auth.Require())
```

This assumes the audience and purpose policy above is sufficient for your
provider. Otherwise call your stricter application verifier inside the wrapper.
A provider subject is unique only within its issuer and may not satisfy
`auth.ValidateSubject`; map it to a local account, as below, before other
packages key on the identity's subject. Authentication middleware alone permits missing credentials;
`Require` rejects that anonymous path. Invalid bearer credentials produce `401`
with `WWW-Authenticate: Bearer error="invalid_token"`.

In a handler, read the external identity from the record:

```go
external, err := request.User[oauth.Identity](r)
if err != nil {
	return err
}
```

`external.Issuer` and `external.Subject` identify the external account.
`*oauth.Identity` and your local `User` are different record value types, not
alternate views of this value. `Claims`, `Email`, and `Name` remain provider-controlled
data; do not log arbitrary claims or use email to link local accounts.

## Load Local Accounts

If handlers need current local permissions, map the verified issuer/subject
pair to an existing account. Keep token scopes request-local instead of mutating
a shared cached account. For example:

```go
type User struct {
	ID int
}
```

With standard-library `context` and `strconv`, `accounts.ByIdentity` returning
your application's `Account`, and a `client` satisfying your provider's purpose
policy, use:

```go
verifyUser := func(ctx context.Context, token string) (auth.Identity, error) {
	external, err := client.Verify(ctx, token)
	if err != nil {
		return auth.Identity{}, err
	}
	account, err := accounts.ByIdentity(ctx, external.Issuer, external.Subject)
	if err != nil {
		return auth.Identity{}, err
	}
	if account.Disabled {
		return auth.Identity{}, auth.ErrUnauthenticated
	}

	subject := strconv.Itoa(account.ID)

	return auth.NewIdentity(subject, func(principal *auth.IdentityBuilder) {
		principal.Scopes(external.Scopes...)
		principal.Record(auth.UserRecord{Subject: subject, Value: User{ID: account.ID}})
	}), nil
}
```

The repository must reject unknown links with an error other than
`auth.ErrNoCredentials`. That sentinel would permit credential fallback, not
reject the presented token. An identity returned with nil error counts as a success.
Do not create or merge accounts automatically from the email claim; see
[local-account linkage](oauth.md#link-local-accounts).

Wire the mapped verifier, not the external one:

```go
api.Use(auth.Middleware(auth.Bearer(verifyUser)))
api.Use(auth.Require())
```

Scope checks and resource permissions belong in
[Authorization Policies](../authorization/policies.md). The token's scopes travel
on the `auth.Identity`, so `gate.HasScopes` checks them for the mapped identity.
Token scopes limit the presented credential's authority rather than
replacing the account's resource permissions.

## Handle Verification

Direct `Verify` calls preserve their error identity and native causes. After
unsupported-provider and missing-audience checks, an ended context is rejected
with `context.Canceled` or `context.DeadlineExceeded`, including when cached or
local keys could otherwise succeed. A final context check also prevents identity
publication after cancellation during verification. These checks do not promise
that every underlying key-fetch operation stops with the request.

Through `auth.Bearer`, verifier errors become an invalid-token HTTP response,
without exposing the error message to the client. The attached cause can still
reach diagnostics. Native errors can contain provider-controlled claims, so
inspect deliberately instead of logging entire chains. Optional
[token-rejection events](oauth.md#events) contain a bounded opaque error instead;
they do not retain the native cause.

One started OIDC client can handle browser callbacks and APIs, but each uses
its own audience rule: callbacks require the positional client ID, while
`Verify` uses configured API audiences. For MCP, apply the same bearer and
required-user middleware outside the mount and follow
[MCP endpoint security](../mcp/securing-mcp.md) for challenges, admission, and
tool/resource authorization. The CLI sends existing credentials; it does not
acquire or refresh them.

For all functions and options, see the
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth).
