# Arkoren Secret Memory Driver

`arkoren.dev/core/service/secret/memorysecret` is an in-memory implementation of
`secret.Driver` for tests and local development.

It is included in `go get arkoren.dev/core`. Import the package directly:

```go
import "arkoren.dev/core/service/secret/memorysecret"

driver := memorysecret.New(func(secrets *memorysecret.Builder) {
	secrets.Values(map[string][]byte{
		"application-config": []byte(`{"http":{"port":8080}}`),
	})
})
```

`Get` returns a copy of each value and missing names return
`secret.ErrNotFound`. `Get`, `Put`, `Update`, and `Delete` return
`ctx.Err()` before reading or changing state. `Put` and `Delete` support test
setup and state changes. The driver is process-local and must not hold
production secrets.

Wrap it with `secret.New` from `arkoren.dev/core/service/secret` to read typed
values. Seeded names start at version `"1"`; `Update` adds a version only when
the name exists. Optional `secret.NewNamespace[T](prefix, separator)` composes
ordinary typed definitions; seed the final exact names. Memory fixture names
do not establish which separators another provider accepts. See the [local secret setup](../../../../cmd/arkoren/skills/arkoren/references/guides/secrets/drivers.md#memory)
and [`memorysecret` API reference](https://pkg.go.dev/arkoren.dev/core/service/secret/memorysecret).

## Version lifetime

`Put` and `Update` append immutable versions within one existing logical secret.
Use a concrete version returned by a write in `secret.Reference` to pin reads
through `driver.Get`. Keep that secret and version available for the duration of
the work; saving the name/version pair does not retain either. Rotate by adding
a version, not by deleting the secret.

`Delete` immediately removes the entire history, with no retention or recovery.
A later `Put` under the same name creates a new logical secret starting at
version `"1"`. An old selector such as name `"api-token"`, version `"1"` then reads
the new secret's version `"1"`, not the deleted contents. This describes memory
behavior, not a requirement for other drivers. Other providers can delay or
prevent name reuse and can provide stronger version or resource identities;
portable conformance requires neither immediate name reuse nor version-string
reuse. Recovery of a retained provider object is not recreation.
