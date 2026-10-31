---
title: Introduction
description: What Arkoren is, the ideas behind its design, what ships in the box, and how this documentation is organized.
order: 10
---

# Introduction

## Introduction

Building a Go service often means connecting several libraries before you can
work on the service itself. Arkoren is a modular toolkit that gives those pieces
shared conventions without asking you to adopt a framework. Import and call the
packages you need; nothing is discovered, generated, or injected behind your back.

You can start with an HTTP router and server, request and response helpers,
middleware, or an application lifecycle. Add services such as a database, cache,
queues, mail, file storage, workflows, or language models as you need them. Each
piece is an ordinary Go package, not a requirement to adopt the rest.

Deciding whether to adopt it? [Should I Choose Arkoren?](choosing-arkoren.md)
compares the trade-offs with assembling packages yourself, explains adoption
alongside an existing router, and identifies the limits to evaluate first.

The core of Arkoren is built on the standard library: `net/http` for HTTP,
`log/slog` for logging, and `context.Context` for cancellation and deadlines.
Anything that needs a third-party library, such as a PostgreSQL client or the
AWS SDK, lives in a separate driver module that you add only when you need it.
This keeps vendor dependencies out of core. Portable service calls can stay the
same when you replace a vendor; the new driver's configuration and limits still
need review.

If you already know Go's `net/http`, most of Arkoren will feel familiar. A
handler is still a function that receives an `http.ResponseWriter` and an
`*http.Request`. The router still sits on top of `http.ServeMux`. The server is
still an `http.Server`, configured with safer defaults. Arkoren adds structure
around these types rather than replacing them.

## A toolkit, not a framework

A framework calls your code. A toolkit is called by your code. Arkoren is
deliberately the second kind: you construct every object yourself, in plain Go,
and you decide how the objects are connected. The only time Arkoren calls back
into your code is through functions you registered explicitly, such as an HTTP
handler, a lifecycle hook, or a queue job handler.

This has a few practical consequences that shape the rest of the documentation:

- **Every package works on its own.** You can use the router without the
  application lifecycle, the database facade without the router, or the
  validation package in a command-line tool that has nothing to do with HTTP.
- **Wiring is visible.** The `main` function of an Arkoren service reads from
  top to bottom: build a driver, wrap it in a facade, register its start and
  stop hooks, build the router, start the server. There is no container to
  inspect and no configuration file that silently changes behavior.
- **Dependency injection is your choice.** If you prefer a container such as
  wire, fx, or dig, you can use one. Arkoren does not require one, and its
  constructors are shaped so that containers can call them directly. See
  [Application Composition](../application/composition.md).

## Contracts, facades, and drivers

You may want an in-memory cache while developing and Redis when several servers
must share the same entries. Arkoren separates the code that uses a service
from the code that talks to its backend, so that change stays in your setup.

Most services follow one pattern. A **contract** is a small Go
interface, named `Driver`, that describes what a backend must be able to do:
store a value in a cache, put a message on a queue, send an email. A **facade**
is the type your application actually uses. It wraps a driver and adds the
behavior that should be identical everywhere: typed helpers, validation of
keys and names, events, and telemetry. A **driver** is an implementation of the
contract for one vendor, such as Redis for the cache or SQS for queues.

Keep portable application code dependent on the facade, not a specific driver. To
move from an in-memory cache in development to Redis in production, you change
the line that builds the driver; everything that reads and writes the cache
stays the same. Driver configuration, lifecycle, durability, and supported
operations still need review. [Facades and drivers](../core-concepts/facades-and-drivers.md)
explains the pattern in detail, and [Contracts](../core-concepts/contracts.md)
describes what every contract promises.

Most contracts ship with an in-process **memory driver** inside the core
module: cache, storage, queues, broadcasting, secrets, scheduling, workflows,
and telemetry, plus a mail driver that writes messages to the log. Memory
drivers need no infrastructure, so you can run a complete service on your
laptop, and in tests, before you choose a vendor. The database runs on SQLite
for the same purpose, and encryption and hashing need no server at all.

Every contract also ships a **conformance suite**: a set of tests that every
driver, including the memory drivers and any driver you write yourself, must
pass. The suite checks shared behavior; it does not make backend-specific
durability, configuration, or supported operations identical.

## Ideas behind the design

A handful of principles explain most of the decisions you will meet in these
guides.

**Errors are values.** HTTP handlers in Arkoren return an `error`. The router
renders returned errors as RFC 9457 Problem Details responses when the response
is not already committed. A handler does not need to write that error body by
hand. Services return sentinel errors such as
`database.ErrNoRows` that you check with `errors.Is`.

**Explicit configuration.** A constructor call shows its dependencies and required
settings. Optional settings have defaults; change them with a builder callback
when you create the object. Published settings do not change when you reuse a
callback elsewhere. Individual calls can have their own options without
reconfiguring the shared object. See
[Configuration](../first-steps/configuration.md).

**Startup is explicit.** Drivers with a `Start` method connect or check their
backend there, and you register it as a lifecycle hook. Some cloud drivers
instead load SDK configuration in their constructors and make service requests
on each operation; they have no `Start` hook. Each driver guide states its
setup and lifecycle requirements. Register available startup checks so a
misconfigured backend fails before the service handles requests. See
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md).

**Observable by default.** Facades emit typed events and, when you enable
telemetry, traces and metrics, without any code in your handlers. Logging uses
the standard `*slog.Logger` everywhere, so it works with whatever log pipeline
you already have.

**Native clients remain reachable.** Drivers that wrap a vendor client expose
it for capabilities outside the portable contract. Code using that client is
vendor-specific; keep it separate from portable service calls.

## What ships in the box

Arkoren is split into a few families of Go modules. You install only the ones
you use.

The **core** module, `arkoren.dev/core`, contains the application lifecycle,
everything on the HTTP request path (router, server, requests, responses,
middleware, authentication, sessions, secure links, and an HTTP client), the
contract and facade of every service, the memory drivers, and the conformance
suites. It depends only on the standard library and the foundation modules.

The **foundation** modules are small, standard-library-only packages that are
useful even outside an Arkoren service: `collection` for generic slice and map
helpers, `key` for validated cryptographic material, `problem` for Problem Details responses, `query` for building
parameterized SQL, and `validate` for schema-based validation. Each is its own
module with its own release cadence.

The **driver** modules hold the implementations that talk to external systems.
There is one module per vendor, such as `arkoren.dev/driver/redis` or
`arkoren.dev/driver/aws`, and each module contains one package per contract it
implements, such as `rediscache` and `redisbroadcast`. Packages are named
`<vendor><contract>` so they never clash with the contract package and never
need an import alias.

[Installation](../first-steps/installation.md) shows how to add each family
to your project.

## How this documentation is organized

These guides are written to be read. They start with installing Arkoren and
building a first service, then explain the architecture. The
[Application](../application/overview.md) guides cover composition, lifecycle,
errors, and readiness. The remaining groups cover each
area (HTTP, security, the database, messaging, scheduling, storage,
observability, and testing) in the order you are likely to need it. Each
service has its own section that explains how to use it, how it behaves, and
how to choose and configure a driver for it.

For exact declarations and symbol contracts, run `go doc` from your application's
module directory, for example `go doc arkoren.dev/core/application.New`. Go
uses the source selected by your module graph and active workspace, so check
that selection when the version matters. Online, use the website API catalog
to find the module on pkg.go.dev, then select its package and version. The
guides link directly to relevant source references; neither the skill nor the
website maintains copied signature inventories.

Focused snippets show the ordinary success path and omit routine error checks,
while keeping `err` visible. Checks remain when they teach failure handling or
guard a consequential action. Complete programs handle errors before using
results; your application must do the same.

The same documentation also ships as an agent skill, so a coding agent working
on your project reads exactly what you read here. See
[Working with Agents](../agent-skills/working-with-agents.md).
