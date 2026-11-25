---
title: References and Sources
nav: References and Sources
description: Route secret references to files, environment variables, and managed providers, and let each deployment choose where a secret lives.
order: 15
summary:
  - Route references by scheme with a multiplexing driver that wraps several secret drivers.
  - Read secrets from mounted files and environment variables, with their size, naming, and read-only limits.
  - Bind a secret to a deployment setting so one build can read from different backends.
---

# References and Sources

One application often reads secrets from several places: a mounted file in
Kubernetes, an environment variable in development, a managed provider in
production. A **reference** names the place and the secret together, as
`scheme:name`. The `secretmux` package routes each reference by its scheme to the
driver registered for it, so one `secret.Store` serves them all. Import it from
`arkoren.dev/core/service/secret/secretmux`.

## Routing by Scheme

Register one driver per scheme. The mux splits a reference at its first colon
and passes the rest to that driver as its name:

```go
sources, err := secretmux.New(func(sources *secretmux.Builder) {
	sources.Source("file", filesecret.New())
	sources.Source("env", envsecret.New(os.LookupEnv))
	sources.Source("aws", awsDriver)
})
secrets := secret.New(sources)
```

With this store, `file:/run/secrets/db-password` reads `/run/secrets/db-password`
and `aws:billing/key` reads `billing/key` from the AWS driver. A name may contain
more colons: `vault:app:db` reaches the Vault driver as `app:db`.

`New` rejects a configuration with no sources, an empty scheme, a scheme that
contains a colon, a nil driver, or a duplicate scheme, wrapping
`secretmux.ErrInvalidConfig`. A reference whose scheme is not registered, or that
has no scheme, fails with `secretmux.ErrUnknownScheme`; the message lists the
registered schemes in sorted order. Results from each driver pass through
unchanged, so `secret.ErrNotFound` and `secret.ErrUnsupported` keep their meaning.

The mux owns no lifecycle. Start and stop each driver yourself; the mux only
routes calls.

## Files

`filesecret.New()` reads a file whose path is the reference name, absolute or
relative to the working directory. It follows symbolic links, which is how
Kubernetes and Docker mount secrets, and reads the file again on every `Get`, so a
rotated mount is picked up by the next read. Import it from
`arkoren.dev/core/service/secret/filesecret`.

A missing file wraps `secret.ErrNotFound`. A file larger than 1 MiB is an error.
Writes (`Put`, `Update`, `Delete`) wrap `secret.ErrUnsupported`. The driver adds
no directory prefix and no path policy, so build references only from trusted
configuration, never from request data.

Reading into a `string` drops the one trailing newline an editor or `echo` adds;
reading into `[]byte` keeps every byte.

## Environment Variables

`envsecret.New(lookup)` reads the variable named by the reference, with `lookup`
normally `os.LookupEnv`. Import it from
`arkoren.dev/core/service/secret/envsecret`. Tests pass a function over a map
instead of changing process state.

The name must be a POSIX environment name (`[A-Za-z_][A-Za-z0-9_]*`); anything
else is an error. An unset variable wraps `secret.ErrNotFound`, and a variable set
to the empty string is a present, empty secret. Values must be valid UTF-8 and
writes wrap `secret.ErrUnsupported`.

Environment variables are inherited by child processes and often appear in
diagnostics. Prefer files or a managed provider for long-lived credentials.

## Bindings

Hard-coding `aws:prod/db` in the application ties every deployment to one backend.
A **binding** names a setting instead, and the deployment supplies the reference:

```go
password := secret.NewBinding[string]("DATABASE_PASSWORD")

secrets := secret.New(sources, func(secrets *secret.Builder) {
	secrets.Bindings(os.LookupEnv)
})
```

With `DATABASE_PASSWORD=aws:prod/db` in the environment, `secrets.Get(ctx, password)`
reads `aws:prod/db`; with `DATABASE_PASSWORD=file:/run/secrets/db` it reads the
file, and the application is unchanged. The lookup runs on every `Get`.

A binding must be bound. No lookup, an unset key, or an empty value fails with
`secret.ErrUnbound`, naming the key; there are no defaults, because a silent
fallback would read the wrong secret. The binding key is a setting name, not a
secret, but the reference it resolves to can reveal your provider layout, so keep
both out of public output.

## Generated Applications

`arkoren add ark iam` and `arkoren add ark oauth` write a `Secrets` provider to
`internal/bootstrap/secrets.go` when a provider they generate needs a key. It
registers the `file` and `env` sources and resolves bindings from the process
environment:

```go
var Secrets = application.Provide(func(compose *application.Composer) *secret.Store {
	driver := compose.Must(secretmux.New(func(sources *secretmux.Builder) {
		sources.Source("file", filesecret.New())
		sources.Source("env", envsecret.New(os.LookupEnv))
	}))

	return secret.New(driver, func(secrets *secret.Builder) {
		secrets.Bindings(os.LookupEnv)
	})
})
```

Each key provider reads one binding with `compose.Use(Secrets)`, such as
`LINK_KEY`, `OAUTH_KEY`, `TWO_FACTOR_KEY` or `APPLE_KEY`. The command writes the
bindings to `.env` and `.env.example` as `file:.secrets/` references:

```sh
LINK_KEY=file:.secrets/link-key
```

Create the file with `arkoren key generate shared --out .secrets/link-key`. The
generated `.gitignore` and `.dockerignore` exclude `.secrets/`, and a project
created by an older version needs that line added by hand. Composition fails with
`secret.ErrUnbound` when a binding is unset, and with the source's error when the
file is missing or invalid; nothing generates a key to replace a missing one.
Every process that resolves a binding, including workers and one-shot commands,
loads `.env` the same way.

To read a ring from a secrets manager, change only the binding value, after
registering the source in `Secrets`:

```sh
LINK_KEY=aws:billing/link-key
```

The generated provider registers neither `aws` nor `vault`: the CLI does not add
secrets-manager drivers. Install the driver, construct it as the
[driver guide](drivers.md) shows and add it with `sources.Source("aws", driver)`.
A driver with a lifecycle, such as Vault, needs `compose.Startup(driver.Start)` and
`compose.Cleanup(driver.Stop)` in `Secrets` before the store is used. Keep a ring
in the manager as one secret whose text is the comma-separated ring, and rotate it
with the [ring commands](../cli/keys.md#rotate-rings).

## Testing

`memorysecret` is a driver, so a test can register it under a scheme (for example
`sources.Source("memory", memorysecret.New(...))`) and read the same references
the application uses. A driver of your own must pass the `secrettest` conformance
suite; its options describe read-only sources, text-only backends, and a mux that
needs the scheme prefix.
