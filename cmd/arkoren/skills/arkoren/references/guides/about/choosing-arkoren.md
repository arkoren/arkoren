---
title: Should I Choose Arkoren?
nav: Choosing Arkoren
description: Decide whether Arkoren fits your Go service, how to adopt it alongside existing packages, and which limits to evaluate first.
order: 15
---

# Should I Choose Arkoren?

## Introduction

If you are choosing libraries for a Go service, the decision is not whether to
replace everything you already use. Arkoren is a toolkit of independently usable
packages. Choose it where shared service interfaces, testing tools, and explicit
application wiring save you work; keep the standard library or specialist
packages where they already meet your needs.

## When Arkoren fits

Arkoren is useful when several backend capabilities need consistent setup,
errors, telemetry, and tests. A **facade** is the application-facing service API;
a **driver** implements it for a backend. This separation lets application code
use the same cache API with an in-process driver in tests and Redis in a deployed
service. You still configure and operate the chosen backend. See
[facades and drivers](../core-concepts/facades-and-drivers.md).

| Your situation | Decision |
| --- | --- |
| You repeatedly assemble service interfaces, lifecycle hooks, and test substitutes across projects. | Evaluate Arkoren's shared conventions against that integration work. |
| A small `net/http` service already meets your needs. | Keep it; adding a toolkit is not a requirement for growth. |
| You need a specialist package's full API more than a portable service interface. | Use that package directly, or isolate vendor-specific calls alongside Arkoren. |
| You want automatic dependency discovery or a complete application with built-in business rules. | Arkoren is not that model: you construct dependencies and own application policy. |

Core uses standard Go types and has no third-party Go dependencies; vendor
libraries live in separately installed driver modules. This does not make a
driver-backed application dependency-free. Compared with assembling packages
yourself, you gain common contracts and test tools but take on Arkoren's API
conventions and the limits of each contract.

## Keep your existing router

Start with one capability, such as a cache or validation, without changing your
HTTP stack. Services do not require the Arkoren router or application lifecycle.
You can construct them in your existing composition code and pass them to your
handlers.

If you also want Arkoren HTTP handlers, both its router and its error-returning
handler type implement `http.Handler`. An existing router that accepts that
interface can serve them. In the other direction, `router.HTTP` adapts standard
handlers into Arkoren routes, and `Mount` serves a handler tree beneath a prefix.
Check path-prefix handling and middleware order at that boundary; `Mount` strips
the prefix. See [mounting standard handlers](../http-fundamentals/routing.md#mounting-standard-handlers).

## Check maturity, compatibility, and limits

The current Go baseline is **Go 1.27 or later**. Modules are versioned
independently, so evaluate the core and drivers you actually use rather than
assuming one version covers the whole toolkit. Pin dependencies in your
application and run its tests when upgrading, including tests against your real
backend.

Arkoren includes [driver conformance suites](../testing/conformance-suites.md),
in-process test implementations, and vendor integration tests. These provide
evidence about specified behavior, not a production track record, performance
guarantee, API freeze, or long-term support window. If your adoption depends on
those assurances, establish them separately before committing.

Evaluate these limits before choosing a capability:

- A portable contract does not expose every vendor feature. Some operations can
  return `ErrUnsupported`; check the selected [driver's behavior and limits](../drivers/choosing-a-driver.md).
- In-memory state is process-local and is lost on restart. Passing memory-driver
  tests does not establish distributed behavior or durability for your deployment.
- You own dependency wiring, lifecycle registration, credentials, migrations,
  and authorization policy; installing a package does not configure them.
- There are no built-in [arks](../core-concepts/arks.md), the optional domain
  features that combine services with business rules. Do not choose Arkoren on
  the assumption that signup or other complete product flows are included.

If the trade-offs fit, follow [Installation](../first-steps/installation.md) and
try one capability against an existing application test before expanding adoption.
