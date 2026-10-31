---
title: Use in an Existing Project
nav: Existing Project
description: Add Arkoren to an existing Go application without the CLI or a generated service.
order: 5
---

# Use in an Existing Project

## Introduction

Add the Arkoren capability you need without reorganizing your project. Your
project can be a library, command, HTTP service, or another Go program. This
guide assumes an existing Go module, but no directory layout, router,
application entry point, or dependency-injection approach.

You do not need the Arkoren CLI, generated files, or `application.Application`.

## Choosing a package

Start with a capability your project needs. For example, you can use
[validation](../validation/overview.md) without an HTTP server, or add a
[cache](../cache/overview.md) without changing your router. Independent
[foundation modules](https://pkg.go.dev/arkoren.dev/foundation/validate) have their own installation
commands; not every package requires core.

The optional example below uses a cache to demonstrate installation and a
service call without external infrastructure. It is not a required first step
or a suggested application architecture.

## Installing the module

Use Go 1.27 or later. Identify the Go module that will import the package. If
your repository contains several modules, choose the relevant one rather than
assuming the repository root owns the dependency.

For the cache example, run this command from the directory containing that
module's `go.mod`:

```sh
go get arkoren.dev/core
```

This adds the core module and its dependencies to your module's dependency
files. You import only the packages you need; installing core does not start
an application or require you to adopt its HTTP or lifecycle packages.

## Running a cache example

If you want to try the API before integrating it, save the standalone program
below in a separate, unused directory of your choice within that module. Its
`package main` must not be mixed with an existing package. Choose your own
directory and filename; this guide does not require a `cmd/` or `internal/`
layout. Leave your existing source files unchanged.

The cache has two parts: a **facade**, `*cache.Cache`, which your code calls,
and a **driver**, which stores the values. The memory driver needs no external
service or credentials. Its state is local to this process and disappears when
it exits, so use it only for values you can rebuild, not durable records.

The following complete program stores a greeting for one minute and reads it
back as a string:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// main demonstrates an isolated cache operation without application startup.
// Operation failures are logged before the command exits unsuccessfully.
func main() {
	ctx := context.Background()
	store := cache.New(memorycache.New())

	key := cache.NewKey[string]("greeting.v1.demo", func(entry *cache.KeyBuilder) {
		entry.TTL(time.Minute)
	})
	if err := store.Put(ctx, key, "Hello from your existing project!"); err != nil {
		slog.Error("store greeting", "error", err)
		os.Exit(1)
	}

	greeting, err := store.Get(ctx, key)
	if err != nil {
		slog.Error("read greeting", "error", err)
		os.Exit(1)
	}

	fmt.Println(greeting)
}
```

`Put` JSON-encodes the value; `Get` decodes it as the key's `string` type.
The key supplies the exact name `greeting.v1.demo` and one-minute write TTL.
Missing or expired keys return `cache.ErrNotFound`; incompatible stored values
return a decoding error. This command logs either failure and exits with status 1.
In your application, [handle a cache miss](../cache/overview.md#storing-and-reading-values)
by rebuilding the value from its source.

From the directory containing the selected module's `go.mod`, run the file
directly. Replace `path/to/example.go` with its relative path:

```sh
go run path/to/example.go
```

On success, it prints:

```text
Hello from your existing project!
```

The memory driver starts no goroutines at construction. Expired values are
never returned, even without a background cleanup task. This short-lived
command needs no startup or shutdown hooks and leaves no persistent resources.
After trying it, you can delete only the example file you created.

## Adding the cache to your application

If you choose to integrate the cache, construct it wherever your project
already creates dependencies. Pass it to the code that needs it using your
existing approach: a constructor parameter, a struct field, or your chosen
dependency-injection mechanism. A library can accept a cache supplied by its
caller instead of creating or owning one.

The standalone program demonstrates API calls, not code to paste into an
existing `main` function. Keep your package names, file locations, entry points,
and HTTP stack. Constructing the cache does not require an Arkoren router or
application lifecycle.

If several processes must share cached values, choose a shared backend using
[Cache Drivers](../cache/drivers.md). Follow that driver's startup and cleanup
requirements through the code that owns that dependency's lifetime. If you are
writing a library, make that ownership clear to its caller. Importing a driver
does not connect it automatically. See [Cache Overview](../cache/overview.md) for typed values,
expiry, and rebuilding values on a miss, and the
[`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache) for every operation.
