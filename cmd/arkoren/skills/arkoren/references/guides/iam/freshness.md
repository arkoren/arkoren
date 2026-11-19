---
title: Fresh Logins
description: Distinguish a session, a verified email, a fresh credential proof, and a new login before allowing sensitive actions.
order: 70
summary:
  - Understand the independent checks that protect credential changes.
  - Confirm a stale session with an enrolled method, or start a new login when confirmation cannot help.
  - Apply the same checks to application routes without overstating what provider sign-in proves.
---

# Fresh Logins

An authenticated session says who logged in, not whether the person holding
the cookie can still prove that identity. Before issuing or changing
credentials, IAM therefore checks more than ordinary login. Understanding
these checks lets your front end recover from a `403` without discarding a
session unnecessarily.

## Independent Checks

The credential routes distinguish four questions:

- **Session:** did the caller authenticate through the guard's session rather
  than an API token? Credential-management routes reject bearer identities with
  `401`, even when the token is valid.
- **Verified email:** has the account proved its address? Adding a provider,
  authenticator, or token requires this proof. Login itself does not.
- **Fresh proof:** did the user recently prove an enrolled password or
  authenticator? A confirmation can renew this.
- **Recent login:** was this application's login record created recently?
  Confirmation cannot renew it.

These checks do not replace application authorization. A fresh user still
needs permission to perform the requested business action.

## Renew a Proof

A **step-up method** is a credential the user can present again without
starting a new login. IAM registers passwords (`pwd`), then authenticator
codes (`otp`), when those features are enabled. Only methods the user has
actually enrolled count.

By default, a proof of one of those methods stays fresh for 15 minutes.
When it is stale, a credential route returns
`tag:arkoren.dev,2026-11:core:auth:step-up-required` with the enrolled
methods in `methods`. Ask the user for one, submit it to `POST /confirm`,
then retry the action. For an authenticator:

```json
{
  "method": "otp",
  "code": "123456"
}
```

A correct confirmation returns `204`. A wrong code returns `403` and consumes
the guard's attempt budget. Recovery codes are not step-up methods: they are
accepted at a login challenge, not at `/confirm`.

To change the default freshness window, configure the guard during construction.
This uses `auth` from `arkoren.dev/core/auth` and standard-library `time`:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(true, hasher)
	identity.Guard(func(policy *auth.GuardBuilder) {
		policy.FreshFor(5 * time.Minute)
	})
})
```

If the user has **no** enrolled step-up method, any recent login factor can
make them fresh. This allows a provider-only user to enroll a first credential.
Once that factor is too old, the response is `403`
`tag:arkoren.dev,2026-11:core:auth:no-step-up-method`: there is nothing to
confirm, so the user logs in again.

## Require a New Login

Some operations should not turn an old session into a long-lived credential,
even after confirmation. The device-token exchange therefore requires a login
record created at most five minutes ago.

`POST /confirm` does not reset that age. Logging in again as the same user
inside an already authenticated session also preserves the original login
record. After `no-step-up-method` from `/device-tokens`, end that session and
perform a new login before retrying the exchange.

This check concerns the age of **your application's** login. It does not
prove that an external provider asked the person to type credentials.

## Credential Routes

Use this comparison when deciding what your account UI must ask for.
“Fresh” uses the configured guard window, normally 15 minutes.

| Action | Session | Proof required | Verified email |
| --- | --- | --- | --- |
| Change or set a password | Yes | Fresh | No |
| Set up or enable two-factor | Yes | Fresh | Yes |
| Disable two-factor or replace recovery codes | Yes | Fresh and a recent second factor; see below | No |
| Create a personal token | Yes | Fresh | Yes |
| Exchange a login for a device token | Yes | New login within five minutes | Yes |
| Connect a provider | Yes | Fresh | Yes |
| Disconnect a provider or delete the account | Yes | Fresh | No |
| List or revoke tokens, list connections, manage sessions | Yes | No freshness check | No |
| Verify email or request another verification | Yes | No freshness check | No |

Weakening two-factor additionally requires a recent authenticator or recovery
proof, within its own 15-minute window by default. For those two operations
only, a recently used recovery code also satisfies freshness. A password alone
is insufficient; [Two-Factor](two-factor.md#recover-or-disable) explains the
lost-device path.

With the built-in directory, verified-email checks are always installed.
With [your own users](own-users.md#verified-emails), they depend on the
`Users.Verified` function you supply.

The handlers enforce these checks themselves, including on custom paths.
The [route reference](routes.md) maps actions to their default URLs.

## Protect Application Routes

To require a session and a recent credential proof on your own billing route,
start from the authenticated `web` router in
[Overview](overview.md#register-routes):

```go
billing := web.With(auth.RequireSession(), identity.Guard().RequireFresh(5*time.Minute))
billing.Post("/billing", updateBilling)
```

`updateBilling` is your authorized application handler.
`RequireFresh` uses its explicit window, independently of the default
`FreshFor`; a zero or negative window admits no request.

If your operation requires a new login instead, check its age inside an
authenticated session handler, before the sensitive work:

```go
if err := identity.Guard().RecentLogin(r, 5*time.Minute); err != nil {
	return err
}
```

Do not continue after an error. `RecentLogin` checks the actual session login,
not just the factors on an identity.

## Provider Logins

External login records a `federated` factor, which is **not** a step-up method.
If a user has a password or authenticator, another provider login alone does
not refresh their credential-change permission: they confirm a registered
step-up method.

For a provider-only user, freshness comes from the federated factor's age.
The factor is stamped at the callback unless an ID token supplies an earlier
`auth_time`. An earlier claim can make the factor older; a missing or future
claim cannot make it newer than the callback.

This matters when an existing provider session approves sign-in silently.
Microsoft can supply `auth_time` through an
[optional ID-token claim](../oauth/providers.md#microsoft).
Do not rely on Google always supplying it; GitHub's OAuth2 flow has no ID
token. Review other OIDC providers' behavior before relying on their claims.

For a provider-only user who needs to authenticate again, navigate to
`/auth/google?fresh=1`, substituting the provider name.
`fresh=1` is preserved in the sealed login state and requests
`prompt=login&max_age=0`. It is a hint, not a guarantee that the provider
requests credentials; GitHub's OAuth2 flow cannot provide that assurance.

A new application login can still pass the device exchange after silent
provider approval. The recent-login check prevents renewing an old
application session; it does not establish the provider's authentication
ceremony.

Next, add [Two-Factor](two-factor.md) when users need an authenticator and
recovery codes.

For freshness and session checks, see the
[`auth` API](https://pkg.go.dev/arkoren.dev/core/auth) and the composed
[`iam` API](https://pkg.go.dev/arkoren.dev/ark/iam).
