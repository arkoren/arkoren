---
title: Organization Sign-In
description: Connect one Microsoft Entra tenant, admit invited users through their first sign-in, and revoke local access when they leave.
order: 100
summary:
  - Configure an Entra registration and the claims Arkoren needs for email-based invitations and authentication age.
  - Keep provider sign-up closed while pre-created users claim their invitations.
  - Separate organizational sign-in from local account membership and offboarding.
---

# Organization Sign-In

An internal application can use Microsoft Entra ID for sign-in while retaining
its own list of allowed users. Entra proves the identity; Access decides
whether it maps to a local account. This example accepts one tenant and keeps
sign-up closed, so belonging to the organization alone does not create a user.

Start with [Access's mounted facade](overview.md) and the
[connection model](connections.md). This page uses the built-in directory;
custom users need their own invitation-resolution policy.

## Register the App

Register an application in the Entra admin center for the intended tenant.
Add this web redirect URI:
`https://app.example.com/auth/microsoft/callback`.
The Microsoft preset's name is `microsoft`, and Access uses
`/auth/{name}/callback`.

Obtain the application client ID (`clientID`), a client secret (`clientSecret`),
and the directory tenant ID (`tenantID`).
Keep the secret in your application's secret-management system.

Configure two optional **ID-token** claims:

- `xms_edov`: the Arkoren preset exposes an email with `EmailVerified` true
  only when this claim is boolean true. Without it, a known connection can
  still sign in, but email-based invitations and new-user creation cannot use
  the missing address.
- `auth_time`: carries the earlier provider authentication time so silent
  approval from an old Entra session does not appear as a fresh factor.

Follow Microsoft's
[optional-claim configuration](https://learn.microsoft.com/en-us/entra/identity-platform/optional-claims).
In the manifest, merge these entries into `optionalClaims.idToken`,
preserving other claims your application requires:

```json
{
  "optionalClaims": {
    "idToken": [
      { "name": "xms_edov" },
      { "name": "auth_time" }
    ]
  }
}
```

Save and sign in again; old ID tokens do not gain the claims.
Check their presence in a controlled development tenant without logging or
sharing whole tokens. The mutable plain `email` claim alone is not sufficient
evidence for this preset.

## Connect the Client

For a single organization, pass its tenant ID, not a domain name. Import
`oauthauth` from `arkoren.dev/ark/oauth/oauthauth`:

```go
provider := oauthauth.Microsoft(tenantID)
```

The tenant ID selects the verified tenant context, including guests admitted
to that tenant. Broader aliases select broader audiences:

| Value | Accepted tenant contexts |
| --- | --- |
| A tenant ID | That tenant only |
| `"organizations"` | Organizational tenants |
| `"consumers"` | The personal-account tenant |
| `"common"` | Both organizational and personal tenants |

Do not substitute `"organizations"` for “our organization.” If you use a
broader alias, your application must enforce its narrower audience policy;
an invitation is local membership policy, not a replacement for checking the
intended tenant.

With a configured `*crypto.Encrypter` named `cipher`, create the client:

```go
microsoft, err := oauthauth.New(provider, cipher, clientID, func(login *oauthauth.ClientBuilder) {
	login.ClientSecret(clientSecret)
	login.RedirectURL("https://app.example.com/auth/microsoft/callback")
})
```

Handle construction failure, then register the client's
[discovery startup](../oauth/overview.md#start-discovery) with your application's
lifecycle. Access does not start it for you.
If users may enroll local two-factor, also configure `ChallengeURL` to your
front-end challenge page.

Pass the configured client to Access, leaving sign-up off:

```go
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Provider(microsoft)
})
```

Complete discovery before serving requests, and use the usual Access migrations
and CSRF/session mounting.
`GET /auth/microsoft` now starts sign-in; an unknown identity without an
eligible local user receives `403` `federation:no-account`.

## Invite Users

For a provider-only invitation, pre-create an **unverified** local account:

```go
subject, err := iam.Directory().Create(ctx, "ada@acme.example")
```

Do not use `CreateVerified` for this path.
When Ada first signs in with a provider-verified matching address, Access
[claims the unverified account](connections.md#unverified-accounts), connects
her Entra identity, and logs her in. Later sign-ins match that connection,
not the email. This works with sign-up disabled because no user is created
at sign-in.

`CreateVerified` would protect the account from automatic claiming. Without
another login method, Ada could not connect her first Microsoft identity:
the callback would return `403` with sign-up off or an email conflict with
sign-up on. Use verified provisioning only when the user can already log in
another way and explicitly connect Microsoft.

If every accepted member and guest of the selected tenant may join, open
sign-up for Microsoft instead:

```go
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Provider(microsoft, func(login *access.ProviderBuilder) {
		login.SignUp(true)
	})
})
```

A first sign-in can then create a verified user without an invitation.
With `"organizations"`, `"consumers"`, or `"common"`, that audience is much
broader than your tenant. Keep sign-up closed unless you deliberately admit
that audience and enforce any additional policy.

## Remove Leavers

Ending a person's Entra session does not revoke a session or token your
application has already issued. For offboarding, use the directory's
[`Disable` and `Rotate` transaction](accounts.md#suspend-a-user).
The disabled flag blocks later checks, including provider sign-in; rotation
prevents old credentials from becoming valid if the user is re-enabled.

Disabling alone is reversible and does not rotate the version.
Deletion also removes Access-owned rows, but with provider sign-up open a
later sign-in could create another user. Do not use deletion alone as a
membership ban.
With [your own users](own-users.md), enforce disabled state in the provider
and rotate its reported credential version in your own transaction.

For sensitive settings, a Microsoft-only user's freshness follows the
federated factor and any earlier `auth_time`, not merely the callback time.
`/auth/microsoft?fresh=1` asks Entra to authenticate again but remains a hint.
A device-token exchange checks the application's new-login age instead.
Use the [freshness distinctions](freshness.md#provider-logins) rather than
treating all three as proof that a password was typed.

If you already have a user database, continue with [Your Own Users](own-users.md).

For Microsoft's identity checks, see the
[`Microsoft` API](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth#Microsoft);
for the local integration, see the
[`access` API](https://pkg.go.dev/arkoren.dev/ark/access).
