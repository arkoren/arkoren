# arkoren.dev/ark/access

Login, optional registration, two-factor authentication, API tokens and social sign-in
for an Arkoren application, as JSON routes on core's `auth.Guard`.

The module has no third-party dependencies (core, foundation and the standard
library) and never imports `arkoren.dev/ark/oauth`: a provider joins through the
small `access.Provider` interface that `*oauthauth.Client` satisfies.

| Package | Owns | Tables |
| --- | --- | --- |
| `access` | the facade: `New`, `Builder`, `Mount`, `Provider`, `Users` | none |
| `account` | users by email, email verification, the user provider | `users`, `email_verifications` |
| `password` | login, optional register, confirm, change, forgot and reset | `password_hashes`, `password_resets` |
| `twofactor` | TOTP and recovery codes | `totp_secrets`, `recovery_codes` |
| `apitoken` | personal and device tokens, the bearer authenticator | `api_tokens` |
| `federation` | links external identities to users | `federation_links` |

The packages never import each other; only the root does. Table names take a
`<instance>_` prefix for a named instance, and the application runs the
migrations: the module never runs DDL.

```go
// The client address keys the throttles: behind a proxy list its prefixes, and
// with no proxy in front pass ratelimit.TrustedProxyKey() with none.
clientIP := ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8"))
scheme := totp.New("Acme") // the name authenticator apps show for this account
iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
	iam.Password(hasher, func(passwords *password.Builder) {
		passwords.Registration(true) // off by default: omit it when you add users yourself
	})
	iam.TwoFactor(scheme, cipher)
	iam.Tokens()
	iam.Provider(google, github)
	iam.Deliver(send)
})
if err != nil {
	return err
}
if err := migrator.Add(iam.Migrations()...); err != nil {
	return err
}
web := routes.With(iam.Middleware())
iam.Mount(web)
```

Read the [Access guide](../../cmd/arkoren/skills/arkoren/references/guides/access/overview.md)
for the route table, the responses and the security rules, and the API on
[pkg.go.dev](https://pkg.go.dev/arkoren.dev/ark/access).

## Tests

`go test -short ./...` runs on in-memory SQLite. `make integration MODULE=ark/access`
starts PostgreSQL and MySQL with Compose and runs every test on all three
databases, including two instances sharing one database.
