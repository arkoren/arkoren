---
title: Organization SSO
nav: Organization SSO
description: Configure a corporate identity provider for organizational single sign-on, admit invited staff, enforce ongoing membership, and offboard departing users.
order: 70
summary:
  - Restrict a work identity provider (Google Workspace, Microsoft Entra ID, or OIDC) and register its IAM client.
  - Choose between invitation-based unverified accounts and explicit open sign-up.
  - Enforce ongoing application membership on every request and revoke local access during offboarding.
---

# Organization SSO

Organizational single sign-on lets employees sign in with their company identity provider while your application keeps full control over who is a member, what they can access, and when their access ends. The identity provider authenticates the person; IAM maps that verified external identity to a local user subject.

This page builds directly on [External Sign-In](federated-sign-in.md) and focuses on the extra boundaries corporate SSO requires: restricting the upstream tenant, admitting staff, checking ongoing membership, and offboarding cleanly.

## Restricting the Work Identity Provider

Choose **one** provider definition below to produce `definition` for your OAuth client, importing `oauthauth` from `arkoren.dev/ark/oauth/oauthauth`.

### Google Workspace

Restrict sign-in to a single Google Workspace domain with `HostedDomain`:

```go
definition := oauthauth.Google(func(definition *oauthauth.GoogleBuilder) {
	definition.HostedDomain("example.com")
})
```

This checks Google's verified `hd` claim rather than merely inspecting the email suffix. Google reports an email only when `email_verified` is boolean `true`.

### Microsoft Entra ID

Pin sign-in to one Entra tenant using its tenant UUID:

```go
definition := oauthauth.Microsoft("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
```

The preset validates the tenant ID and identifies users by tenant issuer and `oid` (not mutable email or `sub`). Never use `common` or `organizations` when restricting access to one tenant, and remember that a tenant may contain external guests; tenant membership is not your application's authorization policy.

For email-based invitations, Microsoft must emit boolean `xms_edov: true` in the ID token; see [Microsoft provider setup](../oauth/providers.md#microsoft).

### Generic OIDC Providers

For Okta, Keycloak, or another OIDC provider, supply its exact trusted issuer URL:

```go
definition := oauthauth.OIDC("https://id.example.com/realms/work", func(definition *oauthauth.OIDCBuilder) {
	definition.TrustEmail(true)
})
```

> [!WARNING]
> Only enable `TrustEmail(true)` after reviewing the issuer's email-verification policy. A false `email_verified` claim could allow someone to claim an invited account.

Without `TrustEmail(true)`, generic OIDC reports no email evidence. Generic OIDC enforces no tenant policy on its own; add any organization checks yourself and request the `email` scope on the client if the provider requires it.

## Building and Mounting the Client

Register `https://app.example.com/auth/work/callback` with your identity provider, then build and start the client with your shared `cipher *crypto.Encrypter`:

```go
work, err := oauthauth.New(definition, cipher, clientID, func(client *oauthauth.ClientBuilder) {
	client.Name("work")
	client.ClientSecret(clientSecret)
	client.RedirectURL("https://app.example.com/auth/work/callback")
})
err = work.Start(ctx)
```

Register the client with IAM, leaving `SignUp` off by default:

```go
identity.Provider(work)
```

With IAM mounted at `/auth`, `GET /auth/work` begins SSO and `/auth/work/callback` completes it. Configure `ChallengeURL` on the client if staff also use [local two-factor authentication](two-factor.md).

## Admitting Staff

With `SignUp` off, you control who can join by pre-creating an **unverified** built-in account for each invited employee from an authorized admin flow:

```go
subject, err := identity.Directory().Create(ctx, "ada@example.com")
```

When Ada signs in through `work` for the first time with a verified `ada@example.com` email, IAM claims that unverified account, verifies the email, and links her corporate identity automatically, without needing `SignUp(true)`.

A few critical rules for this pattern:

- **Use `Create`, not `CreateVerified`**: An unknown provider identity can never automatically attach to an already verified account. Existing verified users must sign in another way and [connect the provider](federated-sign-in.md#connecting-and-disconnecting-accounts).
- **Check for collisions**: If `Create` returns `account.ErrEmailTaken`, do not treat it as a fresh invitation; an existing unverified account is subject to the same destructive [unverified-account takeover](federated-sign-in.md#unverified-account-takeover), which wipes any prior credentials and sessions.
- **No built-in expiry**: This is email-ownership admission, not an expiring invitation-token workflow. With [custom user stores](own-users.md), automatic takeover is unavailable.

If everyone in the restricted tenant should be able to join automatically on first login, enable sign-up on the provider instead:

```go
identity.Provider(work, func(login *iam.ProviderBuilder) {
	login.SignUp(true)
})
```

You can layer a `login.Create(...)` hook on top to restrict who may sign up, though remember that `Create` runs only for new-user creation, not for existing connections or unverified-account claims.

## Enforcing Ongoing Membership

Signing in at the identity provider proves identity at login time, not that the person is still an active member of your application. Check current membership from your own trusted store on every request using `gate` from `arkoren.dev/core/auth/gate`:

```go
activeMember := func(ctx context.Context, actor auth.Identity) (bool, error) {
	return membership(ctx, actor.Subject())
}
```

Apply the rule to your authenticated router:

```go
web.With(gate.Require(activeMember)).Get("/projects", listProjects)
```

Former members receive `403`, anonymous callers receive `401`, and store failures propagate as errors rather than granting access. Still enforce resource-level rules inside your handlers; see [Authorization](../authorization/overview.md).

## Offboarding Departing Users

Signing someone out of Okta, Entra ID, or Google Workspace does **not** end their active local sessions or API tokens in your application. When an employee leaves, disable their IAM account and rotate their credential version in a single database transaction:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := identity.Directory().Disable(ctx, subject); err != nil {
		return err
	}

	return identity.Directory().Rotate(ctx, subject)
})
```

Disabling blocks every subsequent session, token, and provider check; rotating the version invalidates all existing sessions, pending logins, and tokens permanently even if the account is later re-enabled. Also revoke their application membership in your own store. If `SignUp` is open, keep the account disabled rather than deleting it, because deleting it would allow the upstream identity to sign up again.

Keep in mind that upstream MFA and logout are separate from local IAM controls. Review [External Providers and Freshness](freshness.md#external-providers-and-freshness) before relying on SSO for sensitive actions, and enable [local two-factor](two-factor.md) when your security policy requires it.

Next, see [Roles & Permissions](permissions.md) to assign global roles and permissions directly through IAM.

For the client and facade APIs, see [`oauthauth`](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth) and [`iam`](https://pkg.go.dev/arkoren.dev/ark/iam).
