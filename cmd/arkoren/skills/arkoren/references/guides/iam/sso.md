---
title: SSO
description: Use a configured identity provider for organizational sign-in while retaining control of local membership and access.
order: 60
summary:
  - Restrict a work identity provider and mount its client for organizational sign-in.
  - Admit invited users with verified provider email or choose explicit sign-up.
  - Check ongoing application membership and revoke local access during offboarding.
---

# SSO

Single sign-on lets people reuse an identity-provider login across applications.
For organizational SSO, that provider is managed by the organization.
The provider authenticates the person; IAM maps the
provider's identity to a local user. Your application still decides who can
join, what they can access, and when that access ends.

This guide uses an existing IAM database and cache from the
[IAM overview](overview.md), and a browser client registered with your
identity provider. Handle each error in these focused fragments before
continuing.

## Choose a Provider

Choose **one** of the following provider definitions for your organization.
Each defines `definition`, which the client construction below uses. Import
`oauthauth` from `arkoren.dev/ark/oauth/oauthauth`.

### Google Workspace

For a single Google Workspace domain, require its hosted domain:

```go
definition := oauthauth.Google(func(definition *oauthauth.GoogleBuilder) {
	definition.HostedDomain("example.com")
})
```

This sends a domain hint and checks the verified `hd` claim; an email address
ending in `example.com` alone is not the domain check. Google reports an email
only when its `email_verified` claim is boolean true.

### Microsoft Entra ID

For one Microsoft Entra tenant, use its tenant UUID, replacing this example
with your own:

```go
definition := oauthauth.Microsoft("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
```

The preset checks the tenant ID and uses the tenant issuer and `oid` as the
external identity, rather than a mutable email or the token's `sub`. A tenant
can include guests, so tenant acceptance is not your membership policy. Do not
use `common` or `organizations` to restrict sign-in to one tenant.

For email-based invitations, Microsoft must provide boolean `xms_edov: true`;
an ordinary email claim alone is not evidence. Configure and review the
optional ID-token claim as described in [Microsoft provider setup](../oauth/providers.md#microsoft).
If the claim is absent or false, IAM cannot use that email to claim an invitation.

### Other OIDC Providers

For another OpenID Connect (OIDC) provider, use its exact trusted issuer.
Only enable email trust after reviewing that issuer's address-verification
policy: a false assertion could give someone ownership of an invited account.

```go
definition := oauthauth.OIDC("https://id.example.com/realms/work", func(definition *oauthauth.OIDCBuilder) {
	definition.TrustEmail(true)
})
```

With trust enabled, the adapter reports an email only when `email_verified`
is boolean true. Without `TrustEmail(true)`, it reports no email evidence,
even if the claim is present. Generic OIDC infers no organization or tenant
policy; choose an issuer whose policy you have reviewed and enforce any
additional membership restrictions yourself. If the provider requires the
email scope, configure it on the client. See [generic OIDC](../oauth/providers.md#generic-oidc).

## Build the Client

Register `https://app.example.com/auth/work/callback` with the selected
provider. Obtain its `clientID` and `clientSecret` from your application's
configuration or secret provider. Use an existing `cipher *crypto.Encrypter`
with a stable key shared across instances that may receive the callback; an
ephemeral key can make an in-flight login fail after a restart or on another
instance. See [OAuth client setup](../oauth/overview.md#configure-clients).

```go
work, err := oauthauth.New(definition, cipher, clientID, func(client *oauthauth.ClientBuilder) {
	client.Name("work")
	client.ClientSecret(clientSecret)
	client.RedirectURL("https://app.example.com/auth/work/callback")
})
```

Construction does not perform discovery. Start the client successfully before
serving requests:

```go
err = work.Start(ctx)
```

Until discovery succeeds, OIDC handlers return `503`. Your application's
[startup lifecycle](../oauth/overview.md#start-discovery) can run `work.Start`.
If users have local two-factor authentication, configure `ChallengeURL` to a
front-end challenge page; otherwise a login requiring that challenge fails.

## Mount Sign-In

Register the started client with IAM, retaining the default closed sign-up.
Use `iam` from `arkoren.dev/ark/iam`, with the existing `db` and `store`:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Provider(work)
})
```

Apply the enabled [migrations](overview.md#prepare-storage) and
[mount the facade](overview.md#mount-routes) once with session and CSRF
protection. The client
name makes `GET /auth/work` the login entry point and
`/auth/work/callback` the callback. Offer a link to `/auth/work` on your login
page. The browser goes to the provider, then returns to IAM: a connected or
successfully claimed identity starts a local login, subject to any local
two-factor challenge. An unknown identity otherwise receives `403`
`federation:no-account`.

IAM stores a connection by provider issuer and subject, not by email. See
[federated sign-in](federated-sign-in.md) for connection and callback behavior.

## Admit Users

With the built-in directory and provider sign-up still off, invite a person
by pre-creating an **unverified** account for the intended address. This is
email ownership-based admission, not an invitation-token workflow: it adds no
invitation expiry or separate invitation-secret check.

> [!WARNING]
> Claiming an unverified account deletes its existing password, provider
> connections, authenticator, recovery codes, and tokens. IAM verifies the
> address, rotates the credential version, and links the new identity in one
> transaction. Earlier sessions and pending logins become invalid. Trust a
> provider's email evidence only after reviewing its verification policy.

From an authorized administrative operation, create the new invitation account
without attaching other credentials:

```go
subject, err := identity.Directory().Create(ctx, "ada@example.com")
```

`subject` is the local user identifier. On Ada's first provider login, IAM can
claim this account if the provider proves the same email. If `Create` reports
that the address is taken, do not overwrite that account or assume it is a new
invitation: an existing unverified account is subject to the same destructive
claim. Do not use `CreateVerified` for this invitation: an unknown provider
identity cannot automatically attach to an already verified account.

The claim works even when sign-up is off; a disabled user cannot be claimed.
For application-owned credentials, arrange
[`OnEmailProven`](own-users.md#additional-credentials) cleanup. Automatic
takeover is unavailable with a custom user store.

An existing verified account is different: the owner must authenticate using
an existing method and explicitly [connect the provider](federated-sign-in.md#connect-a-provider).
Matching email alone never links it.

### Open Sign-Up

If your application deliberately permits new users to join on first provider
login, use this alternative registration instead:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Provider(work, func(login *iam.ProviderBuilder) {
		login.SignUp(true)
	})
})
```

Sign-up requires a nonempty provider-verified email. You can add a `Create`
hook for an additional creation rule, but it is not a universal admission
gate: existing connections and unverified-account claims do not pass through
that sign-up hook. See [first-time sign-in](federated-sign-in.md#first-time-sign-in)
for hook behavior and collisions.

## Check Access

A successful provider login does not mean that Ada is still an active member
of your application or may read every resource. Load current membership from
your trusted application store, not from a request field or an old provider
profile. Assume `membership` is your application-owned
`func(context.Context, string) (bool, error)` that queries current membership
by local user subject. It returns false for a former member and propagates
store failures. Use `auth` from `arkoren.dev/core/auth` and `gate` from
`arkoren.dev/core/auth/gate` to build a rule over the IAM identity:

```go
activeMember := func(ctx context.Context, actor auth.Identity) (bool, error) {
	return membership(ctx, actor.Subject())
}
```

Apply that rule to the session-authenticated `web` group from
[Mount Routes](overview.md#mount-routes),
before your application's `listProjects` handler:

```go
web.With(gate.Require(activeMember)).Get("/projects", listProjects)
```

The rule checks membership on each request and denies inactive members with
`403`; a missing identity produces `401`. Store failures reach the error
handler rather than granting access or becoming an ordinary denial. Also
enforce resource-specific access in the handler.
See [authorization](../authorization/overview.md) for store-backed checks
and protecting operations used outside HTTP.

## Offboard Users

Ending an upstream session does not revoke the application's local sessions
or API tokens. When a person leaves, disable their built-in IAM account and
rotate its credential version in the same database transaction. Run this from
an authorized administrative operation; `subject` is the local user selected
for offboarding, not an unchecked identifier from a public request:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := identity.Directory().Disable(ctx, subject); err != nil {
		return err
	}
	return identity.Directory().Rotate(ctx, subject)
})
```

Handle the error: either both database changes commit or neither does.
Disabling blocks subsequent checks; rotation invalidates old sessions,
pending logins, and tokens even if the account is later re-enabled. Rotation
does not delete credentials or connections. Keep the user disabled while
access must remain blocked, and remove their application membership through
your own policy. If sign-up is open, deleting an account alone does not prevent
a later sign-in from creating another user. See
[suspension](accounts.md#suspend-a-user) for details.

Upstream MFA and logout are separate from local IAM controls. A provider login
is not automatically a fresh local proof; review
[freshness](freshness.md#provider-logins) before protecting sensitive actions,
and use [local two-factor authentication](two-factor.md) when your policy
requires it.

For the client and facade APIs, see
[`oauthauth`](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth) and
[`iam`](https://pkg.go.dev/arkoren.dev/ark/iam).
