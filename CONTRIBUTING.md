# Contributing to Arkoren

Thanks for helping improve Arkoren. Contributions can be bug reports, driver
requests, documentation fixes, or code changes. This guide explains where each
kind of contribution goes and how to check a change before you open a pull
request.

By taking part, you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- **Questions and ideas:** use
  [Discussions](https://github.com/arkoren/arkoren/discussions). Q&A is for
  usage help; Ideas is for proposals that are not ready to be an issue yet.
- **Bugs, features, and driver requests:** open an
  [issue](https://github.com/arkoren/arkoren/issues/new/choose) with the
  matching form.
- **Security vulnerabilities:** never open a public issue. Follow the
  [security policy](SECURITY.md) and report privately.

Open an issue before starting a large change, a new driver, or a change to a
service contract. A short agreement on the approach avoids rework. Small fixes,
such as typos, broken links, or a bug with an obvious fix, can go straight to a
pull request.

Issues labeled
[`good first issue`](https://github.com/arkoren/arkoren/labels/good%20first%20issue)
are small and well scoped. Issues labeled
[`help wanted`](https://github.com/arkoren/arkoren/labels/help%20wanted) are
open for anyone to pick up. Comment on the issue first so that two people do
not work on the same thing.

## Repository layout

Arkoren is a set of independent Go modules with no root module:

| Path | Contents |
| --- | --- |
| `core/` | `arkoren.dev/core`: application lifecycle, HTTP handling, service contracts, memory drivers, and `<contract>test` conformance suites. Core has no third-party dependencies. |
| `driver/<vendor>/` | One module per vendor, such as `driver/redis` or `driver/stripe`. |
| `foundation/<name>/` | Standard-library-only libraries, such as `validate` and `problem`. |
| `cmd/arkoren/` | The consumer CLI and the consumer guides it embeds. |
| `cmd/arkoren-ci/` | Repository automation for CI and releases. It is not a consumer dependency. |
| `website/` | The [arkoren.dev](https://arkoren.dev) site. |

Consumer documentation is written once, in
`cmd/arkoren/skills/arkoren/references/guides/`. The CLI embeds that tree and
the website renders it. API documentation lives in Go doc comments and is
published on [pkg.go.dev](https://pkg.go.dev/arkoren.dev/core).

## Set up

You need:

- Go 1.27 or later.
- A C compiler, for race tests run by `make check`.
- Docker with Compose, only for driver integration tests.
- Node.js 26.13 or later, only for website and guide changes. See the
  [website README](website/README.md).

The committed `go.work` file joins the modules for local development. Shared
`make` recipes still set `GOWORK=off`, so each module is checked against its
own `go.mod`, as consumers use it.

Run `make` with no arguments to list every target.

## Make a change

1. Fork the repository and create a branch from `main`.
2. Make the change, including a test that fails without it.
3. Update the documentation that describes the changed behavior: Go doc
   comments, and the relevant consumer guide when behavior visible to users
   changes.
4. Run the checks for every module you changed (see [Check your
   change](#check-your-change)).
5. Open a pull request against `main` and fill in the template.

Some changes must also update other parts of the repository:

- **Service contract changes** must update the contract's `<contract>test`
  conformance suite, the memory driver, and every vendor driver that implements
  the contract.
- **Driver behavior changes** must update the compatibility matrix on that
  service's drivers guide.
- **Drivers never run DDL.** Packages that need tables ship `Migrations()`, and
  the application decides when to run them.
- **Dependencies:** core never imports a driver, and driver modules never
  require other driver modules. Foundation modules use only the standard
  library.

## Check your change

Select a module with `MODULE`, using its path from the repository root:

```sh
make test MODULE=core PKG=./http/...    # short tests
make check MODULE=core                  # lint and race tests
make integration MODULE=driver/redis    # driver tests against real services
```

`make integration` starts the module's Compose services, runs the full race
suite, and removes the containers and volumes when it exits.

After changing a consumer guide, refresh the guide index and build the site:

```sh
make generate
make docs
```

Pull requests run the same checks in CI for the modules they change. A pull
request can merge only when the `CI gate` and `website` checks pass.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/) with a scope
that names the module or area:

```text
fix(postmark): key webhook credential comparison tags
fix(core): make retry delay bounds explicit
docs(guides): rewrite consumer guides and add page summaries
```

Release tooling reads these messages. Within a module, `fix` proposes a patch
release, and `feat` or a breaking change proposes a minor release while Arkoren
is below v1.0. Mark a breaking change with a `BREAKING CHANGE:` footer that
explains what users must change.

Maintainers decide when to publish releases. A merged pull request does not
publish a release by itself.

## AI-assisted contributions

The repository includes contributor skills for coding agents in
`.agents/skills/`, linked from `.claude/skills/`, and agent instructions in
`AGENTS.md`. You may use them; you remain responsible for checking every line
you submit and for running the checks above.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
