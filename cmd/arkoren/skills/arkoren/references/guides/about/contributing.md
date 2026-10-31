---
title: Contributing to Arkoren
nav: Contributing
description: How the repository is laid out, how to build and test each module, and the rules a change must follow to be accepted.
order: 20
---

# Contributing to Arkoren

## Introduction

Before changing Arkoren, identify the module that owns your change and test it
independently. The single repository, `github.com/arkoren/arkoren`, holds many Go
modules but has no root module. A contract change also affects its drivers and
conformance suite, so it cannot be tested as a local interface edit alone.

Use this contributor guide to locate the change, run the same checks as
continuous integration, and keep modules independent and drivers interchangeable.
Read it before opening a pull request, especially when changing a contract or
adding a driver.

Start from a repository checkout with Go 1.27 or later and `golangci-lint`
installed. Run commands from the repository root unless a step changes the
directory. Infrastructure-backed driver tests also need Docker Compose. Website
checks need the Node.js version and dependencies described in `website/README.md`.

## Repository layout

There is no root Go module. `arkoren.dev` is a namespace. Product code lives in
the following module families; tooling and documentation share the repository:

- `core/` is `arkoren.dev/core`. It contains the application lifecycle
  (`application`), the in-process event bus (`event`), everything on the HTTP request
  path under `http/`, and every service contract under `service/`, with
  conformance suites and in-process implementations where applicable.
- `foundation/<name>/` is one module per folder, such as
  `arkoren.dev/foundation/query`. Foundation modules import only the standard
  library.
- `driver/<vendor>/` is one module per vendor, such as
  `arkoren.dev/driver/redis`. Each contains one package per contract it
  implements, named `<vendor><contract>`, and may also contain provider-specific
  HTTP adapters without a shared service contract.
- `ark/<name>/` is reserved for [arks](../core-concepts/arks.md): opt-in modules that add a whole feature
  and assume an application's domain, such as users or teams. There are none yet.
- `cmd/arkoren/` is the `arkoren.dev/cmd/arkoren` CLI module. It uses core and
  foundation packages plus the pinned MCP SDK as a protocol client. Installing it
  as a Go tool adds those dependencies to the application's module graph, but
  does not pull in vendor driver modules. Core itself remains SDK-free.
- `cmd/arkoren/skills/arkoren/` holds the single consumer skill and these
  guides. The CLI embeds that directory directly; the website in `website/`
  renders the same guides. API declarations stay in Go source documentation.

A `go.work` file joins every module for local development, so an edit in
`core` is visible in a driver module straight away. Until versions are
published, relative `replace` directives in each `go.mod` link the modules to
each other as well.

> [!WARNING]
> Do not run `go work sync` or `go mod tidy` across the whole repository. Both
> rewrite every module's `go.mod` and can bump dependencies you did not mean to
> change. Run `go mod tidy` only inside the one module you changed, and review
> the diff.

## Where a change belongs

Before you add code, decide which module should own it. Ask these questions in
order and stop at the first answer that fits:

1. **Does it need a vendor?** Use a contract in `core/service/` with drivers under
   `driver/` for interchangeable service behavior. Keep provider-specific HTTP
   adapters in the vendor module when the provider protocol is the interface,
   as with CAPTCHA or webhooks; a vendor alone does not require a new contract.
2. **Is it a pure algorithm that needs only the standard library and is useful
   without Arkoren?** Then it belongs in `foundation/`.
3. **Does it assume the application's domain or product flow**, such as users,
   plans, or a signup flow with its own routes and emails? Then it is an ark.
4. **Does it need a third-party library?** Then it stays out of core, as a
   driver or an ark.
5. **Otherwise** it belongs in core: a mechanism any application can use.

A few rules follow from this, and a change that breaks one is not accepted. Core
never imports a driver or an ark, and has no third-party dependencies. A
driver module never requires another driver module. An ark never requires another
ark. Separate product modules live only under `cmd/`, `driver/`, `ark/`, and
`foundation/`, never below a `core/` path.

## Building and testing a module

Every module is checked independently. The root Makefile disables the workspace
with `GOWORK=off`, so checks use dependencies declared by that module:

```sh
make test MODULE=core
make check MODULE=core
```

`test` runs short tests with normal built-in vet; `check` runs lint and short
race tests with test-time vet disabled to avoid duplicate work. Both use normal
Go result caching.
Use `PKG=./http/...` and `RUN=TestName` to narrow the selected tests. The shared
lint configuration includes vet; `make vet MODULE=core` is the standalone
toolchain backstop.

Routine CI runs on every pull request, main-branch push and manual dispatch.
It checks every canonical module on a separate hosted runner, with at most six
check jobs and three integration jobs running concurrently. Those limits are
independent. Compose modules run lint in the check matrix and full race suites
in integration. The repository module runs lint there and fresh nonrace tests
in an independent job. Rendered documentation checks also run independently on
every routine event. The stable `CI gate` succeeds only if all routine jobs succeed.

The separate audit workflow runs on Mondays and Thursdays at 03:17 UTC and on
manual dispatch. It runs standalone vet and `govulncheck` for every module,
repository race tests, and the CLI generated-project end-to-end test. Scheduled
audits use the default branch; manual audits use the selected ref. Vulnerability
scanning is audit-only: dependency pull requests no longer receive a dedicated
vulnerability merge gate. Run `govulncheck ./...` inside an affected module when
you need that evidence before merging.

### Integration tests

Compose-backed driver suites are skipped under `go test -short`. Their
`compose.yaml` files supply local services on fixed ports bound to `127.0.0.1`;
live-provider suites can require separate credentials and are not enabled by
these workflows. The integration target starts services, runs fresh race
tests without `-short`, and cleans up even when tests fail or a handled signal
interrupts them:

```sh
make integration MODULE=driver/postgres
```

For manual investigation, use `make services MODULE=driver/postgres`, then
`make cleanup MODULE=driver/postgres` when finished. Cleanup removes container
volumes; keep test data disposable.

Cleanup validates only the module path and files, so it works without Go or a
resolvable package graph. A cleanup failure fails an otherwise successful
integration run; if tests already failed, their exit status is preserved and the
cleanup failure is reported separately.

Integration creates a unique Compose project by default; fixed host ports still
need to be free. An explicit `COMPOSE_PROJECT_NAME` is honored and that project's
containers and volumes are removed, so choose a disposable project. Manual
services and cleanup instead share `arkoren-manual-<module-with-hyphens>`; pass
the same explicit project name to both if overriding that default.

## Changing a contract

A **contract** is the `Driver` interface of a service, such as
`cache.Driver`. Because every driver implements it, a change to a contract is
never local. When you change one, update all of these in the same change:

- the contract's conformance suite in `<contract>test`, so the new behavior is
  tested;
- the memory driver, if the contract has one;
- every driver that implements the contract;
- the contract's source documentation and relevant guides.

The contract's interface documentation defines the behavior, and the
conformance suite checks that drivers follow it. If a documented rule is not
tested, add a test rather than treating that rule as optional.
[Writing a driver](../drivers/writing-a-driver.md) explains the suites.

## Documentation

Documentation is written once, in
`cmd/arkoren/skills/arkoren/references/guides/`; the website and installed
consumer skill use the same files. Source doc comments are the API reference:
use `go doc` for the version selected by a consuming application, or pkg.go.dev
for online lookup. The website API catalog lists modules rather than maintaining
package-by-package declarations.

When behavior changes, update its source documentation and the guide that
teaches the affected task. Preserve ownership, lifecycle, failure, and driver
limitations in the guide; do not copy exhaustive API inventories into it.

Refresh the marked guide index and check the shared documentation from the
repository root. The CLI embeds the skill directory directly; no archive
generation step is required:

```sh
make generate
make docs
```

Install website dependencies first as described in `website/README.md`.

The CI `docs` job performs the same checks on every routine run. The build checks
guide-index freshness without writing and validates metadata; rendered tests
check links and navigation. Review offline relative source links, heading and
fence structure, alert syntax, and other editorial conventions manually. There
is no separate repository-wide source-policy suite.

For a new or materially revised guide, follow the mandatory guide review in
the contributor `developer-documentation` skill under `.claude/skills/`. Identify
the page type, check the whole page in reader order, and report verification
results and justified non-applicable rules in the change summary. Automated
checks do not verify behavior claims or replace editorial review.

## License

Arkoren is released under the MIT license. By contributing, you agree that
your contribution is licensed under the same terms. See the `LICENSE` file at
the repository root.
