---
title: Configuration
description: Translate application settings into typed construction callbacks while preserving component defaults and ownership.
order: 40
summary:
  - Separate required constructor arguments from optional builder settings.
  - Preserve defaults and reuse typed policies with explicit local overrides.
  - Read and validate application settings before passing them to components.
  - Distinguish published settings from shared clients and lifecycle validation.
---

# Configuration

Your application decides where settings come from; Arkoren components receive
the values they need when you construct them. Required dependencies and settings
are constructor arguments. Optional settings use a **builder callback**, a
function that edits a temporary construction draft.

Keep environment and file formats in your application rather than decoding them
into package-internal settings. This lets your deployment configuration stay
stable as you change components or drivers.

## Component Construction

With an existing HTTP handler named `handler`, construct a server using its
defaults. Import `arkoren.dev/core/http/server`:

```go
listener := server.New(handler)
```

The handler is a required dependency. Construction does not bind a socket;
`Start` opens the listener. The generated project registers that startup and
cleanup in `internal/bootstrap/server.go`.

To select a loopback address, change only that setting during construction:

```go
listener := server.New(handler, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:9090")
})
```

These are alternative constructions, not consecutive declarations. The callback
changes the address without replacing the server's other defaults. Its parameter
is a `*server.Builder`, not a server you can reconfigure later. After construction,
changing a retained draft does not change the published component.

Required arguments can also be settings with no safe default, such as a validated
signing key or a database connection string. Their validation and provisioning
belong to the relevant [key](../keys/overview.md) or
[driver guide](../database/drivers.md), not an optional callback.

## Preserving Defaults

Omit settings you do not need to change. For example, the server's default header
read timeout is 10 seconds; changing its address leaves that timeout in place.
Its default address is `:8080`, which listens on all interfaces, not only loopback.
Defaults are not a substitute for application security policy.

Zero and empty values are package-specific. The server restores its default for
an empty address or zero timeout. Its negative write timeout disables the write
deadline; that exception is useful for streaming but should not be applied to
ordinary endpoints without a reason. Other builders can treat an empty value as
an explicit instruction to disable or clear a setting.

Do not copy every documented default into your callback or assume every
collection setting appends. Check the particular method's contract when replacing
lists, clearing values, or disabling protection.

## Shared Policies

When several listeners need the same header deadline, give that construction
policy a typed name. Import `time` for the duration:

```go
headers := server.BuilderFunc(func(listener *server.Builder) {
	listener.ReadHeaderTimeout(5 * time.Second)
})
```

Apply the policy, then add the settings specific to this listener:

```go
listener := server.New(handler, headers, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:9090")
})
```

Callbacks run once, synchronously, in argument order on one fresh draft. A later
scalar setting replaces an earlier one. `server.BuilderFunc` is an alias for the
ordinary callback type, so it also works in slices passed with `policies...`.
Use the alias belonging to the constructor's role; a server policy cannot configure
a cache or queue consumer.

To omit optional policies, omit arguments or expand an empty slice. Supplying a
nil callback panics; it does not mean “use defaults.” Do not mutate the draft
concurrently with construction.

## Environment Settings

Read values beside the code that constructs their consumer. In the generated
`internal/bootstrap/server.go`, the existing factory callback imports `os` and
constructs its listener with:

```go
listener := server.New(handler, func(listener *server.Builder) {
	listener.Addr(os.Getenv("HTTP_ADDR"))
})
```

The factory reads `HTTP_ADDR` when composition invokes its callback, not when
`Server(handler)` returns the factory. Empty or unset values preserve `:8080`.
Later environment changes do not reconfigure that listener.

Core packages do not load your application environment or files. The starter's
executable separately imports `godotenv/autoload`, which reads `.env` during
initialization without overriding process environment values. Its loading errors
are ignored, so validate required settings explicitly rather than treating a
present `.env` as proof of valid configuration.

For example, if you add a required service credential, reject a missing value in
your application's loader before constructing the client. In a function returning
an error, with `errors` and `os` imported:

```go
token := os.Getenv("REPORTS_TOKEN")
if token == "" {
	return errors.New("REPORTS_TOKEN is required")
}
```

Do not log the token or include it in the error. This validation establishes
presence, not authorization or remote reachability. A driver's documented startup
or first operation establishes those later conditions.

## Typed Settings

Extract a loader when several components share values or parsing becomes
substantial. Keep its result application-owned. For example, a server loader
could return this type from your own `internal/settings` package:

```go
type HTTP struct {
	Addr            string
	ShutdownTimeout time.Duration
}
```

Import `time` in that package. Begin with a zero `ShutdownTimeout`, preserving the
server's default, and parse an override only when it is present. Inside a loader
returning `(HTTP, error)`, use `errors`, `os`, and `time`:

```go
cfg := HTTP{Addr: os.Getenv("HTTP_ADDR")}
if value := os.Getenv("HTTP_SHUTDOWN_TIMEOUT"); value != "" {
	timeout, err := time.ParseDuration(value)
	if err != nil || timeout <= 0 {
		return HTTP{}, errors.New("HTTP_SHUTDOWN_TIMEOUT must be a positive duration")
	}
	cfg.ShutdownTimeout = timeout
}
```

This application policy permits positive overrides such as `20s`; it rejects
malformed, zero, or negative input rather than silently changing shutdown behavior.
Return `cfg, nil` after any other application checks. The fragments show the
loader's type and parsing branch, not a complete loader implementation.

After checking the loader's error, translate its result into component settings
where you construct the server:

```go
listener := server.New(handler, func(listener *server.Builder) {
	listener.Addr(cfg.Addr)
	listener.ShutdownTimeout(cfg.ShutdownTimeout)
})
```

An absent override leaves the server's 15-second drain default intact. Keep your
JSON/YAML schema or environment variable names on `HTTP` and its loader, not on
an Arkoren reporting snapshot. Components that expose `Config()` do not thereby
accept that record as constructor input.

## Validation Boundaries

Check each constructor's contract rather than assuming every mistake becomes a
returned error at construction. Some constructors return configuration errors;
some middleware constructors panic for invalid code-authored policy; reusable
definitions can retain domain errors until registration or use. Network and TLS
failures can appear during startup. A successful constructor does not establish
that a backend is reachable.

Publication separates owned settings from the temporary draft, not every object
reachable from it. Clients, service pointers, functions, captures, and native TLS
configuration can remain shared. Finish their setup before concurrent use.
Before wrapping a native client with `NewFrom`, read the driver's ownership rules:
wrapping does not necessarily mean borrowing, and `Stop` can close that client.

For startup and cleanup ownership, continue with
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md). The
[`server` API reference](https://pkg.go.dev/arkoren.dev/core/http/server)
documents the defaults and builder methods used here.
