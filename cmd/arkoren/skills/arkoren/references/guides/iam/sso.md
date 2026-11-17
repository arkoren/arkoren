---
title: SSO
description: Use a configured identity provider for organizational sign-in while retaining control of local membership and access.
order: 60
summary:
  - Choose an upstream provider and restrict its accepted issuer or audience.
  - Decide how users are admitted, linked, and removed from your application.
  - Separate upstream authentication from local authorization, sessions, and multi-factor protection.
---

# SSO

Single sign-on lets users authenticate with an organization-managed identity
provider. IAM maps that identity to a local user; your application remains
responsible for deciding who may join and what each user may do.

Start with [Federated Sign-In](federated-sign-in.md), which explains connection
mapping, first sign-in, and linking. Use [OAuth Providers](../oauth/providers.md)
to configure a supported provider and compare its identity and email guarantees.
Select the most restrictive issuer, tenant, and audience your provider supports.
An organization-wide or multi-tenant endpoint is not necessarily restricted to
your organization.

## Register the Provider

Assume `provider` is a configured and started OAuth client. Register it with
IAM, keeping first-login sign-up disabled:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Provider(provider)
})
```

Handle the construction error and apply IAM's migrations before serving
requests. The provider's discovery lifecycle belongs to your application.
An unknown identity normally receives `403` `federation:no-account`; the
unverified built-in account claim described below is an exception.

## Admit Users

For provider-only invitations with the built-in directory, create an
unverified local account for an address whose owner you intend to invite:

```go
subject, err := identity.Directory().Create(ctx, "ada@example.com")
```

When a provider proves the same email, IAM can
[claim the account](federated-sign-in.md#unverified-accounts), remove its
previous credentials, and link the provider identity. This destructive claim
also applies when provider sign-up is disabled. Check the provider's email
verification guarantees before using this flow. With a custom user store,
automatic claiming is unavailable; implement your own admission policy.

If the user already has a verified account, they must sign in by an existing
method and explicitly connect the provider. A matching email alone does not
attach a provider to that account.

You can instead enable sign-up for the provider and apply a creation hook for
additional admission rules. Sign-up admits new users only when the provider
reports a verified email; a creation hook is not called on every subsequent
login. See [first-time sign-in](federated-sign-in.md#first-time-sign-in) for
the exact behavior.

## Manage Access

Provider authentication does not grant application permissions. Keep local
membership and [authorization](../authorization/overview.md) in your
application, and enforce any continuing organizational membership policy there.

Ending an upstream session does not revoke local sessions or API tokens.
When a user leaves, [disable and rotate](accounts.md#suspend-a-user) the local
account as part of offboarding. Disabling alone does not rotate its credential
version. If sign-up remains open, deleting the local account alone does not
prevent a later sign-in from creating another user.

## Require Stronger Login

An upstream login is not automatically a fresh local proof. Review
[Fresh Logins](freshness.md#provider-logins) and the provider's authentication
time behavior before protecting sensitive actions. IAM can require a local
[second factor](two-factor.md); configure the provider's challenge URL so
the browser can complete it rather than bypassing the challenge.
