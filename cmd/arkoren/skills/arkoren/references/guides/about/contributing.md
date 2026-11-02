---
title: Contributing
description: Locate a toolkit change, preserve module and contract boundaries, and run the relevant contributor checks.
order: 20
summary:
  - Locate the owning module and coordinate service-contract changes across implementations and tests.
  - Run focused checks and disposable integration suites without confusing workspace builds with module independence.
  - Update shared guides and report verification evidence alongside the required CI checks.
---

# Contributing

This page is for changing Arkoren itself, not for adding it to an application.
Start with a checkout of `github.com/arkoren/arkoren` and read its `AGENTS.md`.
Identify the module that owns your change before editing: a service contract
can affect several drivers even when the interface change is small.

Run the commands below from the repository root. Go work needs Go 1.27 or later;
linting also needs `golangci-lint`, and race tests need a C compiler. Additional
requirements for integration and documentation checks appear beside those tasks.

## Locate the Change

The repository has no root Go module. Its product modules have separate
manifests and dependency versions:

| Location | Responsibility |
| --- | --- |
| `core/` | Application lifecycle, HTTP packages, service contracts and facades, bundled local implementations, and conformance suites. |
| `foundation/<name>/` | Independent libraries that import only the standard library. |
| `driver/<vendor>/` | Vendor implementations and provider-specific adapters, one module per vendor. |
| `ark/<name>/` | Reserved for opt-in domain features; none are built in yet. |
| `cmd/arkoren/` | Consumer CLI, project templates, and embedded documentation skill. |

Keep vendor dependencies outside core. Core must not import a driver, ark, or
CLI; driver modules must not require other driver modules, and arks must not
require other arks. A provider-specific HTTP adapter can belong in its vendor
module without introducing a portable service contract. Read the contributor
`arkoren-codebase` skill before a change that crosses package boundaries or adds
a package or driver, and `arkoren-go` before writing Go.

The committed `go.work` supports development across the modules it lists.
Module manifests declare real dependency versions without local `replace`
directives. Workspace builds can use edited local dependencies, but the shared
Make recipes set `GOWORK=off` to verify each module independently.

Do not run `go work sync` or a repository-wide `go mod tidy`: they can rewrite
unrelated modules and change dependency versions. If dependency maintenance
requires `go mod tidy`, restrict it to the affected module and review the diff.

## Coordinate Contract Changes

A service's `Driver` interface specifies backend behavior. If you change that
contract, find every implementation and caller rather than relying on the
interface package's tests alone. Update these together:

- The `<contract>test` conformance suite and the bundled memory implementation,
  when one exists.
- Every vendor implementation of the contract and its affected tests.
- Public Go doc comments, relevant consumer guides, and contributor instructions
  affected by the behavior change.

Use the conformance suite to test the documented behavior. Add coverage for a
new requirement rather than leaving it as an untested promise. The
[driver-writing guide](../drivers/writing-a-driver.md) explains how implementations
use these suites.

Preserve application ownership when changing persistence: drivers do not run
DDL themselves. A driver that needs tables supplies `Migrations()` for the
application to execute, and existing migration groups must remain stable.

## Run Module Checks

While developing, select the owning module and narrow tests to the affected
package. For an HTTP change in core, for example:

```sh
make test MODULE=core PKG=./http/... RUN=TestRequest
```

`MODULE` names the module directory; `PKG` is relative to that module, and `RUN`
is a Go test regular expression. This runs short tests with normal built-in vet
and normal test-result caching. Choose a pattern that actually matches your tests.

Before submitting a Go change, check each affected module. For core:

```sh
make check MODULE=core
```

`check` runs lint, including configured govet, and short race tests. Race tests
disable test-time vet to avoid repeating it; ordinary test caching remains
enabled. `make vet MODULE=core` runs standalone toolchain vet when you need that
additional audit, rather than replacing the routine lint check.

## Run Integration Tests

For a module with `compose.yaml`, install Docker Compose and make sure its fixed
host ports are available. Integration teardown removes containers and volumes.
By default, the target creates a unique disposable Compose project. If you set
`COMPOSE_PROJECT_NAME`, use a disposable project: the target removes that named
project's containers and volumes too.

To check the PostgreSQL driver against its local service:

```sh
make integration MODULE=driver/postgres
```

The target waits for healthy services, runs uncached full race tests without
`-short`, and attempts cleanup on success, failure, or handled interruption.
Failure diagnostics include service logs. A cleanup failure fails an otherwise
successful run; if tests already failed, their failure status is preserved.
Live-provider tests can need credentials beyond this local Compose setup.

For manual investigation, start services with disposable test data:

```sh
make services MODULE=driver/postgres
```

Unlike integration runs, this command leaves services running in the persistent
default project `arkoren-manual-driver-postgres`. When your investigation is
finished, remove the services and their volumes:

```sh
make cleanup MODULE=driver/postgres
```

Cleanup deletes the project's test data. If you override `COMPOSE_PROJECT_NAME`,
pass the same value to both commands.

## Update Documentation

Author consumer guides in `cmd/arkoren/skills/arkoren/references/guides/`. The CLI
embeds the skill tree directly and the website renders the same Markdown. Keep
API declarations in Go doc comments rather than copying signature inventories
into guides.

Before changing a guide, read the contributor `developer-documentation` skill
under `.agents/skills/`. For a material rewrite, establish the reader's task and
page scope, check claims and examples against source and tests, and review the
finished page separately for comprehension and technical fidelity. Driver-page
changes also require rechecking their compatibility matrices against every
affected implementation.

Install the website's Node.js dependencies and Playwright Chromium using
`website/README.md`, then run:

```sh
make generate
make docs
```

Generation updates the consumer guide index; there is no documentation archive
to generate. The docs target runs website checks, a build, and tests. The build
checks index freshness without repairing it and validates metadata; rendered
tests check links and navigation. Review relative Markdown links and editorial
conventions yourself. Passing rendered tests does not verify API claims or
example correctness.

## Prepare the Review

Describe the behavior changed, the modules and guides affected, and the evidence
you collected. Distinguish tests actually run from source inspection, and report
checks you could not run. For documentation examples, distinguish compiled
programs from API-checked fragments.

Routine CI discovers canonical modules with `scripts/ci-modules.sh` and checks
them independently. Compose modules run lint in the check jobs and full race
suites in integration. Require both the `CI gate` for Go and tooling and the
separate `website` check; the gate alone does not cover rendered documentation.
The scheduled and manually dispatched audit separately runs standalone vet,
vulnerability scans, and the CLI generated-project end-to-end test.

If you edit workflows, run `make workflow-check` with its required tools,
including `actionlint`, rerun the affected shared recipes, and inspect the actual
GitHub CI results. Local fixtures do not emulate GitHub Actions.

## License

Arkoren uses the MIT license in the repository's `LICENSE` file. Contributions
are made under those same terms.
