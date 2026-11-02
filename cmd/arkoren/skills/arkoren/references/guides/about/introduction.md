---
title: Introduction
description: Understand Arkoren's modular approach and choose a starting point for your Go application.
order: 10
summary:
  - Adopt individual capabilities without replacing your application's router or lifecycle.
  - Understand how service facades separate application calls from backend implementations.
  - Find existing-application, new-project, CLI, and API-reference starting points.
---

# Introduction

Arkoren is a modular Go toolkit for building services. You can add a cache to an
existing application, use its HTTP packages for a new service, or adopt its
application lifecycle to coordinate startup and shutdown. Each choice is separate:
using one capability does not require adopting the rest.

## Modular Adoption

Keep the parts of your application that already work. Arkoren services do not
require its router, and neither the router nor the services require
`application.Application`. You construct dependencies in Go and pass them to the
code that uses them. The optional [application lifecycle](../application/overview.md)
coordinates the startup hooks, workers, and cleanup you explicitly register; it
does not discover dependencies or manage resources merely because you created them.

The module boundaries let you make the same choice about dependencies. Core,
`arkoren.dev/core`, provides HTTP packages, application lifecycle, and service
APIs. It depends on standard-library-only foundation modules, not vendor SDKs.
Foundation libraries, such as validation and SQL statement construction, are also
available independently. Vendor integrations live in separate driver modules,
which bring their own dependencies when you add them.

If you are deciding whether these conventions suit your application, read
[Choosing Arkoren](choosing-arkoren.md) before installing anything.

## Shared Service APIs

A cache used by one process has different storage needs from a cache shared by
several servers. Arkoren separates those backend choices from the code that uses
the cache. A **facade** is the application-facing API; a **driver** implements
the service's **contract**, the Go interface that specifies its backend behavior.

For example, you can use the cache facade with its bundled memory driver for
process-local data, then configure a Redis driver when servers need shared state.
Calls through the facade retain the same API. That does not make the backends
equivalent: memory-cache contents disappear with the process, while Redis requires
a running service and its own configuration and operating policy.

The [facades and drivers guide](../core-concepts/facades-and-drivers.md) explains
this separation. Driver guides cover setup and backend limits; conformance suites
test shared contract behavior rather than proving identical durability or
performance.

## Where to Begin

Choose the path that matches what you have already built:

- For an existing Go application, follow [Installation](../first-steps/installation.md#existing-applications)
  to add one capability without replacing your entry point or HTTP stack.
- For a new service, use [Creating Projects](../first-steps/creating-a-project.md)
  for bootstrap files, then follow the [Quickstart](../first-steps/quickstart.md)
  to run it and verify an HTTP response.
- To install the optional command-line tool, follow
  [Installing Arkoren](../cli/overview.md#installing-arkoren). Library use does not
  require the CLI.

After that first task, read the guide for the capability you need rather than
working through every category. Each service guide explains ordinary usage and
links to driver-specific setup where backend choices matter.

## Reading the Guides

Focused code snippets assume the collaborators named in the surrounding prose.
They keep error results visible but omit routine error propagation. Checks remain
when they explain recovery, authorization, resource ownership, or another
consequential decision. Complete programs handle errors before using results;
your application must do the same.

For exact declarations, use `go doc` in your application's module directory or
the package links to pkg.go.dev. Local `go doc` reflects the dependencies selected
by your module graph and active workspace, so check that selection when a version
matters. The guides teach usage rather than maintaining a second API inventory.

These same guides ship as an agent skill. [Working with Agents](../agent-skills/working-with-agents.md)
explains how to give a coding agent access to them.
