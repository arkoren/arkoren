---
title: Project Layout
description: Organize a service by feature, with one package per feature that owns its routes, rules, and storage, and a main function that wires them.
order: 40
---

# Project Layout

## Introduction

As a service grows, you need to find and change one feature without searching
through every technical layer. This page recommends a **feature-driven** layout:
keep related behavior together and connect features in one visible place.
Arkoren does not look for files in particular locations, so this is a convention,
not a runtime requirement.

In a feature-driven layout, the code is grouped by what the service does, not
by technical layer. Everything about invoices lives in an `invoice` package:
its types, its business rules, its database queries, its HTTP handlers, and
its tests. You do not spread one feature across `handlers/`, `models/`, and
`repositories/` folders. Adding a feature means adding a package, and
removing one means deleting it.

The layout rests on two rules. **Each feature is a package that owns its
slice of the service**, from the route to the table. And **wiring lives in one
place**: a `main` function builds the drivers and facades and hands each
feature what it needs. A generated project keeps this composition in `internal/bootstrap`, called by `main`;
the same rule applies when you move wiring out of the executable package.

The optional [`arkoren new` command](creating-a-project.md) starts with
`cmd/server` and `internal/bootstrap`. Its bootstrap separates composition
(`bootstrap.go`), application settings (`app.go`), logging (`logging.go`), routes
(`router.go`), and listener settings and lifecycle (`server.go`), with debug-only diagnostics in `mcp.go`. It generates
no tests; add them as behavior grows. `Router`, `Server`, and `MCP` return typed
factories. The root obtains the shared router with `routes := compose.Use(Router())`,
then passes it to `MCP` through `UseDebug` and `Server` through `Use`.
Resource construction and route registration happen inside the returned callbacks,
not while obtaining a factory. Each callback returns its constructed component;
startup runs only after composition succeeds.
You own these files and can extend them into the
layout below.

## The layout

A service with two features might look like this:

```text
billing/
├── cmd/
│   └── server/
│       └── main.go        application, signals, and process exit
├── internal/
│   ├── bootstrap/
│   │   ├── app.go        application construction policy
│   │   ├── bootstrap.go  wiring and application lifecycle
│   │   ├── bootstrap_test.go
│   │   ├── logging.go    application log handler factory
│   │   ├── mcp.go        debug diagnostics
│   │   ├── router.go     routes and feature registration
│   │   └── server.go     HTTP server construction
│   ├── customer/          one feature
│   │   ├── customer.go    types and rules
│   │   ├── store.go       database access
│   │   ├── http.go        handlers and routes
│   │   └── customer_test.go
│   └── invoice/           another feature
│       ├── invoice.go
│       ├── store.go
│       ├── http.go
│       └── invoice_test.go
├── compose.yaml           local infrastructure
└── go.mod
```

Everything under `internal/` can only be imported by your own module, which
leaves you free to rename and restructure it.

## Feature packages

A feature package is named after the thing it manages, in the singular:
`customer`, `invoice`, `subscription`. Inside, it usually has three kinds of
code.

The **types and rules** describe the feature in plain Go, with no knowledge
of HTTP or SQL. This is the code that changes most often, and keeping it free
of infrastructure makes it easy to test.

The **store** reads and writes the feature's data. It receives a service
facade, such as the database, rather than a specific driver, so portable queries
can run through PostgreSQL or SQLite. SQL dialect features can still differ. In your
`internal/invoice/store.go`, import `context`, `arkoren.dev/core/service/database`,
and `arkoren.dev/foundation/query`. With an application-owned `Invoice` result
type whose fields map to the selected columns, a lookup can be:

```go
type Store struct {
	db *database.Database
}

func (store *Store) Find(ctx context.Context, id int) (Invoice, error) {
	return store.db.Find[Invoice](ctx, query.Table("invoices", func(statement *query.Builder) {
		statement.Where("id", id)
	}))
}
```

`Find` returns one invoice, or `database.ErrNoRows` when no row matches. This
fragment assumes the `invoices` table already exists; create it through your
application's [migrations](../database/migrations.md).

The **HTTP layer** holds the handlers and a function that registers the
feature's routes. Keeping the routes next to their handlers means a feature's
whole URL structure is visible in one file. In `internal/invoice/http.go`, import
`arkoren.dev/core/http/router`; `handlers`, `list`, `create`, and `show` below are
your application's handler type and methods:

```go
func Routes(routes *router.Router, store *Store) {
	endpoint := &handlers{store: store}
	routes.Get("/invoices", endpoint.list)
	routes.Post("/invoices", endpoint.create)
	routes.Get("/invoices/{id}", endpoint.show)
}
```

Calling `Routes` mounts the endpoints; it does not start an HTTP listener.

A feature can grow more files as it needs them, such as `jobs.go` for its
queue jobs or `events.go` for the events it publishes. They stay in the same
package, because they belong to the same feature.

## How features talk to each other

Features should depend on as little of each other as possible. When the
invoice feature needs to know about customers, there are two good options.

If it needs an answer right away, it depends on a small interface that it
defines itself, and `main` passes in the customer store, which already
satisfies it:

```go
// Customers is what invoices need to know about customers.
type Customers interface {
	Exists(ctx context.Context, id int) (bool, error)
}
```

If it only needs to react to something that happened, such as a customer being
deleted, it listens to an event that the customer feature publishes on the
application's [event bus](../events/overview.md). The customer feature does
not know who listens.

Either way, `invoice` never imports `customer`, so each feature can be changed
and tested on its own. If two features constantly need each other's details,
that is usually a sign that they are one feature.

## The `main` function

In a generated project, `cmd/server/main.go` creates the application through `application.New` and calls
`app.Run(ctx, bootstrap.Compose)`. Keep shared object construction and feature wiring in that composition
root. With an existing database `driver`, router `r`,
and application-owned `customer` and `invoice` packages, it shows every dependency:

```go
db := database.New(driver)

customers := customer.NewStore(db)
customer.Routes(r, customers)
invoice.Routes(r, invoice.NewStore(db, customers))
```

This is the only place where features meet. Adding a feature is one more
line here, and the compiler tells you if you forgot to pass something it
needs. When `main` grows long, split it into plain functions in the same
package rather than hiding it behind a framework. If you prefer a dependency
injection container, see
[Application Composition](../application/composition.md).

## Configuration

Keep environment reads beside the component wiring in `bootstrap`. The generated
starter reads `HTTP_ADDR` inside `Server`'s composition callback and passes it to
the server builder. In `internal/bootstrap/server.go`, the listener is constructed
with this policy (imports include `os` and `arkoren.dev/core/http/server`):

```go
listener := server.New(handler, func(listener *server.Builder) {
	listener.Addr(os.Getenv("HTTP_ADDR"))
})
```

Empty or unset `HTTP_ADDR` preserves the recommended `:8080` default. Obtaining
the `Server(handler)` factory reads nothing; its composition callback reads the
environment when constructing the listener. Later environment changes do not
reconfigure that listener. Required values such
as a database URL can be read and validated separately when the application needs them.

Core packages do not load your application settings from the environment.
Your composition root passes required values as arguments and optional policies as builder callbacks.
Vendor drivers may also use their SDK's default credential sources. See
[Configuration](configuration.md).

## Testing a feature

Because a feature receives everything as arguments, a test builds it with
in-memory drivers or SQLite in a few lines and exercises it through its routes
or its store. No server, container, or global setup is needed.
[Testing Overview](../testing/overview.md) shows the patterns.

## Local infrastructure

When a feature starts to use real infrastructure, add a `compose.yaml` at the
root that starts it locally, such as the database and a cache. A new
contributor then runs the service with `docker compose up -d` and
`go run ./cmd/server`. Until then, the memory drivers make the compose file
unnecessary.

## Starting small

A service with one or two endpoints needs no feature packages; the generated
bootstrap in the [Quickstart](quickstart.md) is enough. Move to feature
packages when the second feature appears. Because Arkoren does not depend on
where your files live, the move is an ordinary refactoring.

Continue with [Application](../application/overview.md) for composition and
lifecycle guidance, or [Composition](../application/composition.md) if several
executables need to share the same wiring.
