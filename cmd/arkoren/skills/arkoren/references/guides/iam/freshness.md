---
title: Step-Up & Freshness
nav: Freshness
description: Understand how IAM protects sensitive actions with session checks, verified emails, fresh credential proofs, and recent logins.
order: 40
summary:
  - Distinguish the four independent checks IAM applies before allowing credential changes.
  - Renew a stale session in place with POST /confirm instead of forcing a full logout.
  - Know which operations require a genuinely new login and how to apply the same guards to your own routes.
---

# Step-Up & Freshness

A valid session cookie proves that someone signed in earlier, not that the person at the keyboard right now can still prove their identity. If a laptop is left unlocked, an attacker with the cookie should not be able to change the password, disable two-factor authentication, or mint a long-lived API token.

IAM therefore layers targeted checks on top of ordinary authentication. Understanding these checks lets your front end prompt for a quick confirmation instead of discarding a healthy session, and lets you apply the same protection to sensitive routes of your own, such as billing or account exports.

## Four Independent Checks

Every credential-management route evaluates up to four separate questions:

- **Browser session**: Did the caller authenticate with a session cookie rather than an API token? All credential-management routes reject bearer tokens with `401`, even when the token is valid.
- **Verified email**: Has the account proved ownership of its email address? Connecting a provider, enrolling two-factor, and creating tokens require this; ordinary login does not.
- **Fresh proof**: Did the user recently prove an enrolled password or authenticator code? A stale session can renew this in place with `POST /confirm`.
- **Recent login**: Was this application's login record created within the last few minutes? Confirmation cannot renew it; the user must start a new login.

None of these checks replace application authorization. A freshly verified user still needs permission to act on a given resource.

## Renewing a Proof

A **step-up method** is a credential the user can present again without signing out. IAM registers passwords (`pwd`) and authenticator codes (`otp`) automatically when those features are enabled. Only methods the user has actually enrolled count.

By default, a proof stays fresh for **15 minutes**. When it expires, a protected route returns `403` with problem type `tag:arkoren.dev,2026-11:core:auth:step-up-required` and a `methods` array listing what the user can confirm. Prompt the user for one of those methods and submit it to `POST /confirm`:

```json
{
  "method": "pwd",
  "password": "correct horse battery"
}
```

Or with an authenticator code:

```json
{
  "method": "otp",
  "code": "123456"
}
```

A `204` response refreshes the session's proof; retry the original request. A wrong password or code returns `403` (not `422`) and consumes the guard's confirmation attempt budget. Recovery codes are accepted only at a login challenge, never at `POST /confirm`.

To shorten or lengthen the default freshness window, configure the guard during construction using `auth` from `arkoren.dev/core/auth`:

```go
identity.Guard(func(policy *auth.GuardBuilder) {
	policy.FreshFor(5 * time.Minute)
})
```

If a user has **no** enrolled step-up method (for example, a user who signed in only through Google), any recent login factor satisfies freshness so they can enroll their first password or authenticator. Once that factor ages out, IAM returns `403` `tag:arkoren.dev,2026-11:core:auth:no-step-up-method`: because there is no local credential to confirm, the user must sign in again.

## Requiring a New Login

Some operations should never convert an old session into a long-lived credential, even after confirmation. Exchanging a session for a [mobile device token](tokens.md#mobile-device-tokens) therefore requires a login record created at most **5 minutes ago**.

Neither `POST /confirm` nor signing in again inside the same active session resets the login record's creation time. If `POST /device-tokens` returns `403` `no-step-up-method`, end the current session and perform a brand-new login before retrying the exchange.

## What Each Credential Route Requires

Use this table to decide what your account settings UI should prompt for. "Fresh" uses the guard's freshness window (15 minutes by default).

| Action | Browser session | Proof required | Verified email |
| --- | --- | --- | --- |
| Change or set a password | Yes | Fresh | No |
| Set up or enable two-factor | Yes | Fresh | Yes |
| Disable two-factor or replace recovery codes | Yes | Fresh + recent second factor | No |
| Create a personal token | Yes | Fresh | Yes |
| Exchange a login for a device token | Yes | New login within 5 minutes | Yes |
| Connect an external provider | Yes | Fresh | Yes |
| Disconnect a provider or delete account | Yes | Fresh | No |
| List or revoke tokens, connections, or sessions | Yes | None | No |
| Verify email or resend verification | Yes | None | No |

Weakening two-factor authentication (disabling it or regenerating recovery codes) requires a recent authenticator or recovery-code proof in addition to freshness, so a stolen password alone cannot remove the second factor. See [Two-Factor Authentication](two-factor.md#disabling-or-regenerating-codes) for details.

These checks live inside the handlers themselves, so they still apply if you mount handlers on [custom paths](routes.md#custom-paths).

## Protecting Your Own Routes

You can enforce the same step-up boundary on sensitive routes in your own application. Attach `auth.RequireSession()` and `identity.Guard().RequireFresh(...)` to a route group derived from your authenticated `web` router:

```go
billing := web.With(auth.RequireSession(), identity.Guard().RequireFresh(5*time.Minute))
billing.Post("/billing", updateBilling)
```

`RequireFresh` enforces its own explicit window independently of `FreshFor`. Passing a zero or negative duration admits no requests.

When an action requires a genuinely new login rather than confirmation, call `RecentLogin` inside your handler before performing the sensitive work:

```go
if err := identity.Guard().RecentLogin(r, 5*time.Minute); err != nil {
	return err
}
```

Always return on error before continuing. `RecentLogin` inspects the underlying session login record rather than just the identity's factor list.

## External Providers and Freshness

Signing in through an external provider records a `federated` factor, which is **not** a step-up method. If a user already has a password or authenticator enrolled, another provider sign-in does not refresh their credential-change proof; they confirm one of their enrolled step-up methods instead.

For a provider-only user, freshness comes from the federated factor's timestamp:

- IAM stamps the factor at the OAuth callback unless the provider's ID token includes an earlier `auth_time` claim (an earlier `auth_time` can make the factor older, while a missing or future claim never makes it newer than the callback).
- Not all providers supply `auth_time`: Microsoft supports it as an [optional claim](../oauth/providers.md#microsoft), Google does not guarantee it, and GitHub's OAuth2 flow has no ID token at all.
- To hint that a provider-only user should re-authenticate, send them to `/auth/google?fresh=1` (substituting the provider name). IAM preserves `fresh=1` in the sealed state and sends `prompt=login&max_age=0` to OIDC providers, though providers are not forced to honor the hint.

Keep in mind that `RecentLogin` measures the age of *your application's* login record, not whether an external provider prompted the user for credentials.

Next, add [Two-Factor Authentication](two-factor.md) so users can protect their accounts with an authenticator app and recovery codes.

For the guard and freshness APIs, see [`auth`](https://pkg.go.dev/arkoren.dev/core/auth) and [`iam`](https://pkg.go.dev/arkoren.dev/ark/iam).
