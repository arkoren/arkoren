---
title: Working with Agents
description: Install the Arkoren agent skill offline with the CLI or from GitHub with gh or npx skills, and read the documentation through llms.txt and raw Markdown pages.
order: 10
---

# Working with Agents

## Introduction

Give your coding agent the Arkoren guides rather than relying on its training
data for integration and behavior. Install the
documentation as an **agent skill** in your project for a versioned local copy,
or let the agent fetch plain Markdown from the website through its `llms.txt`
indexes.

Both forms contain the same guides as the website. Exact APIs come from source
documentation, not a copied signature inventory. The
`llms.txt` indexes link to individual pages rather than replacing them with
summaries. The installed skill is a fixed snapshot, so its revision can differ
from the current website until you update it.

This skill supplies documentation for writing code. It does not connect an agent
to a running service or grant permission to call its operations. For runtime
access, follow [connecting the CLI](../cli/connecting.md)
and [discovering and calling tools](../cli/calling-tools.md). An MCP
client can also [connect directly to an MCP server](../mcp/first-server.md).
Neither runtime path requires this skill or an LLM agent inside your service.

## Installing the skill

A **skill** is a folder of instructions and reference pages that an agent
loads when a task needs it. The `arkoren` skill contains a short entry file,
`SKILL.md`, with rules for writing Arkoren code and a table of contents, plus
every guide as a separate Markdown file. The agent reads the entry
file first and can then open only the pages the current task needs, rather than
loading the whole documentation set into context.

Install the [Arkoren CLI](../cli/overview.md#installing-the-cli), then run this
from your project root:

```sh
arkoren skills install
```

The command copies the complete consumer skill embedded directly in the CLI to
`.agents/skills/arkoren/`, including `SKILL.md`, all reference pages, and the
`.arkoren-skill.json` installation manifest. It works offline: no GitHub access,
Git or Node.js installation, running service, or endpoint configuration is needed.
It does not change dependencies or create or edit `AGENTS.md`.

Commit the skill directory and manifest so the team uses the same instructions.
Agent discovery varies. If your agent does not discover this location, point it
at `.agents/skills/arkoren/SKILL.md` in your project's instructions, or select a
skills directory it supports:

```sh
arkoren skills install --dir .claude/skills
```

`--dir` names the **parent** skills directory; the CLI appends `arkoren`.
Relative paths start at the current working directory. The CLI does not search
parent directories for a project or an existing installation. Supply the same
`--dir` on subsequent commands when using a custom location.

For new applications, [`arkoren new`](../first-steps/creating-a-project.md)
already installs the skill and creates a short, editable `AGENTS.md` pointing
to it. Use `--skills-dir .claude/skills` to select another project-relative
location, or `--no-agent-files` to omit both `AGENTS.md` and the skill installation.

### Choosing a version

The CLI carries a fixed snapshot of the documentation. `status` reports the
installed and bundled **content revisions** (SHA-256 identifiers), plus a count
of managed files that are edited, missing, or unreadable:

```sh
arkoren skills status
```

Add `--verbose` to list those filenames.

A content revision identifies documentation bytes, not a Go module version.
The CLI does not infer compatibility from `go.mod`. Before updating, compare
the guide's assumptions with the module versions your project uses. Run `go doc`
from your application's module directory to read the API selected by its module
graph; check the active workspace if it changes dependency selection. Pin the CLI
version if your team needs a reproducible tool and documentation bundle.

To obtain a newer bundle, upgrade the CLI using your chosen installation method,
then preview and apply its bundled revision:

```sh
arkoren skills update --dry-run
arkoren skills update
```

Neither command downloads documentation. An older CLI can also install older
documentation: `update` means "match this CLI's bundle," not "find the latest
release." The Arkoren CLI accepts no GitHub ref and performs no automatic
latest-version lookup.

Each invocation returns one JSON report. `status` reports `directory`, `state`,
and `bundled_revision`, plus `installed_revision` and `modified_file_count` for a
managed installation. Install/update previews summarize planned writes, removals, and
`conflict_count`; successful writes report `completed: true`. Add `--verbose` to
include `modified_files`, per-file `changes`, and complete `conflicts`.
A conflicting preview reports its result once on stdout and exits 2; ordinary
conflicting mutations report a short error on stderr without repeating a plan.
Check the counts and exit status before proceeding.

### Preserving local work

`install` accepts a missing or empty skill directory. Repeating it for an
unchanged matching installation does not rewrite files. If another managed
revision is installed, use `update`; if the directory contains an installation
without an Arkoren manifest, move it aside after review before installing.

The manifest records each managed file's installed hash. `update` replaces
unchanged managed files, adds new files, and removes unchanged obsolete managed
files. Unrelated files remain. Local edits or deletions, symlinks at managed
paths or their parent directories, and collisions with new paths block the
whole update before writes. There is no force-overwrite option.

Preserve edits in a separate location and restore the managed files to their
installed contents before retrying. Skill updates never rewrite `AGENTS.md`;
keep project-specific rules there instead of editing managed reference pages.
`--dry-run` is read-only and reports conflicts with exit code 2, just as a real
conflicting update does.

Each replacement is atomic, but the complete installation/update is not a
transaction. An I/O failure or interruption can leave partial changes; the
manifest is written last. Run `status` and review the named paths before
retrying. Restore a known-good installation from your repository, or move the
partial directory aside after preserving local work and install again. Run
only one installer at a time and avoid editing managed files during updates.

After installation, ask your agent to load `arkoren` and
open the guide for your task. Check that it can read `SKILL.md` and a linked
reference page before relying on the installation.

## Installing with GitHub CLI or npx skills

If you already manage skills with another installer, you can install directly
from GitHub without installing the Arkoren CLI. These alternatives need network
access and an accessible source repository. Use the explicit skill path so the
installer selects the consumer skill rather than contributor or example skills.

With a current [GitHub CLI](https://cli.github.com/manual/gh_skill_install), run
this from your project root:

```sh
gh skill install arkoren/arkoren cmd/arkoren/skills/arkoren --pin main --agent opencode
```

This installs the skill from `main` for OpenCode at project scope. Replace
`opencode` with your agent's supported identifier, such as `claude-code`.
Replace `main` with a commit SHA for a reproducible source snapshot. Without
`--pin`, GitHub CLI prefers the repository's latest release before falling back
to its default branch.

If you use [npx skills](https://github.com/vercel-labs/skills/blob/main/docs/commands.md),
you need Node.js with `npx` and Git. Pass the full directory URL:

```sh
npx skills add https://github.com/arkoren/arkoren/tree/main/cmd/arkoren/skills/arkoren --agent opencode
```

This selects the same consumer skill from `main` for your project. Replace the
agent identifier as needed, or replace `main` in the URL with a commit SHA to
select a fixed snapshot. Add `--list` to preview the skills at that path without
installing them. Use the explicit URL rather than relying on repository-wide
discovery.

Both installers include the skill's reference pages and manage installation
with their own tracking metadata, not the Arkoren CLI's
`.arkoren-skill.json` manifest. Keep using the installer you chose for updates;
do not run `arkoren skills update` on these installations. To switch installers,
preserve local work and move the existing skill directory aside first. After
installation, confirm your agent can read `SKILL.md` and a linked reference page.

## What the skill tells an agent

The rules in `SKILL.md` are the conventions that an agent most often gets
wrong without them. They tell it, among other things, that handlers return
errors instead of writing error responses, that configurable constructors use
builder callbacks as described in [Configuration](../first-steps/configuration.md),
that drivers with a `Start` method verify connectivity there, and that application code
depends on a facade rather than a driver. Most rules link to the guide that
explains them, so an agent that needs more detail knows where to look.

The guide index lists every guide with its group and a one-line description.
When an agent needs an exact signature or symbol contract, it runs `go doc`
against the application's selected dependency source. For example, from your
application's module directory:

```sh
go doc arkoren.dev/core/service/cache.New
```

This uses Go's module and workspace selection rather than the CLI's content
revision. Once the required dependency source is available locally, `go doc`
also works offline. Installing the skill alone does not install that source.
For online lookup, the website API catalog links to pkg.go.dev at module level;
select the package and matching version there.

## Reading the documentation from the website

If your agent works from the web rather than from installed skills, point it at
the `llms.txt` files. They follow the [llms.txt](https://llmstxt.org)
convention: a Markdown index with one link per page and a one-line description.

| File | What it lists |
| --- | --- |
| `https://arkoren.dev/llms.txt` | Every guide, grouped by section, and the module API catalog. |
| `https://arkoren.dev/docs/guides/llms.txt` | Every guide, grouped by section. |
| `https://arkoren.dev/docs/guides/<group>/llms.txt` | The guides of one group, such as `database`. |
| `https://arkoren.dev/docs/api/llms.txt` | The module-level pkg.go.dev catalog. |

For coding tasks, start with an index and fetch only the guides
your task needs. Use a group index when you already know the topic. You do
not need to load all of Arkoren's documentation to use one package.

The site-wide and guides indexes have optional `-full` companions containing
guides only. Local API Markdown and bulk API exports are retired:

| Bulk archive | Contents |
| --- | --- |
| `https://arkoren.dev/llms-full.txt` | Every guide. |
| `https://arkoren.dev/docs/guides/llms-full.txt` | Every guide. |

These exhaustive exports are for offline indexing, bulk processing, or
deliberate large-context use, not the normal reading path. Loading a full
archive into an agent can exhaust its context window and increase processing
cost. Each archive opens with its page count, documentation size, and a rough
token estimate based on four characters per token. This is not a model-specific
tokenizer count; check your model's limits before loading an archive.

The module catalog links to pkg.go.dev without probing release or indexing
availability. An unpublished or unindexed module can return not found; use
locally available source with `go doc` instead. The catalog does not promise
that every version is already published.

With JavaScript enabled, ordinary clicks on website archive links open a warning
with actions to cancel, browse the matching index, or open the archive. Direct
text URLs remain accessible without a confirmation step, including to agents.

Every guide is also available as raw Markdown at the same
address with `.md` appended. For example, this page is at
`https://arkoren.dev/docs/guides/agent-skills/working-with-agents.md`.
HTML pages announce their Markdown version with a
`<link rel="alternate" type="text/markdown">` element, so a crawler can find
it without knowing the convention.

The shared page layout also links to the root `llms.txt` index with
`rel="describedby"`. Individual Markdown pages and the full bundles include
links back to the HTML documentation, so an agent can cite the page you read.
The website footer links to this guide under **Use with your agent**.

These files help agents that choose to read them; they do not guarantee that
a search engine or an assistant discovers or recommends Arkoren. The HTML
documentation remains the preferred format for search results.

## Tips for working with an agent

Agents follow the same path through the documentation that people do, so a
few habits help:

- Ask the agent to read the relevant guide before it writes code for an
  unfamiliar area, such as queues or workflows. The guide explains behavior
  that a signature cannot, like when a job is retried or which errors a
  facade returns.
- When you choose a driver, name it in the prompt. Facades make the code the
  same for every driver, but configuration and local setup differ, and each
  driver's section explains them.
- For a new service, ask the agent to read [Installation](../first-steps/installation.md),
  preview with `arkoren new billing --dry-run`, and show the destination and conflicts
  before writing. Generation is offline; install dependencies before building.
  For an existing service, connect the CLI and
  discover its tools before calling qualified `group:operation` commands.
- Keep the installed skill aligned with your `arkoren.dev/core` version.
  Documentation for a newer release can describe functions your version does
  not have yet.
