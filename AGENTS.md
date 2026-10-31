# arkoren

Modular Go 1.27 toolkit (`github.com/arkoren/arkoren`) with independent modules
and no root module. `core/` is `arkoren.dev/core`: application lifecycle,
`http/` request handling, and `service/` contracts, memory drivers and
`<contract>test` conformance suites. `driver/<vendor>` holds one module per
vendor; `ark/<name>` holds opt-in domain features (none yet);
`foundation/<name>` holds stdlib-only libraries. `cmd/arkoren` is the consumer
CLI; `scripts/` holds workflow fixtures. `go.work` joins modules for development;
relative `replace` directives link them until releases exist.

## Find code before reading it

Use glob and grep to locate the owning package and its callers; read the relevant
declarations and tests. Use `go doc arkoren.dev/core/<pkg>.<Symbol>` (or the
driver/foundation module path) for declarations and contracts. Consumer guides
live in `cmd/arkoren/skills/arkoren/references/guides/`; API declarations live in
Go doc comments and on pkg.go.dev, not duplicated local API pages.

Never read `go.sum`, `go.work.sum`, `website/package-lock.json`, or
`foundation/validate/wrappers.go`.

## Skills

- `arkoren-go`: load before writing Go; API, ownership, lifecycle and test rules.
- `arkoren-codebase`: load before crossing packages or adding a package/driver;
  boundaries, domain terms and coordinated changes.
- `developer-documentation`: load before changing READMEs, website prose, or
  consumer guides. Preserve source-backed safety, fidelity and driver matrices.

## Invariants

- A contract change updates its `<contract>test` suite, memory driver and every
  vendor implementation. Find implementations and all callers with grep and Go
  tooling; there is no generated contract map.
- Core has no third-party dependencies and never imports a driver, ark or CLI.
  Driver modules never require other driver modules; arks never require other
  arks. Foundation modules import only the standard library. `cmd/arkoren` may
  require core and its pinned MCP SDK; keep dependencies small because a
  `go tool` dependency enters the consumer's `go.mod`.
- Separate modules live under `cmd/`, `driver/`, `ark/`, and `foundation/`, never
  below `core/`. Drivers never run DDL: packages needing tables ship
  `Migrations()` for application-owned execution (see `arkoren-codebase`).
- Public API changes update Go doc comments and relevant guides/examples.
  Construction builders retain matching public `BuilderFunc` aliases. Preserve
  stable problem identities and shipped migration groups. Review builder aliases,
  problem conventions and module boundaries; there is no repository-wide AST gate.
- Consumer documentation has one authored tree, `cmd/arkoren/skills/arkoren/`,
  directly embedded by the CLI and read by the website. Update relevant consumer
  rules and contributor skills (`.agents/skills/`, linked from `.claude/skills/`)
  with behavior changes. `make generate` updates
  only the consumer guide index; no API, map or archive generation is required.
- Every service drivers page requires a compatibility matrix. After driver-page
  edits or driver/contract behavior changes, recheck each affected provider's
  coverage, evidence, portable statuses and shared presentation using
  `developer-documentation`.

## Commands

Run `make` from the root for help. Select a canonical module root explicitly:

```sh
make test MODULE=core PKG=./http/... RUN=TestRequest
make check MODULE=core
make integration MODULE=driver/redis
make generate
make docs
make workflow-check
```

`test` runs short tests with normal built-in vet defaults. `check` runs
golangci-lint (including configured govet) and short race tests with test-time
vet disabled to avoid duplicate work. Ordinary module test and race results
retain Go caching: neither target passes `-count=1`.
Race targets set `CGO_ENABLED=1` only on the test
command and require a C compiler; do not change global Go environment settings.
All shared Go recipes use `GOWORK=off` to verify module requirements.

Configured govet is the routine paired-check authority. Standalone toolchain
`make vet MODULE=...` runs in scheduled/manual audits to bypass golangci-lint's generated
diagnostic, exclusion and version filtering. It does not restore fieldalignment
or shadow: those analyzers are not enabled by default toolchain vet either.

`integration` starts healthy Compose services, runs uncached full race tests,
prints failure logs and removes containers and volumes on exit, including handled
interrupt/termination signals. Without `COMPOSE_PROJECT_NAME`, it creates a unique
`arkoren-integration-<random>` project so teardown cannot remove a pre-existing
default vendor project. An explicit project name is honored, including in CI;
integration removes that project's containers and volumes, so use only a disposable
project. Fixed host ports still require other services to release those ports.

Manual `services` and `cleanup` share the intentional persistent default project
`arkoren-manual-<module-with-slashes-replaced-by-hyphens>` (for example,
`arkoren-manual-driver-redis`). Both honor `COMPOSE_PROJECT_NAME`; pass the same
name to both commands. Manual cleanup does not target unique integration projects.

After guide changes run `generate` and `docs`. Generation requires installed
website dependencies; docs also requires Playwright Chromium. The website build
checks guide-index freshness without repairing it and validates metadata.
Rendered tests check links and navigation. Offline source-link containment and
custom Markdown/editorial policy checks are retired; review source conventions
and relative links explicitly. A rendered build is not proof of API correctness
or editorial quality.

During incremental source imports, `.github/workflows/imports.yml` uses
`scripts/imported-modules.sh` to check only committed canonical modules, including
an empty checkout inventory. Keep `ci-modules.sh` strict: it is the complete-source
inventory. Replace the temporary import workflow with full CI/audit workflows
only after the full inventory is published; scheduled audits remain held back.
The separate website workflow and its required `website` job remain active.

After workflow edits run `workflow-check` (discovery/Make fixtures and actionlint), the affected shared
recipes, and inspect real GitHub CI results. There is no local workflow emulator.
Routine CI checks every canonical module on isolated runners (six check jobs and
three integration jobs at a time, independently), plus workflow tooling checks
and rendered docs. `scripts/ci-modules.sh` owns the bounded inventory; module
changes must preserve its canonical paths and pass its fixtures. Compose modules
run lint in check and full race suites in integration.
`CI gate` requires every routine job.
The separate Monday/Thursday 03:17 UTC audit runs all-module toolchain vet and
govulncheck and the CLI generated-project end-to-end test.
Manual audit dispatch checks the selected ref; scheduled audits use the default
branch. Vulnerability scans are audit-only, not a dependency-PR merge gate.

Answer plans, proposals, comparisons and findings in the conversation. Write
them to a file only when requested, under `.claude/tmp/` (temporary, gitignored,
not a source of truth). Durable instructions belong in a skill. Preserve existing
temporary user work.

Never run `go work sync` or a blanket `go mod tidy`: they rewrite unrelated
modules and bump dependencies.
