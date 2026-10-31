---
title: Installation
description: Bootstrap a project with the Arkoren CLI and add modules as you need them.
order: 10
---

# Installation

## Introduction

Arkoren is not a single dependency. It is a set of Go modules that you add to
your project one at a time, as you need them. A service that only serves HTTP
needs the core module. A service that also stores data in PostgreSQL adds the
PostgreSQL driver module. Go downloads those modules and their dependencies;
only packages your program imports, directly or indirectly, are compiled into
your binary.

For a new service, start with `arkoren new` as shown below. This page also
explains how to add individual modules to an existing project. The
[Quickstart](quickstart.md) extends the generated service with a route, a cache
counter, and error responses.

## Requirements

Arkoren requires **Go 1.27** or later. The core packages use generic methods,
which earlier Go versions do not support. Check your version with:

```sh
go version
```

The core module has no other requirements: it depends only on the standard
library and on Arkoren's own foundation modules. Drivers have the
requirements of the systems they talk to. A PostgreSQL driver needs a
PostgreSQL server to connect to, for example, and each driver's guide explains
local setup when a local backend is supported.

## Creating a project

Install the [Arkoren CLI](../cli/overview.md#installing-the-cli), then run this
from the directory that will contain your new project:

```sh
arkoren new hello --module example.com/hello
cd hello
```

The command writes an HTTP service with configuration, health probes, lifecycle
wiring, and debug-only MCP route diagnostics, but no tests. It does not download
dependencies. Resolve imports and check the code from the project root:

```sh
go get arkoren.dev/core arkoren.dev/driver/mcp
go mod tidy
go vet ./...
```

Then follow the [Quickstart](quickstart.md#your-first-application) to test your
first request.

For previews, directory safety, and direct composition, see
[Creating a Project](creating-a-project.md).

## How the modules are named

Every Arkoren import path starts with `arkoren.dev`. The domain is a namespace,
not a module: there is no `arkoren.dev` module to install. Below it, the
modules fall into three families.

The **core** module is `arkoren.dev/core`. One `go get` gives you every core
package: the application lifecycle in `arkoren.dev/core/application`, the HTTP
packages under `arkoren.dev/core/http/` (such as `router`, `server`,
`request`, `response`, and the middleware under `http/middleware/`), and the
services under `arkoren.dev/core/service/` (such as `cache`, `database`,
`queue`, and `mail`) and their test helpers. Most services include a memory
driver; the database uses SQLite for local tests, mail has a log driver, and
language models use a scripted fake.

The **foundation** modules are small libraries that do not depend on the rest
of Arkoren: `arkoren.dev/foundation/certificate`, `arkoren.dev/foundation/collection`,
`arkoren.dev/foundation/key`, `arkoren.dev/foundation/problem`,
`arkoren.dev/foundation/query`, and `arkoren.dev/foundation/validate`. Each one is
a module of its own and can be used without core. Installing core brings its
foundation dependencies automatically; add independent modules such as
[`certificate`](../http-networking/certificates.md) explicitly when your
application imports them.

The **driver** modules connect a service to a vendor. There is one module per
vendor, named `arkoren.dev/driver/<vendor>`, and one package inside it per
service the vendor supports, named `<vendor><service>`. For example, the Redis
module `arkoren.dev/driver/redis` contains `rediscache` for the cache and
`redisbroadcast` for broadcasting. Because the package name always combines
the vendor and the service, it never collides with the service package
(`cache`) and you never need an import alias:

```go
import (
	"arkoren.dev/core/service/cache"
	"arkoren.dev/driver/redis/rediscache"
)
```

## Installing the core

Add the core module from the root of your Go module:

```sh
go get arkoren.dev/core
```

This is the only module most services need to begin with. It includes an
in-process memory driver for most services, so you can build and test a
complete application before you pick any infrastructure.

## Installing drivers

When you are ready to connect a service to real infrastructure, add the
vendor's module. To store data in PostgreSQL and cache it in Redis, for
example:

```sh
go get arkoren.dev/driver/postgres
go get arkoren.dev/driver/redis
```

Installing a vendor module gives you every package in it, but only the
packages you import are compiled into your binary. Each vendor module brings
in that vendor's client library, such as `pgx` for PostgreSQL or `go-redis`
for Redis; that is the reason drivers live outside the core.

[Choosing a driver](../drivers/choosing-a-driver.md) lists every service and
the drivers available for it.

Some driver modules serve a protocol rather than connect to infrastructure.
To expose MCP tools, add the `arkoren.dev/driver/mcp` module alongside core:

```sh
go get arkoren.dev/driver/mcp
```

This module contains `mcpserver`, an HTTP handler backed by
the official Go MCP SDK. [Your First MCP Server](../mcp/first-server.md) covers
the complete installation, listener, and client connection.

## Installing foundation modules on their own

If you only need one of the foundation libraries, for example the validation
package in a command-line tool, install it directly:

```sh
go get arkoren.dev/foundation/validate
```

## Next steps

The [Quickstart](quickstart.md) walks you through creating and extending a
service with the CLI: routes, JSON responses, a cache counter, error responses,
health probes, and graceful shutdown. If you use a coding agent, also install the
documentation as an agent skill, as described in
[Working with Agents](../agent-skills/working-with-agents.md).
