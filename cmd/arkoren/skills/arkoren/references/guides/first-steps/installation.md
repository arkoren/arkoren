---
title: Installation
description: Choose Arkoren modules and add a capability to an existing Go application without changing its structure.
order: 10
summary:
  - Check the Go requirement and select the module that owns your dependency.
  - Install core, independent foundation libraries, or vendor integrations as needed.
  - Adopt a cache without replacing your router, entry point, or dependency wiring.
---

# Installation

Add the Arkoren capability your application needs, keeping your existing packages
and entry point. You do not need the CLI, a generated project, an Arkoren router,
or `application.Application` to use a library or service facade.

For a new HTTP service with bootstrap files, use [Creating Projects](creating-a-project.md)
after checking the requirements below. Installation itself does not generate an application.

## Go Requirements

Use Go 1.27 or later. Check the toolchain before adding dependencies:

```sh
go version
```

Run installation commands from the directory containing your application's
`go.mod`. In a repository with several Go modules, choose the module that will
import the package. If you have no module yet, create one in your application's
directory with `go mod init example.com/your-app`, using your intended module path.

Core has no external-service prerequisite. Vendor integrations can require a
running backend, credentials, or native dependencies; follow the relevant
[driver guide](../drivers/choosing-a-driver.md) before connecting one.

## Choosing Modules

`arkoren.dev` is an import namespace, not a module you install. Choose the module
that contains the package you want:

| Need | Module | Example Import |
| --- | --- | --- |
| HTTP, application lifecycle, or service facades | `arkoren.dev/core` | `arkoren.dev/core/service/cache` |
| An independent library | `arkoren.dev/foundation/<name>` | `arkoren.dev/foundation/validate` |
| A vendor integration | `arkoren.dev/driver/<vendor>` | `arkoren.dev/driver/redis/rediscache` |

To use a core package, add core:

```sh
go get arkoren.dev/core
```

This includes the core service contracts and their bundled local implementations,
including the memory cache. It does not start services or require that you use
other core packages. Go compiles only packages reachable through your imports.

Foundation libraries are independent modules. If you need validation without
core, install it directly:

```sh
go get arkoren.dev/foundation/validate
```

Each foundation module uses only the standard library. When core already requires
a foundation library, Go resolves that dependency automatically.

Add a vendor module when your application needs its implementation. For example,
a shared Redis cache uses:

```sh
go get arkoren.dev/driver/redis
```

The module contains vendor-specific packages such as `rediscache`, rather than
replacing the core `cache` package. Installing it does not configure credentials,
connect to Redis, or register cleanup. [Cache Drivers](../cache/drivers.md) owns
that setup. Integrations marked pending in the driver guides are unpublished;
do not infer an installation command from their proposed package names.

## Existing Applications

Keep dependency construction wherever your application already does it. A cache
has a **facade**, which application code calls, and a **driver**, which stores
the values. With these imports, construct a process-local cache:

```go
import (
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)
```

The construction needs no network service or credentials:

```go
driver := memorycache.New()
store := cache.New(driver)
```

Pass `store` to the code that needs it through your existing constructors or
struct fields. A library can accept a caller-supplied cache instead of creating
one. Neither approach requires changes to your HTTP stack.

To cache a rebuildable display label, define its value type and write it using
an existing operation context `ctx`. Import `time` for the expiry:

```go
label := cache.NewKey[string]("catalog.label.featured", func(entry *cache.KeyBuilder) {
	entry.TTL(time.Minute)
})
err := store.Put(ctx, label, "Featured Items")
```

The key supplies the exact storage name, `string` value type, and one-minute write
TTL. Check `err` in application code. The guides' focused fragments keep routine
error results visible without repeating propagation; complete runnable examples
handle their errors.

The memory driver's constructor starts no goroutines. Expired entries are not
returned even without its optional expiry janitor. Values disappear on process
exit and are not shared between replicas, so this is not durable storage. If you
start the janitor or adopt a network driver, the code that owns the driver must
also stop it; see [Cache Drivers](../cache/drivers.md).

Continue with [Cache Overview](../cache/overview.md) for reads and misses, or
[Key Definitions](../cache/keys.md) for reusable names and expiry policies.

## Optional Tooling

The CLI is separate from runtime dependencies. Install it if you want project
generation, bundled documentation, or diagnostics commands:

```sh
go install arkoren.dev/cmd/arkoren@latest
```

Alternatively, pin it as a tool in your application's module:

```sh
go get -tool arkoren.dev/cmd/arkoren@latest
```

Use `go tool arkoren` for the module-tool installation. For executable lookup
and `PATH`, see [CLI Installation](../cli/overview.md#installing-arkoren).
Then [create a project](creating-a-project.md) or keep integrating individual
packages into the application you already have.
