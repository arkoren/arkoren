# arkoren.dev/ark/iam

This module is not released; installation is pending.

Login, optional registration, two-factor authentication, API tokens, social sign-in
and permissions and roles for an Arkoren application, as JSON routes on core's
`auth.Guard`.

The module has no third-party dependencies (core, foundation and the standard
library) and never imports `arkoren.dev/ark/oauth`: a provider joins through the
small `iam.Provider` interface that `*oauthauth.Client` satisfies.

| Package | Owns | Tables |
| --- | --- | --- |
| `iam` | the facade: `New`, `Builder`, `Mount`, `Provider`, `Users` | none |
| `account` | users by email, signed email verification links, the user provider | `users` |
| `password` | login, optional register, confirm, change, forgot and reset with signed links | `password_hashes` |
| `twofactor` | TOTP and recovery codes | `totp_secrets`, `recovery_codes` |
| `apitoken` | personal and device tokens, the bearer authenticator | `api_tokens` |
| `federation` | links external identities to users | `federation_links` |
| `permission` | roles and permissions (core `gate` values) assigned to users | `permission_grants` |

The packages never import each other; only the root does. Table names take a
`<instance>_` prefix for a named instance, and the application runs the
migrations: the module never runs DDL.

```go
scheme := totp.New("Acme") // the name authenticator apps show for this account
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher, func(passwords *password.Builder) {
		passwords.Registration(true) // off by default: omit it when you add users yourself
		passwords.Reset(signer, dispatchReset, deliverReset) // required with registration and the built-in users
	})
	identity.Directory(func(accounts *account.Builder) {
		accounts.Verification(signer, dispatchVerification, deliverVerification)
	})
	identity.TwoFactor(scheme, cipher)
	identity.Tokens()
	identity.Provider(google)
	identity.Provider(github, func(login *iam.ProviderBuilder) {
		login.SignUp(true) // off by default, set per provider
	})
})
if err != nil {
	return err
}
if err := migrator.Add(identity.Migrations()...); err != nil {
	return err
}
web := routes.With(csrf.Middleware(), identity.Middleware())
identity.Mount(web)
```

IAM uses the direct request peer by default. Behind a trusted proxy, configure
`Builder.ClientIP` using the [Client Addresses guide](../../cmd/arkoren/skills/arkoren/references/guides/http-networking/client-addresses.md).
The routes above require CSRF protection; the example uses core `csrf.Middleware`.

Reset and verification links are signed values from core's `link.Signer`, bound to
the password hash or the email state, so the module keeps no token tables and
starts no goroutine. `dispatch` stores the work in your queue or workflow, and a
worker runs `identity.SendReset` or `identity.SendVerification`, which call your `deliver`
function to send the email. Without them the forgot-password, reset and
verification routes are not mounted.

`identity.Permissions(...)` stores role and permission assignments for core
`gate.Role` and `gate.Permission` values. The guard then loads each session's
expanded roles and permissions on every request, so `gate.HasPermission` and
`gate.HasRole` work without a query, and tokens are capped at the owner's
permissions. Sessions pass scope rules too, so keep CSRF protection on every
route a session can reach. See the
[Permissions guide](../../cmd/arkoren/skills/arkoren/references/guides/iam/permissions.md).

Device tokens that nobody uses, expired tokens and tokens that a password reset
ended stay in the table until you prune them. `identity.Prune` has the shape of a scheduled function; run it daily:

```go
schedule.NewEntry("access-prune", identity.Prune, func(entry *schedule.EntryBuilder) {
	entry.When(schedule.UTC().Hour(3))
})
```

The hooks the facade sets on its packages (token revocation after a password
change, the verified-email gates, the takeover, user creation, the registration
notices) add up with the callbacks you give, so an application callback cannot
drop them. `Provider` takes one call per provider, with that provider's own
sign-up and creation settings; `Password`, `TwoFactor`, `Tokens` and `Permissions`
replace their earlier call. A provider login whose email the provider verified takes over an
unverified account and publishes `federation.AccountClaimed`; a first provider
sign-up needs a proven email. A reset does not purge the connections of a verified
user, so review them after one. Read the [IAM guide](../../cmd/arkoren/skills/arkoren/references/guides/iam/overview.md)
for the route table, the responses and the security rules, and the API on
[pkg.go.dev](https://pkg.go.dev/arkoren.dev/ark/iam).

## Tests

`go test -short ./...` runs on in-memory SQLite. `make integration MODULE=ark/iam`
starts PostgreSQL and MySQL with Compose and runs every test on all three
databases, including two instances sharing one database.
