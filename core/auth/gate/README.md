# Gate

`arkoren.dev/core/auth/gate` authorizes the identity stored by `auth.Middleware`
with plain rules of the form `func(ctx, U) (bool, error)`.

After [authentication](../README.md), use your application's `policy.IsAdmin`
rule to protect `dashboard` on an existing `r` router:

Import `gate` from `arkoren.dev/core/auth/gate`. `U` is the application user the
authenticator attached to the identity's record. Rules must accept the exact same
user type; `User` and `*User` do not match. A rule over `auth.Identity` receives
the identity itself and needs no application user type.

```go
r.With(gate.Require(policy.IsAdmin)).Get("/admin", dashboard)
```

For an operation outside route middleware, check your `policy.CanEdit(postID)`
rule before making the protected change:

```go
if err := gate.Authorize(ctx, policy.CanEdit(postID)); err != nil {
	return err
}
```

Outside a request, `gate.Bind(rules...)` adapts rules to a `func(ctx) (bool, error)`
availability check that reports `false` for anonymous callers, and
`gate.HasScopes(scopes...)` is a rule over `auth.Identity` that requires every
listed scope.

No identity, no user record, or a record of another type returns
`auth.ErrUnauthenticated` (401), a denied rule returns `gate.ErrForbidden` (403),
and a rule error is returned as is. Row-level access belongs in query scopes and
invariants in conditional writes; see the package documentation.

See the [authorization guide](../../../cmd/arkoren/skills/arkoren/references/guides/authorization/overview.md)
and [`gate` API reference](https://pkg.go.dev/arkoren.dev/core/auth/gate).
