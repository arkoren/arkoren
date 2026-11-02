---
title: Choosing Arkoren
description: Evaluate Arkoren's shared conventions, integration boundaries, and backend limits before adopting a capability.
order: 15
summary:
  - Weigh shared service APIs and explicit wiring against your application's existing integration work.
  - Distinguish portable contracts from backend guarantees and application-owned policy.
  - Plan incremental adoption and test compatibility with your HTTP stack and production backend.
---

# Choosing Arkoren

Choose Arkoren for a capability whose shared API and integration conventions help
your application. You do not need to replace a working HTTP stack or standardize
every dependency at once. The useful comparison is between the integration work
you would otherwise maintain and the conventions and limits you accept by using
Arkoren.

## Evaluate the Fit

If several services need common application-facing APIs and test substitutes,
Arkoren's [facades and drivers](../core-concepts/facades-and-drivers.md) provide
that separation. Your application calls a facade while a driver supplies backend
behavior. For a cache, this lets a unit test use process-local storage while a
deployed service uses a shared backend without changing the facade calls.

That boundary is useful only if the contract covers your task. If most of your
code needs a vendor's specialized operations, a portable facade might add little
value. Use the vendor package directly where appropriate; do not assume every
vendor feature has an Arkoren equivalent.

The same reasoning applies to application structure. Explicit constructors and
optional lifecycle hooks fit applications where you want dependency wiring in
Go. If a small `net/http` service already meets your needs, keeping it is a valid
choice. Arkoren does not provide automatic dependency discovery or a container
that wires the application for you.

## Understand the Boundaries

Shared APIs do not remove backend differences. Review the relevant
[driver guide](../drivers/choosing-a-driver.md) before choosing an implementation,
especially if your task depends on persistence, coordination between servers,
or an optional operation. Some contracts permit unsupported operations to return
`ErrUnsupported`; the presence of a facade method alone is not proof of support
by every driver.

Memory drivers serve local and test scenarios, but their state is process-local
and does not survive a restart. A passing [conformance suite](../testing/conformance-suites.md)
is evidence about the behavior that suite tests, not about distributed operation,
production capacity, or identical durability across backends. Vendor integration
tests add backend-specific evidence; your deployment still needs its own tests.

You also retain responsibility for credentials, resource startup and cleanup,
schema migrations, and authorization policy. Installing a module does not
configure those responsibilities. There are no built-in [arks](../core-concepts/arks.md),
the optional domain features intended to combine services with product-specific
rules; do not assume signup or another complete business flow is included.

Core's Go dependencies are Arkoren's standard-library-only foundation modules,
not third-party libraries. Vendor modules do bring external dependencies.
Evaluate the module graph you actually install rather than treating the whole
application as dependency-free.

## Keep Existing Components

For a first adoption, choose a service or foundation library that does not affect
HTTP routing. Construct it where your application already constructs dependencies
and pass it to existing handlers. Neither the Arkoren router nor its application
lifecycle is required.

If you later adopt Arkoren HTTP handlers, its router and error-returning handler
type both implement `http.Handler`. An existing router can serve them wherever it
accepts that interface. In the other direction, `router.HTTP` adapts a standard
handler, and `Mount` serves a handler tree beneath a prefix. `Mount` strips that
prefix before forwarding the request, so test path handling and middleware order
at the boundary. [Routing](../http-fundamentals/routing.md#mount-native-handlers)
shows the integration.

## Try One Capability

Arkoren requires Go 1.27 or later and is pre-1.0. Modules version independently,
and v0 minor releases can break APIs. Read the
[compatibility and support policy](../first-steps/installation.md#compatibility-and-support)
before adopting a module: security fixes target its latest published version,
with no guaranteed backports or fixed long-term support window. Conformance and
integration tests do not establish an API freeze, performance guarantee, or
production track record. If you require those assurances, establish them
separately before committing.

Start with the [existing-application installation example](../first-steps/installation.md#existing-applications)
and add a test for the behavior you need. Before expanding adoption, test the
chosen production driver against its backend, including the failure and
unsupported-operation cases relevant to your application. Keep the dependency
versions recorded in your application's module files and rerun those tests when
upgrading.
