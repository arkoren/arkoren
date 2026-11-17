---
title: Organization Sign-In
description: Let the people of one organization sign in with Microsoft Entra ID, keep sign-up invite-only and end a leaver's access, with the access facade.
order: 65
summary:
  - Register the application in Entra, choose a single tenant or all organizations and connect the client with the facade.
  - Decide between invite-only and open sign-up, and add invited users so their first Microsoft sign-in claims them.
  - Disable a user who leaves the organization so their sessions and tokens stop at the next request.
---

# Organization Sign-In

An internal tool should not ask employees for another password. With Microsoft
Entra ID the organization already knows who works there, so the application
accepts that sign-in and keeps its own list of who may use it. This page connects
an Entra client to the [access facade](overview.md), decides who may sign in for
the first time, and ends a leaver's access. It assumes the facade is composed and
mounted as described in [Access Overview](overview.md), and that your database
has the facade's migrations.

## Register the App

In the Entra admin center, register an application and add the web redirect URI
`https://app.example.com/auth/microsoft/callback`. The facade mounts every
provider's callback at `/auth/{name}/callback`, and the Microsoft preset is named
`microsoft`. Create a client secret, and note the application (client) ID and the
directory (tenant) ID.

Then add the optional claims `xms_edov` and `auth_time` to the ID token. `xms_edov`
says that the owner of the email's domain verified the address. Microsoft's plain
`email` claim can be unverified and can change, so the preset exposes an address
only when `xms_edov` is true. Without the claim the application receives no email
at all: sign-in by an existing connection still works, but a first sign-in cannot
find the invited user and a new user has no address to be created with.
`auth_time` says when the user last authenticated at Entra, so a silent approval
by an old Entra session is not treated as a fresh sign-in; see
[Fresh Logins](freshness.md#provider-logins).

Add both in the application manifest, which works for every registration the same
way. Open Manifest for the app, and set `optionalClaims` to:

```json
{
  "optionalClaims": {
    "idToken": [
      { "name": "xms_edov", "source": null, "essential": false, "additionalProperties": [] },
      { "name": "auth_time", "source": null, "essential": false, "additionalProperties": [] }
    ]
  }
}
```

Save the manifest and sign in again: a token issued before the change does not
carry the claims. Check a token's claims once, for example in a development
tenant, before relying on them.

## Create the Client

Choose which tenants may sign in when you create the client:

```go
import oauth "arkoren.dev/ark/oauth/oauthauth"

microsoft, err := oauth.New(oauth.Microsoft(tenantID), cipher, clientID,
	func(login *oauth.ClientBuilder) {
		login.ClientSecret(clientSecret)
		login.RedirectURL("https://app.example.com/auth/microsoft/callback")
	},
)
```

| Argument | Who may sign in |
| --- | --- |
| The tenant ID, a GUID | Only members and guests of that one organization. |
| `"organizations"` | Work and school accounts of any organization. |
| `"consumers"` | Personal Microsoft accounts only. |
| `"common"` | Both classes. |

For an internal tool, pass the tenant ID, not a domain name such as
`contoso.onmicrosoft.com`. The aliases accept more than your organization:
`"organizations"` and `"common"` let people from organizations you do not know
reach the callback, and `"consumers"` lets in any personal Microsoft account,
which no organization controls. Use them only when you check each identity
yourself. Start the client with the rest of your application, as
[OAuth Overview](../oauth/overview.md#start-discovery) shows, and give it to the
facade:

```go
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Provider(microsoft)
})
```

`GET /auth/microsoft` now sends the browser to Entra, and the callback logs a
known identity in. Call `ChallengeURL` on the client as well when some users have
[two-factor](two-factor.md) enabled, as [Connections](connections.md#signing-in)
explains.

## Choose Who Joins

What happens to a person who signs in for the first time is your decision, as
described in [Connections](connections.md#signing-in).

**Invite-only** is the default, and the right choice when the organization is
larger than the application's audience. A person you did not add is refused with
`403` `federation:no-account` and nothing is created. You add people yourself
with the directory:

```go
subject, err := iam.Directory().Create(ctx, "ada@contoso.com")
```

Use `Create`, not `CreateVerified`, for these users. `Create` makes a user with an
unverified email and no password. When Ada first signs in through Microsoft with
that address, verified by `xms_edov`, the facade
[takes the account over](connections.md#unverified-accounts): it marks the email
verified, connects her Entra identity and logs her in. Later sign-ins match the
connection, not the email. A user made with `CreateVerified` has nothing to
prove, and accounts are never linked by email, so her first Microsoft sign-in
would be refused: `403` with sign-up off and `409` with it on. Use `CreateVerified` only for people who can log in
another way and connect Microsoft from their settings.

**Open sign-up** lets any accepted identity of one provider create a user. It is a
setting of that provider, so you can open it for Entra and keep it closed for
another provider, such as Google, in the same facade:

```go
iam.Provider(microsoft, func(login *access.ProviderBuilder) {
	login.SignUp(true)
})
iam.Provider(google)
```

A first sign-in then creates a user whose email is verified, because Microsoft
vouched for it. This is safe only with a single tenant: with `"organizations"`,
`"common"` or `"consumers"`, anyone with an account in an accepted tenant becomes
a user. Keep sign-up off for those, and for any application whose audience is a
subset of the organization.

## Remove Leavers

Signing someone out of Entra does not end sessions or tokens that your
application already issued. When a person leaves, disable their user:

```go
subject, _, err := iam.Directory().Lookup(ctx, "ada@contoso.com")
if err != nil {
	return err
}
err = iam.Directory().Disable(ctx, subject)
```

The next request of every session, [API token](tokens.md) and pending login of
that user is rejected, and a sign-in through Microsoft answers `401`, so a user
Entra still lists cannot come back. `iam.Directory().Enable(ctx, subject)` reverses
it. Delete the user with `iam.Directory().Delete` when their data must go as well,
which removes every feature's rows in one transaction; with sign-up on, a later
sign-in would then create a new user.

With [your own users](own-users.md), disabling is yours. Mark the user disabled in
your store so your driver's `Find` returns `auth.ErrUserDisabled`, and bump the
same version your `Users.Rotate` bumps, so nothing issued earlier survives a later
re-enable.

A person who signs in only through Microsoft has no password and no authenticator,
so they have no step-up method and are fresh right after a sign-in. When Entra
approves a sign-in silently, because the browser already has an Entra session, the
`auth_time` claim carries the older time of the original sign-in, and the login is
as old as that, so a person who signed in to Entra hours ago is not fresh for a
credential change. Without the optional claim, the silent approval counts as a
login now. Add `?fresh=1` to `/auth/microsoft` to ask Entra for credentials again;
it is only a hint (`prompt=login&max_age=0`) that Entra may ignore. The device-token
exchange asks for `guard.RecentLogin`, which checks when your application's own
login was created, so a new login that Entra approves silently still passes it; see
[Fresh Logins](freshness.md#provider-logins) and
[exchanging a login for a device token](tokens.md#mobile-login).

For the provider's registration and claims, see
[Providers](../oauth/providers.md#microsoft). For every function and option, see
[the `access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
