---
title: CLI Reference
nav: Reference
description: Look up command forms, reserved flags, input conversion, output contracts, exit codes, and retries.
order: 50
summary:
  - Look up fixed local commands, discovered remote names, reserved flags, and local options.
  - Build schema-guided JSON inputs with explicit source precedence and conversion rules.
  - Interpret report fields, output streams, diagnostics, exit codes, and restricted retries.
---

# CLI Reference

Local commands have fixed syntax. Remote commands come from the selected MCP
endpoint on every invocation; use `arkoren tools` and the selected command's
`--help` for its current contract. [Connecting](connecting.md) owns credential
and destination policy; [Calling Tools](calling-tools.md) demonstrates a call.

## Command Forms

Uppercase operands below are values you supply, not literal words.

| Form | Contract |
| --- | --- |
| `arkoren`, `arkoren -h`, `arkoren --help` | Offline root help as text; no endpoint configuration is read. |
| `arkoren --version` | Installed CLI version as JSON, offline. |
| `arkoren new DIR [flags]` | Create a starter project offline, without downloads or overwrites. |
| `arkoren skills install\|update\|status [flags]` | Manage the CLI's bundled documentation offline. |
| `arkoren key generate TYPE --out FILE` | Generate `shared`, `signing`, or `exchange` private material. |
| `arkoren key check TYPE FILE` | Validate stored key material without exporting it. |
| `arkoren key public signing\|exchange FILE [--format pem]` | Export SPKI public-key PEM. |
| `arkoren key public signing FILE --format vapid` | Export a VAPID public key. |
| `arkoren certificate root generate --name NAME --cert FILE --key FILE` | Generate a root CA pair without installing trust. |
| `arkoren certificate generate --ca-cert FILE --ca-key FILE --dns HOST --cert FILE --key FILE` | Generate a server identity; repeat `--dns` or `--ip IP` for explicit SANs. |
| `arkoren certificate check --cert FILE [--key FILE]` | Parse public PEM and optionally check the matching private key. |
| `arkoren certificate root trust FILE [--yes]` | Explicitly install one public root in the supported system store. |
| `arkoren certificate root untrust FILE [--yes]` | Remove trust for that exact root in the selected system scope. |
| `arkoren tools [--json]` | Discover tools as JSON without invoking them; no positional target is accepted. |
| `arkoren status` | Explicitly call uniquely advertised identity and health roles for a report. |
| `arkoren NAME --help` | Discover one remote tool's schema without invoking it. |
| `arkoren GROUP: --help` | Describe an advertised CLI group, unless an exact native name takes precedence. |
| `arkoren NAME [flags] [arguments]` | Invoke an exact advertised MCP name. |
| `arkoren GROUP:OPERATION [flags] [arguments]` | Invoke a metadata-derived alias. |

Local command help is offline, including `tools --help` and `status --help`.
Ask for a leaf's flags without reading configuration or accessing key material:

```sh
arkoren key generate --help
```

Use attached `-h` or `--help`; there is no built-in `help` command. Remote help
needs the endpoint and obeys the same destination-approval rules as execution.
Connection setup, `tools`, and remote help initialize MCP and read `tools/list`
only; they never call identity, health, or other tools.

## Command Names

Unqualified local names win. Exact native MCP names are tried before CLI aliases
and remain callable when an alias exists. A metadata alias uses exactly one
colon with a nonempty group and operation; two-word remote invocation is not
supported. Positionals exist only when metadata declares their order.

If several tools advertise one alias and no exact name matches, dispatch exits
2 before calling anything. Choose an exact name from `tools`; `native_name` is
included when different from `command`. Catalog commands are sorted and chosen
to avoid shadowed aliases. There are no built-in diagnostic aliases.

## Global Flags

Reserved global flags can appear anywhere before `--` and take precedence over
schema-derived flags. One or two leading dashes work; flags do not bundle.

| Flag | Default | Effect |
| --- | --- | --- |
| `--url URL` | No URL | Select the MCP mount; otherwise environment, then file. |
| `--token TOKEN` | No token | Supply an existing bearer credential; otherwise environment, then file. |
| `--env-file PATH` | Implicit `./.env` | Replace the file; an explicit nonempty path must exist and approves its URL for inherited tokens. Empty disables file loading. |
| `--compact` | Off | Print ordinary JSON documents on one line. |
| `--verbose` | Off | Include complete captured safe diagnostics, schemas, local file lists, and status/build detail. |
| `--no-color` | Off | Disable terminal coloring and bold help headings. Nonempty `NO_COLOR` also disables them. |
| `--request-timeout DURATION` | `10s` | Per-request deadline for ordinary requests; `0` removes that deadline. Waiting reads normally use their total budget. Does not limit follow. |
| `-f`, `--follow` | Off | Follow only tools advertising cursor support. |
| `-h`, `--help` | Off | Describe the selected command. |

`wait`, `timeout`, and `fail_fast` are tool inputs, not global flags. A property
named `verbose`, `input`, or another reserved global cannot be set by its own
generated flag. Supply it in the base object instead: for a tool with a boolean
`verbose` input, add `--input '{"verbose":true}'` to the discovered command.

`--verbose` changes presentation, not remote request bodies, successful tool
payloads, follow records, authorization, or raw exports. Use it on the original
invocation; do not replay a mutation to obtain diagnostics.

Session cleanup has its own one-second maximum, including with
`--request-timeout=0`. Caller cancellation or the remaining condition-wait
deadline can end cleanup sooner. This bounds cleanup, not tool execution;
cleanup errors cannot replace a completed verdict or stdout failure.

## Local Options

`new` accepts flags before or after `DIR`:

| Flag | Default or effect |
| --- | --- |
| `--module PATH` | Destination basename if omitted. |
| `--skills-dir PATH` | Project-relative `.agents/skills`. |
| `--no-agent-files` | Omit bundled skills and `AGENTS.md`. |
| `--dry-run` | Report the plan without writing. |
| `--yes` | Approve a nonempty directory, never a generated-file collision. |

See [Creating a Project](../first-steps/creating-a-project.md) for filesystem
safety and dependency setup. A conflicting preview prints one stdout report and
exits 2. Non-preview conflicts go to stderr with `conflict_count` and
`destination`; neither path writes files. Add `--verbose` for per-path lists.

`skills install`, `skills update`, and `skills status` accept `--dir PATH`
(default `.agents/skills`, with `arkoren` appended) and `--dry-run`, before or
after the subcommand. They do not fetch from GitHub, read endpoint settings, or
edit `AGENTS.md`. Install requires an empty destination or matching unchanged
installation; update requires a managed installation. Local changes and
new-path collisions fail with exit 2 before writes. Status reports differences
without failing merely because files changed. See
[Working with Agents](../agent-skills/working-with-agents.md) for recovery and
ownership.

[Key Commands](keys.md) owns private exports and exclusive file creation.
[Certificate Commands](certificates.md) owns file-pair safety and supported trust
stores. Certificate generation never installs trust. Trust operations require
default-no terminal confirmation or bare `--yes` noninteractively; `-yes` and
value-bearing approval forms are invalid. The CLI never elevates itself.

## Input Syntax

Each remote call sends one JSON object. Merge order is base `--input`, then
metadata-declared positionals, then flags in command-line order. Later assignments
override earlier values at the same path; repeated scalar-array flags append.
Traversing a value of the wrong object/array shape is an error.

`--input` accepts inline JSON, `@file.json`, or `-` for stdin. Relative files
resolve from the working directory. If repeated, only the final schema-owned
source is read, once, including for waits. Overridden sources are not opened or
merged. Materialized bytes are data: contents beginning with `@` or equal to `-`
fail JSON parsing rather than selecting another source.

The following forms apply only when the advertised schema declares the
corresponding property and type:

| Form | Meaning |
| --- | --- |
| `--address.city Paris` | Nested object property. |
| `--labels.env prod` | Literal map key `env`. |
| `--items.0.sku A` | Array element; indexes must be dense from 0 and no flag index can exceed 10000. |
| `--tag a --tag b` | Append two scalar-array elements. |
| `--tag '["a","b"]'` | Set the whole array from JSON. |
| `--items '[{"sku":"A"}]'` | Set an object-array value from JSON. |
| `--enabled` | Boolean `true`; `--enabled=false` or `--enabled false` sets false. |

String values stay strings; integers parse as decimal without a machine-size
limit, and numbers accept JSON syntax without floating-point conversion.
Objects and arrays parse as JSON. The CLI checks conversion, not the server's
validation rules. Validation errors can map back to their supplying inputs.

`-x value` and `--x=value` are accepted. `--` ends flag parsing; a bare `-` or
negative number can be positional data. Snake_case properties accept kebab-case
aliases, but exact properties win and map keys stay literal. Map keys containing
dots and properties colliding with reserved names require `--input`. For schemas
outside generated-flag support, use the exact tool name with `--input`.

The schema owns flag arity. A string flag followed by `--wait` or `--input`
consumes that token as a literal value. Provisional discovery hints do not
override parsing; execution deadlines and retry eligibility use the final
merged input and advertised types.

Cancellation stops waiting for stdin without closing caller-owned input. An
arbitrary blocked reader can outlive the canceled command; its read goroutine
ends when the owner supplies EOF or an error. The CLI cannot forcibly interrupt
that reader.

## Output Formats

| Output | Format |
| --- | --- |
| Ordinary local or remote result, including `--version` | One JSON document. |
| `--follow` | Compact NDJSON records and loss notices on the same stdout stream. |
| Help and interactive confirmation | Text. |
| `key public` and `key generate --out -` | Raw PEM or base64, not JSON strings. |
| Errors | JSON with an `error` object on stderr. |
| Trust warning before mutation | JSON with a `warning` object on stderr, even with `--yes`. |

Ordinary JSON is indented by default. Terminal output adds color unless
disabled; pipes do not. `--compact` changes indentation, not content. Help uses
bold terminal headings and plain piped headings; remote output schemas follow
the same color policy. Stderr independently checks whether its own stream is a
terminal. Use `--compact --no-color` for one-line JSON diagnostics in captures.

Remote results prefer structured MCP content with sorted keys. A single text
block parses as JSON when valid, otherwise it becomes a JSON string. Multiple
or multimodal blocks retain protocol content records. Generic results preserve
unknown domain fields and exact wire number tokens after successful SDK
decoding, without an MCP envelope. This does not bypass SDK decoding limits,
enforce output schemas, or repair precision lost in application values; see
[Number Precision](calling-tools.md#number-precision).

Follow emits individual records rather than page envelopes or persisted cursor
checkpoints. Notice fields include `dropped`, `instance_changed`, and
`truncated`; consumers must handle them alongside records. See
[Waiting and Following](waiting-and-following.md) for gaps and restart behavior.

## Report Fields

`tools` returns `inspector` context and a `tools` array sorted by `command`.
Inspect `inspector.state`, not just the exit status: absent configuration or an
unreachable endpoint can be report data with exit 0. Successful discovery
includes `protocol` and the server implementation version in `arkoren` when
supplied. It never calls identity to obtain `app` or a version override.

Each tool summary contains `command`, optional description, `kind` (`read` or
`action`), and `follow`. `native_name` appears when different. Descriptions can
be abbreviated with `description_truncated`; verbose discovery includes full
descriptions, input/output schemas, positionals, examples, and semantic roles.
It is not a raw MCP catalog or local CLI manual. `tools --json` is a compatibility
spelling of the same focused result.

`status` calls a uniquely advertised identity role before a uniquely advertised
health role, once each with `{}`; missing or ambiguous roles are skipped.
Identity failure stops before health or output. Health failure appears as data
and does not itself fail status. The report retains configuration sources,
negotiated protocol, implementation version, optional process `instance`, and
health. A string identity `arkoren` overrides the implementation version for
this report only. `app` selects name, version, environment, and state when
returned; verbose mode keeps full identity/runtime data. Health lists become
status and check/failing/pending counts; verbose mode keeps full details.
Neither mode makes extra role calls. Arkoren's health tool with `{}` observes
cached checks; external servers define their own role semantics.

Local reports use these fields rather than completion prose:

| Command | Result fields |
| --- | --- |
| `new` | `created`, `destination`, `module`, `file_count`; previews include `dry_run`, `yes`, `conflict_count`. Verbose adds paths and preview conflicts. |
| `skills` | `directory`, `state`, `bundled_revision`, and installed revision/modification count when managed. Install/update add operation, preview state, write/remove/conflict counts; completed writes set `completed`. Verbose adds per-file details. |
| `key generate` to file | `created`, `category`, absolute `path`; no private contents. |
| `key check` | `valid`, `category`, absolute `path`. |
| Certificate generation | `created`, `certificate`, `key`, `trust_changed: false`. |
| `certificate check` | `certificate`, optional `key`, `parsed`, `key_matched`, `expiry_checked: false`, `trust_checked: false`. |
| Root trust/untrust | `action`, `completed`, `sha256`, `target`. |
| `--version` | `version`; verbose adds Go/module and available revision identifiers, not dependency or compiler-setting dumps. |

## Failure Diagnostics

`error.message` is present in emitted error reports. Optional context includes
`type`, actual HTTP `status`, safe `details`, and a `hint`; empty values and
duplicate protocol title/detail messages are omitted. Validation `fields` retain
JSON pointers; `sources` map them to supplied flags or positionals where known.
For example, `/address/city` can map to `-address.city`. Validation fields are
not duplicated in `details`. Embedded discovery and health errors use this same
shape.

Default diagnostics bound long text and show at most three validation fields
with one message each. `truncated`, `omitted_fields`, and `details_omitted` expose
abbreviation. Small safe context stays visible; `--verbose` includes complete
captured safe context, not raw headers or protocol traces. Neither mode prints
the configured bearer token or implicit private-key contents.

Managed Arkoren failures can use `_meta["arkoren.dev/failure"]`; internal handler
and availability details are redacted. Operational input-check errors and
resource/cancellation failures during output validation remain internal failures,
not user validation failures. Managed plain `ToolError` text is capped at 4096
UTF-8 bytes at a code-point boundary; malformed UTF-8 becomes `tool failed`, and
an approved empty message remains empty at the server boundary. These server
guarantees do not apply to arbitrary native or external tools. See
[safe failures](../mcp/tools.md#safe-failures).

An input-required response exits 1 after one call, without successful stdout.
Automatic continuation is disabled. Failure after a mutation does not establish
rollback. A stdout write failure can leave partial bytes, exits 1, and takes
precedence over all other verdicts, including clean follow cancellation.

## Exit Codes

| Code | Meaning |
| --- | --- |
| `0` | Successful command or clean follow cancellation. A false immediate condition can still be a successful read. |
| `1` | Connection, protocol, execution, structured tool failure, deadline, file/trust operation, or stdout failure; also a direct health-role report with `status: "failing"`. |
| `2` | Invalid usage/configuration, unapproved file URL with an inherited token, missing required endpoint, unknown command, or ambiguous alias. |
| `4` | Actual HTTP 401 authentication failure, not a tool failure with a similarly named code. |

The direct health-role verdict applies after its report is printed; pending or
stale health does not trigger that verdict. `status` does not apply the gate.
A broken pipe is failure even if the remote tool succeeded. Always check process
status rather than treating stdout as a success receipt.

## Retry Rules

Ordinary calls do not retry automatically. Only a read-only tool declaring
boolean `wait` and receiving JSON `true` qualifies for condition-wait execution
retries. Only a declared string `timeout` sets or receives its remaining budget;
omitted/zero duration selects 30s from before discovery and input processing.
Other timeout types remain tool data. Provisional wait hints can permit
discovery transport retries, not execution replay or token ownership.

Discovery backoff starts at 50ms, grows by 1.5, and caps at 500ms. Eligible
execution retries pause 50ms within the remaining budget. HTTP, authentication,
certificate-verification, protocol, and tool failures are terminal. Actions and
follow are never replayed. Lost responses or output-validation failures can
follow completed effects; verify state and use domain idempotency instead of
automatically retrying a mutation.
