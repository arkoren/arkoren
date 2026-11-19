---
title: Browser Login
description: Connect the login and callback routes, map provider identities to local accounts, and handle second factors, step-up, failures and events.
order: 20
summary:
  - Mount the login and callback routes on a session-enabled group and understand what a successful callback does.
  - Link local accounts by issuer and subject, never by email, and hand a second factor to its challenge page.
  - See how a federated login interacts with step-up, how login state is protected, and how failures and events surface.
---

# Browser Login

A browser login sends the person to the provider, then returns them to your
callback, where the ark verifies the provider identity and logs the matching
local account in. This page assumes a configured and started `client` from
[OAuth Overview](overview.md#configure-clients).

## Connect Routes

Use an existing session-enabled, CSRF-protected browser group `web` and the
`guard` from [Enable Login](../authentication/session-login.md#enable-login), which
already runs `auth.Middleware(guard.Authenticator())`. Import `auth` from
`arkoren.dev/core/auth`. The mapper `mapIdentity`, written in the next section,
is an `auth.Resolver`: it returns the local account's subject.

```go
web.Get("/login", client.LoginHandler())
web.Get("/oauth/callback", client.CallbackHandler(guard, mapIdentity))
web.Post("/logout", guard.LogoutHandler(func(logout *auth.LogoutBuilder) {
	logout.Redirect("/")
}))
```

`LoginHandler` redirects to the provider. A successful callback verifies the
provider identity, maps it to a subject, calls `guard.Begin` with a federated
factor, rotates the session ID and redirects to the saved local destination.
Later requests use the normal authenticator.

Core's `guard.LogoutHandler` owns the logout route. It calls `guard.Logout`,
which also ends a pending second-factor login, and redirects to `/`. It does not
sign the person out at the provider, and the client has no logout of its own.
Keep logout CSRF-protected.

If a user who is already logged in as the mapped subject signs in through the
provider, to connect it or to re-authenticate, `Begin` adds the federated factor
to their session and does not ask for a second factor again.

A nil guard or mapper is a configuration error, not a rejected credential. A
request without a session returns `401` before the state is consumed or the
provider is contacted. If you use
[custom session selection](../authentication/session-login.md#select-sessions), build
the guard with the same handle as the session middleware. The session middleware
still owns [response-time persistence](../http-capabilities/sessions.md#save-changes):
a successful callback mutation does not prove that storage succeeded or that the
old session ID was retired.

## Link Local Accounts

> [!NOTE]
> With the [IAM](../iam/overview.md) ark you write no mapper:
> `identity.Provider(client)` mounts these routes and maps identities through its
> connection store, with connect and disconnect, never linking by email. It also
> sets whether a first login may create a user, for each provider separately. See
> [Federated Sign-In](../iam/federated-sign-in.md). The rest of this section shows
> how to write the mapper yourself.

Identify an external account by the pair `Issuer` and `Subject`, not by email
or display name. Require an existing link for ordinary sign-in, and create
accounts only under your explicit registration policy. To link another provider
to an existing account, require proof of control of that local account as a
separate, authorized action.

With standard-library `context`, this application mapper looks up an existing
link using `accounts.ByIdentity(ctx, issuer, subject)`, which returns the
application's `Account` or an error, and converts the account to its subject
with the user `driver` from
[Session Login](../authentication/session-login.md#load-accounts):

```go
mapIdentity := func(ctx context.Context, external auth.External) (string, error) {
	if external.Issuer == "" || external.Subject == "" {
		return "", auth.ErrUnauthenticated
	}
	account, err := accounts.ByIdentity(ctx, external.Issuer, external.Subject)
	if err != nil {
		return "", err
	}

	return driver.Subject(account)
}
```

Your repository owns the uniqueness of the issuer and subject link and the
rejection of unknown accounts. The callback never uses the provider's `Subject`
as the login subject: it identifies the person at the provider, not your account.
The user provider checks the returned subject on `Begin`: an unknown or disabled
account is `auth.ErrUnauthenticated` (`401`), and an invalid subject returns
`auth.ErrInvalidSubject`, so the mapper needs no disabled-account check. Mapper
errors matching `auth.ErrNoCredentials` become `ErrInvalidCallback` (`400`);
other application errors pass through unchanged.

> [!WARNING]
> Never match or merge accounts by email alone. Some providers pass through
> unverified email claims, and even verified addresses can change. An automatic
> email match can give an attacker another person's existing local account.

`external.Email` is optional profile data, and `external.EmailVerified` says
whether the provider vouched for it. The [provider rules](providers.md) set both
differently:

- Google, Microsoft and GitHub keep the email only when they can vouch for it, and
  leave it empty otherwise.
- Generic OIDC blanks the email and sets `EmailVerified` to false, whatever the
  token says, unless you set `TrustEmail(true)` and the `email_verified` claim is
  the boolean `true`.
- Apple passes the email through, with `EmailVerified` taken from the
  `email_verified` claim, which it accepts as the boolean `true` or the string
  `"true"`.
- Generic OAuth2 uses whatever your user-info function returns.

Link by email only when `EmailVerified` is true and your policy allows it, and
still prefer the issuer and subject link. The display name is
`external.Claims["name"]`, untrusted text to escape when rendering.

For browser callbacks, `external.Claims["scope"]` holds the granted scopes as a
space-delimited string from the token response, or the requested scopes if the
response omits them. These are provider grants, not your local account's
resource permissions.

## Provider Claims

The verified identity reaches your resolver as `auth.External`. Its `Claims` map
holds what the provider sent beyond the email:

| Provider | Typical claims |
| --- | --- |
| Google | ID-token claims such as `name`, `given_name`, `family_name`, `picture`, `locale` and `hd` |
| Microsoft | ID-token claims such as `name` and `preferred_username`, plus `oid` and `tid` |
| GitHub | `name`, falling back to the login |
| Apple | `name`, on the first login only |

Apple sends the user's name only on the first authorization, so store it then;
a later login carries no `name`. With IAM, `ProviderBuilder.KeepClaims` does this
for you and keeps the name through later logins: see
[Keep Claims](../iam/federated-sign-in.md#keep-claims). Claims are the
provider's suggestions, not proof of anything beyond the verified identity.

## Second Factors

If the `guard` has a registered
[second-factor check](../authentication/two-factor.md#hold-a-login) for the mapped
account, `Begin` holds the login as pending and the request stays anonymous. Set
the page that asks for the second factor with `ClientBuilder.ChallengeURL`, a
local path, and mount core's
[challenge route](../authentication/two-factor.md#verify-the-second-factor) there:

```go
client, err := oauth.New(oauth.Google(), cipher, clientID,
	func(login *oauth.ClientBuilder) {
		login.ClientSecret(clientSecret)
		login.RedirectURL("https://app.example.com/oauth/callback")
		login.ChallengeURL("/login/challenge")
	},
)
```

The callback then redirects to that path without publishing `LoginSucceeded`.
The `return_to` target is not carried over, so your challenge page chooses the
destination once the challenge route has logged the user in. `New` rejects a
challenge URL that is not a safe local path. Without one, the callback ends the
pending login and fails with `ErrNoChallengeURL` (`500`); it never signs the
person in without the second factor.

## Federated Logins and Step-Up

A federated login is one factor of kind `auth.KindFederated` and method
`oauth.MethodFederated`, and never multi-factor on its own. The server's clock
stamps it at the callback, unless the ID token carries an earlier `auth_time`
claim: the factor then takes that time, so a provider can make the login older but
never fresher, and a token without `auth_time` is not an error and keeps the
callback time. Microsoft Entra sends `auth_time` only when you add it as an
optional claim; do not count on one from Google, and GitHub has no ID token. `/auth/{name}?fresh=1` asks an OIDC
provider to authenticate the user again (`prompt=login`, `max_age=0`) and records
the request in the sealed login state; the provider may ignore it. The factor is
not a step-up method unless you register one, so core's
[recent-proof rule](../authentication/two-factor.md#require-recent-proof) applies:

- A user with a registered step-up method (password, TOTP) who signs in again
  silently through the provider is not made fresh by it. `guard.RequireFresh`
  asks them to [confirm](../authentication/two-factor.md#handle-step-up) with that
  method.
- A user with no step-up method is fresh right after any login, including a
  federated one, and must log in again when the window passes.
- To require a genuinely new login, for example before exchanging a session for
  a device token, call `guard.RecentLogin(r, 5*time.Minute)`. Browser and native
  federated logins satisfy it; a silent re-login of an open session does not.

Sending the user to `/login?return_to=/path&fresh=1` adds `prompt=login` and
`max_age=0` to an OIDC provider's authorization request, which asks it to prompt
for credentials again. It is only a hint: the provider may ignore it, and the
callback does not check the answer. OAuth2 providers ignore the parameter.

## Protect Login State

To return a person to an invoice after login, start the flow with a local path:

```text
/login?return_to=/invoices/42
```

The client validates this target before redirecting to the provider, seals it
with the login state, and uses a safe redirect after the callback. Unsafe
targets return `ErrInvalidLoginRequest` (`400`). Do not bypass this path by
redirecting to another unvalidated request parameter.

Each login gets random state, an OIDC nonce when applicable, and a PKCE verifier
when enabled. State binds the callback to the browser login, and the nonce binds
the ID token to that attempt. These values live in an encrypted, HttpOnly cookie
for ten minutes, not in the application session. The client keeps room for at
most five live login cookies, which allows parallel tabs.

Callbacks reject missing, tampered, expired or mismatched state cookies. For
OIDC they also check the ID token's signature, expiry, issuer, the positional
client-ID audience, a nonempty final subject and the saved nonce before mapping.
Plain OAuth2 relies on the authenticated user API instead of an ID token. Cookie
deletion is browser cleanup, not a server-side replay registry: a copied cookie
survives until expiry, so the provider must enforce one-time authorization codes.

Cookies use `Secure` and the `__Host-` prefix by default. Use HTTPS for login
and for registered callbacks. `ClientBuilder.AllowInsecure(true)` removes both
protections for plain-HTTP development only; never enable it in production.
Apple's cross-site cookie requires HTTPS even during development.

## Handle Failures

Invalid state, failed ID-token checks, provider callback errors, and a token
endpoint's native `invalid_grant` rejection return `ErrInvalidCallback` (`400`).
Start a fresh login rather than replaying an invalid, expired or consumed code.
Other exchange failures return `ErrProviderUnavailable` (`502`). Returned errors
retain their native causes for explicit inspection; provider response text and
claims can be sensitive, so do not dump those chains into logs.

Discovery errors retain native causes and, when applicable, context cancellation
or deadline identity. The bearer-only
[verification path](bearer-tokens.md#handle-verification) has additional context
checks; do not assume every browser callback or local policy denial behaves the
same way.

## Events

If you need outcome events, configure `ClientBuilder.Events` with your existing
[event bus](../events/overview.md). A nil bus publishes nothing. Completed logins
publish the external issuer and subject and the local subject, which can
identify a person; a login held for a second factor publishes nothing until the
challenge route completes it. `guard.LogoutHandler` publishes `auth.LoggedOut`
with the subject when configured with `LogoutBuilder.Events`; it reports local
mutation, not provider logout or durable session persistence.

`LoginFailedEvent.Err` contains `ErrInvalidCallback`, `ErrProviderUnavailable`
or an opaque local-login classification, without the original cause.
`ProviderError` is an untrusted, allowlisted RFC 6749 callback hint; unknown
values and arbitrary descriptions are omitted. Configuration, not-started and
missing-session exits before the cookie is consumed publish no callback
outcome. Do not classify opaque errors by matching their text. Returned errors
keep their separate diagnostic chains, and event context values are not
sanitized by these payload limits.

Ordinary `fmt` formatting redacts owned client and draft configuration, and
`Config` formatting redacts a nonempty client secret. This is not a general
serialization or logging guarantee: arbitrary claims, native objects, callback
captures and explicitly unwrapped errors remain outside that boundary.

Continue with [Providers](providers.md) for each provider's registration and
identity rules, or see the
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth).
