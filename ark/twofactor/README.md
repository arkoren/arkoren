# Two-Factor Authentication

`arkoren.dev/ark/twofactor` adds TOTP two-factor authentication to an Arkoren
session login: authenticator-app enrollment, a second step that completes a
challenged login, single-use recovery codes, and cleanup for deleted users. It
takes your `auth.SessionIdentity`, so it never sees your user type, and it ships
its two tables as migrations that your migrator runs.

This ark is pending its first release. Until then it builds only inside the
repository workspace (`go.work`), and there is no `go get` command for it. See
the [two-factor login guide](../../cmd/arkoren/skills/arkoren/references/guides/authentication/two-factor.md#use-the-two-factor-ark)
for wiring, and the package documentation for the API.

```go
factors, err := twofactor.New(identity, db, cipher, hasher, func(ark *twofactor.Builder) {
	ark.Issuer("Example")
})
if err != nil {
	return err
}
if err := migrator.Add(factors.Migrations()...); err != nil {
	return err
}

web.Post("/two-factor/verify", factors.Verify(identity))
```

Run the integration tests against PostgreSQL and MySQL with
`make integration MODULE=ark/twofactor`; SQLite tests run with `make test`.
