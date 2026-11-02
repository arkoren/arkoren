---
title: Using Skills
description: Give a local coding agent Arkoren guidance, keep its documentation snapshot aligned with your application, or read selected guides online.
order: 10
summary:
  - Install a project-local documentation snapshot for Codex or Claude Code without generating an application.
  - Load task-specific guidance and check exact APIs against your application's selected Go dependencies.
  - Preview updates, preserve local work, and remove a managed skill without deleting other skills.
  - Read selected Markdown guides online while keeping context use and action permissions separate.
---

# Using Skills

When a coding agent works on your application, give it Arkoren's integration
guides rather than relying on its training data. The `arkoren` **skill** is a
folder containing an entry file, `SKILL.md`, and reference pages that the agent
reads as needed. It includes the same authored guides as the website, packaged
as a fixed snapshot in the Arkoren CLI.

You can install the skill in an existing application without generating a new
project or changing its Go dependencies. For adopting Arkoren packages while
keeping your current layout, follow
[Existing Applications](../first-steps/installation.md#existing-applications).
If your agent reads from the web instead, use the [online indexes](#reading-online).

The skill supplies documentation, not backend tooling. Installing or loading it
does not connect to your service, supply credentials, or authorize file edits,
dependency changes, migrations, or remote operations. Keep those actions within
your agent's configured permissions and your explicit task. Runtime access has
separate setup through the [CLI connection](../cli/connecting.md) or an
[MCP client](../mcp/first-server.md).

## Installing Skills

Install the [Arkoren CLI](../cli/overview.md#installing-arkoren), then choose the
project-local directory your agent reads. Run the following commands from your
application's root, not from a parent workspace or your home directory.

For [Codex](https://developers.openai.com/codex/skills/), the CLI's default
`.agents/skills` matches its repository skill directory. Preview the destination
and planned changes before writing:

```sh
arkoren skills install --dry-run
```

The JSON report names the destination ending in `.agents/skills/arkoren`, with
planned write, removal, and conflict counts. The preview creates no files or
directories. Add `--verbose` when you need individual paths. If the destination
is the one you intended and there are no conflicts, install the bundle:

```sh
arkoren skills install
```

The installation contains `SKILL.md`, the reference pages, and
`.arkoren-skill.json`, which records managed file hashes. A successful report
includes `completed: true`. This operation works offline: it does not download
documentation, read endpoint configuration, install Go modules, or edit
`AGENTS.md`.

For [Claude Code](https://code.claude.com/docs/en/skills), use its project skill
directory instead. Preview and then apply this alternative installation:

```sh
arkoren skills install --dir .claude/skills --dry-run
```

After reviewing the preview, run the same command without `--dry-run`. The skill
lands at `.claude/skills/arkoren/SKILL.md`. Choose one location for the client you
use; these commands are alternatives, not required duplicate installations.

`--dir` always names the **parent** skills directory; Arkoren appends `arkoren`.
Relative paths start at the current working directory. The CLI does not search
parent directories for a project or an installed skill. Use the same `--dir`
with later status and update commands. For other clients, check their documented
local skill discovery rules rather than assuming either location is recognized.

Commit the chosen skill directory, including its manifest, if your team should
share the snapshot. Keep application-specific instructions in your project's
own instruction files, outside the managed skill. If you already used
[`arkoren new`](../first-steps/creating-a-project.md), check its generated skill
before installing another copy; new projects include agent files by default.

## Loading Guidance

Installation and discovery are separate. Codex can select a skill when a task
matches its description; use `/skills` or mention `$arkoren` to select it
explicitly. In Claude Code, invoke `/arkoren`, or ask for a task that matches the
skill's description. Client policy can restrict skill loading. Confirm your
agent can read the installed `SKILL.md` and one linked guide before relying on
automatic selection; if it cannot find the skill, check its location and restart
the client if needed.

For example, if you want advice on adding a cache to an existing service, give
the agent a bounded reading task before asking for changes:

```text
Load the arkoren skill and read the cache overview and driver guide.
Our service already has its own layout. Compare a process-local cache with
Redis for this service. Explain the trade-offs without editing files.
```

This asks the agent to use relevant guidance without turning the request into
permission to install Redis, change dependencies, or rewrite your application.
Name the provider when you have already chosen one so the agent reads its
specific setup and limitations.

The entry file contains authored usage rules and a generated guide index. The
index points to guides; it is not an API inventory or the source of the rules.
Ask the agent to read the entry file and only the reference pages needed for
the task, rather than loading the whole guide tree into its context.

For an exact API, run `go doc` from your application's module directory. For
example, if that module already depends on core:

```sh
go doc arkoren.dev/core/service/cache.New
```

Go selects the declaration from your application's module graph and active
workspace, not from the installed skill's revision. Check `go.mod` and the
workspace when the selected source is uncertain. This lookup can work offline
once the dependency source is available locally; installing the skill does not
install that source. For online lookup, select the matching package and version
on [pkg.go.dev](https://pkg.go.dev/arkoren.dev/core/service/cache).

## Updating Skills

Check the installed snapshot before changing it:

```sh
arkoren skills status --verbose
```

The report shows bundled and installed revisions and lists modified managed
files, including files that are missing or unreadable. A missing installation
has state `not installed`; a directory without an ownership manifest can be
reported as `unmanaged`. Status does not write files or fail merely because
managed files changed.

A revision is a SHA-256 content identifier, not a Go module version or a
compatibility guarantee. Before updating, compare the documentation's
assumptions with the dependencies selected by your application. Pin the CLI
version if your team needs a reproducible tool and documentation bundle.

To obtain a newer snapshot, upgrade the CLI using your chosen
[installation method](../cli/overview.md#installing-arkoren). Then preview the
bundle it now carries:

```sh
arkoren skills update --dry-run
```

Review the planned writes, removals, and conflicts. When the plan is acceptable,
apply it:

```sh
arkoren skills update
```

Neither command downloads documentation or looks up the latest release.
`update` means “match this CLI's bundle”; running an older CLI can therefore
replace your documentation with an older snapshot. For a custom installation,
include its parent directory, such as `--dir .claude/skills`, in each command.

## Preserving Changes

`install` accepts a missing or empty destination, or an unchanged managed
installation matching the bundle. Repeating a matching install does not rewrite
files. If another managed revision is present, use `update`. If another
installer owns the directory, keep using that installer for maintenance;
`arkoren skills update` requires Arkoren's ownership manifest. To switch
installers, preserve local work and move the old directory aside before
installing.

During an update, Arkoren replaces unchanged managed files, adds new files, and
removes unchanged obsolete managed files. Unrelated files remain. Local edits
or deletions, unsafe symlinks, and collisions at new paths block the update
before writes. There is no force-overwrite option. A conflicting dry run prints
its report and exits 2; a conflicting non-preview operation reports an error on
stderr and exits 2. Check exit status as well as the report. The
[command reference](../cli/reference.md) describes output and option details.

If local changes block an update, preserve them elsewhere and restore managed
files to their installed contents before retrying. Keep lasting project rules
in your own instruction files instead of modifying managed reference pages.
Neither installation nor updates rewrite `AGENTS.md`.

Run only one installer at a time and avoid editing managed files during updates.
Individual replacements are atomic, but the full operation is not a
transaction: an interruption or I/O failure can leave partial changes. The
manifest is written last. After a failure, run `status`, inspect the affected
paths, and restore a known-good installation from version control. Alternatively,
preserve local work, move the partial directory aside, and install again.

## Removing Skills

There is no `arkoren skills uninstall` or `remove` command. To uninstall a
CLI-managed copy, first run `status --verbose` with the same `--dir` used at
installation and review the reported directory. Preserve any local edits or
unrelated files inside it, then remove only that installation's `arkoren`
directory, including its manifest. Do not remove the parent `.agents/skills`
or `.claude/skills` directory, which can contain other skills.

Remove any pointers to that copy from your own project instructions. If the
directory is tracked, review the deletions before committing them. Refresh or
restart your agent and confirm it no longer discovers that copy. A second local
or personal copy can still be discoverable, and removing files does not erase
guidance already loaded into an ongoing conversation; start a fresh session
when you need to verify behavior without it.

## Reading Online

An agent with web access can read the current website without installing a
skill. Start with an `llms.txt` index, which links individual Markdown pages with
descriptions, then fetch only the pages your task needs:

| Index | Contents |
| --- | --- |
| `https://arkoren.dev/llms.txt` | All guides and the module API catalog. |
| `https://arkoren.dev/docs/guides/llms.txt` | All guides, grouped by section. |
| `https://arkoren.dev/docs/guides/agent-skills/llms.txt` | This group's guides; replace `agent-skills` with another group to narrow the topic. |
| `https://arkoren.dev/docs/api/llms.txt` | Module-level pkg.go.dev links, not copied API declarations. |

Each guide also has raw Markdown at its page URL with `.md` appended. For
example, this guide is available at
[Using Skills in Markdown](https://arkoren.dev/docs/guides/agent-skills/working-with-agents.md).
HTML guide pages advertise that URL with
`<link rel="alternate" type="text/markdown">`; the shared layout points to the
root index with `rel="describedby"`. Markdown exports link back to the HTML
documentation so an agent can cite the page you read.

The website can be newer than an installed snapshot. Its module catalog does
not verify that each module is published or indexed: if pkg.go.dev returns not
found, use locally available dependency source with `go doc`. Online discovery
does not replace checking your application's selected version.

Use `https://arkoren.dev/llms-full.txt` or
`https://arkoren.dev/docs/guides/llms-full.txt` only for deliberate bulk indexing
or large-context reading. Both contain guides only, not API declarations. Each
archive reports its page count, documentation size, and an approximate token
count based on characters divided by four, not a model-specific tokenizer.
Loading the whole archive can exhaust context and increase processing cost.
With JavaScript enabled, ordinary website archive clicks offer a warning and a
route back to the index; direct text requests have no confirmation step.

These indexes make documentation available to agents that read them; they do
not guarantee discovery or recommendation by an assistant or search engine.
Whether you use local files or web pages, keep reading scoped to the task and
approve consequential actions separately.
