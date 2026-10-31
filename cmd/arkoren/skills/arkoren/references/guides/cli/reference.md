---
title: CLI Reference
nav: Reference
description: Look up CLI commands, reserved flags, input conversion, result rendering, exit statuses, and retry rules.
order: 50
---

# CLI Reference

## Introduction

Use this page after [connecting](connecting.md). Remote command names and
operation-specific inputs come from the current MCP catalog, not a fixed CLI
command list. `arkoren tools` returns a focused JSON catalog of callable commands.

For a command you have discovered, look up [input syntax](#input-syntax),
[output and failures](#output-and-failures), or [exit codes](#exit-codes).
For automation, distinguish [condition waits from cursor following](waiting-and-following.md)
before choosing a timeout or retry policy.

## Commands and naming

| Syntax | Behavior |
| --- | --- |
| `arkoren`, `arkoren -h`, or `arkoren --help` | Generated root help; exits 0 without reading configuration. |
| `arkoren new DIR [flags]` | Offline project creation; flags can precede or follow the destination. |
| `arkoren skills install\|update\|status [flags]` | Offline bundled skill management; flags can precede or follow the subcommand. |
| `arkoren key generate TYPE --out FILE` | Provision `shared`, `signing`, or `exchange` material offline; refuses existing files. |
| `arkoren key check TYPE FILE` | Validate stored key material without printing it. |
| `arkoren key public signing\|exchange FILE [--format pem]` | Print the public key as SPKI PEM. |
| `arkoren key public signing FILE --format vapid` | Print the browser-ready VAPID public key. |
| `arkoren certificate root generate --name NAME --cert FILE --key FILE` | Generate a local root CA pair offline; refuses existing outputs. |
| `arkoren certificate generate --ca-cert FILE --ca-key FILE --dns HOST --cert FILE --key FILE` | Generate a server identity; repeat `--dns` or use repeated `--ip IP` for explicit SANs. |
| `arkoren certificate check --cert FILE [--key FILE]` | Validate public PEM and optionally the private-key match, not expiry, purpose, hostname, or trust. |
| `arkoren certificate root trust ROOT.pem [--yes]` | Explicitly install one validated root in the supported system store. |
| `arkoren certificate root untrust ROOT.pem [--yes]` | Explicitly remove the exact matching root from that store. |
| `arkoren tools [--json]` | Discover the whole remote tool catalog as JSON without invoking tools; `--json` is a compatibility flag. Accepts no positional target. |
| `arkoren NAME -h` or `arkoren NAME --help` | Discover and describe one tool's schema without invoking any tool. |
| `arkoren status` | Report connection and configuration sources; explicitly invoke uniquely advertised identity and health roles. Not a readiness gate. |
| `arkoren --version` | Print the installed CLI version as JSON offline; exits 0 without reading configuration. |
| `arkoren NAME [flags] [arguments]` | Exact advertised MCP tool name. |
| `arkoren GROUP:OPERATION [flags] [arguments]` | Optional metadata-derived command; exactly one colon, neither side empty. |

The old two-word remote invocation is unsupported. Unqualified local commands
win. Remote commands use exact advertised MCP names or metadata-derived
`group:operation` names; there are no built-in diagnostic command aliases.
Exact native names remain callable when metadata supplies an alias and take
precedence over matching aliases. If no exact name matches, an alias advertised
by several tools fails with exit 2 before any tool call; select an exact
`native_name` from `arkoren tools`. Positional order comes only from CLI metadata.

`new --help`, `skills --help`, `key --help`, and `certificate --help` are offline
and run before endpoint or env-file settings are read. Remote discovery reports
when the catalog is unavailable; inspect its reported state rather than assuming exit 0 proves a
connection. Connection setup, `tools`, and remote help only initialize MCP and
read `tools/list`; they never invoke identity, health, or other tools.
`--version` never connects.

`status` explicitly calls a uniquely advertised `identity` role, then a uniquely
advertised `health` role, once each with `{}` inputs. Missing or ambiguous roles
are skipped. Roles describe result semantics, not authorization or a read-only
guarantee. An identity failure stops before health or report output; a health
failure is report data rather than a command failure. Stdout failures still take
precedence. Review the endpoint's tool contracts before requesting status.

Static commands also have offline help for individual subcommands, such as
`arkoren key generate -h` and `arkoren certificate root trust --help`. These
pages list the selected command's arguments, flags, and defaults.
Use attached `-h` or `--help` for help; there is no `help` command. For remote
tools, discover names with `arkoren tools`, then request help for one exact name
or metadata-derived `group:operation` command, such as `arkoren public:routes -h`.
Generated help uses bold section headings on terminals. Piped help stays plain;
`--no-color` or a nonempty `NO_COLOR` disables bold headings too.
Discovered-command help follows the same policy and colors its output JSON schema
only on terminals without a color opt-out.

## Global flags

Global flags are accepted anywhere on a remote command and are reserved from
schema-derived flags. Both one and two leading dashes are accepted.

| Flag | Default and effect |
| --- | --- |
| `--url URL` | No default; otherwise environment, then file. Selects the MCP endpoint. |
| `--token TOKEN` | Optional; otherwise environment, then file. Existing bearer credential. |
| `--env-file PATH` | Implicit `./.env`; explicit nonempty path must exist and approves its URL for inherited tokens. Empty disables file loading. |
| `--compact` | Off; print each ordinary JSON document on one line. |
| `--verbose` | Off; include full captured safe diagnostics, local file lists, tool schemas, and status/build details. Changes content, not indentation or color. |
| `--no-color` | Off; disable terminal coloring. `NO_COLOR` also disables it. |
| `--request-timeout DURATION` | 10s for non-wait requests. `0` removes only the per-request deadline. Waiting reads use their total budget unless explicitly overridden. Not applied to follow. |
| `-f`, `--follow` | Off; follow only tools advertising cursor support. |
| `-h`, `--help` | Show help for the selected command. |

Owned SDK session cleanup has its own one-second maximum, even when
`--request-timeout=0`. Caller cancellation or the remaining command deadline can
end it sooner. This limit bounds cleanup requests, not tool execution; cleanup
failures do not replace a completed command or stdout verdict.

URL trust, env-file syntax, first-nonempty precedence, and credential safety are
defined in [Connecting the CLI](connecting.md#choosing-configuration-sources).
Tool inputs named `timeout`, `wait`, or `fail_fast` are not global flags.
`verbose` is a reserved presentation flag. Supply a remote tool's same-named
input property through `--input`, for example `--input '{"verbose":true}'`.

The offline generator has its own flags: `--module PATH` (destination basename
if omitted), `--skills-dir PATH` (default
`.agents/skills`), `--no-agent-files`, `--dry-run`, and `--yes`. The last
only approves nonempty directories, never collisions. See [Creating a
Project](../first-steps/creating-a-project.md) for filesystem and dependency behavior.

`skills install`, `skills update`, and `skills status` accept `--dir PATH`
(default `.agents/skills`, with `arkoren` appended) and `--dry-run`. They use
the CLI's embedded documentation without reading endpoint settings or fetching
from GitHub. `install` requires an empty destination or a matching unchanged
installation; `update` requires a managed installation. Local changes and new-path
collisions fail with exit 2 before writes. `status` reports differences without
failing just because files are modified. None edits `AGENTS.md`.
See [Working with Agents](../agent-skills/working-with-agents.md) for versioning,
ownership, and partial-update recovery.

[Certificate Commands](certificates.md) defines exclusive file creation,
supported trust stores, default-no confirmation, elevation, and partial-failure
recovery. Trust commands require `--yes` noninteractively and never elevate
automatically. Generation alone never installs trust.

## Input syntax

Every call sends one JSON object. Merge order is `--input`, then metadata-declared
positionals, then flags in command-line order; later sources override earlier
ones. `--input` accepts inline JSON, `@file.json`, or `-` for stdin. Relative files
resolve from the current directory. If `--input` is repeated, only the final
schema-owned input source is read; overridden sources are not opened or merged.
The selected source is read once, including for waits. Materialized bytes remain
JSON data: file contents beginning with `@` or equal to `-` fail JSON parsing
rather than selecting another file or stdin.

Cancellation stops waiting for stdin without closing caller-owned input. A
blocked reader can outlive the canceled command: its read goroutine remains
until the reader returns, for example after its owner supplies EOF or an error.
The CLI cannot forcibly interrupt an arbitrary reader.

| Form | Input behavior |
| --- | --- |
| `--address.city Paris` | Nested object property. |
| `--labels.env prod` | Literal map key `env`. |
| `--items.0.sku A` | Array element; indexes must be dense from 0. |
| `--tag a --tag b` | Repeated scalar-array input. |
| `--tag '["a","b"]'` | Whole array as JSON. |
| `--items '[{"sku":"A"}]'` | Whole object/array value as JSON. |
| `--enabled` | Boolean true. `--enabled=false` and `--enabled false` set false. |

Values convert according to the schema at their path: strings remain strings;
integers and numbers parse numerically; booleans parse as true/false; objects
and arrays parse as JSON. Only conversion is checked locally. The server performs
validation, and returned field errors map to their originating flags.

Both `-x value` and `--x=value` work. Flags do not bundle. `--` ends flag parsing.
Snake_case properties accept kebab-case aliases, but exact property names win
and map keys stay literal. Map keys containing dots, properties named `input`,
and properties colliding with global flags require `--input`. Use the exact
native tool name plus `--input` for schemas outside generated-flag support.

The advertised property type owns flag arity. A string flag followed by
`--wait` or `--input` consumes that token as a literal value, not a second flag.
Pre-discovery wait hints do not override this parsing. Execution budgets and
retry eligibility come from the schema and final merged input, not the presence
of a wait-looking token; see [wait budgets](waiting-and-following.md#setting-the-wait-budget).

## Output and failures

Command results are JSON, including local commands and `--version`. A
nonstreaming result is one document, not a sequence of progress messages.
Help and interactive confirmation prompts remain text. Explicit key exports
(`key public` and `key generate --out -`) remain raw PEM or base64 so they can
be piped directly to their destination.

Remote success prefers structured MCP content, rendered with sorted keys. A
single text block parses as JSON when valid; otherwise it is a JSON string.
Multiple/multimodal blocks preserve protocol content records. Ordinary output is
indented; a terminal adds color unless disabled. Piped output has no color.
`--compact` prints one line. `tools` returns `inspector` discovery context: its
`state`, negotiated `protocol`, and server-supplied implementation version in
`arkoren` when available, or an error explaining unavailable discovery. It does
not return tool-derived `app` identity or identity-tool version overrides.
The `tools` array is sorted by `command`. Each entry contains `command`, the owner's
`description` when supplied, `kind` (`read` or `action`), and `follow`. It also
includes `native_name` when the exact MCP name differs from the command.
These capability hints do not grant authorization or enforce read-only behavior.
By default, schemas, raw MCP catalog records, local command manuals, and CLI rules
are not included; request `NAME --help` for input flags, examples, and output
schema. `tools --verbose` adds advertised
input/output schemas, positionals, examples, and semantic roles, not a CLI manual or
raw protocol dump. Long descriptions are abbreviated by default and marked with
`description_truncated`; verbose discovery keeps them in full. `tools --json` is an accepted
compatibility spelling of the same focused result, not a raw catalog mode.

`status` keeps configuration sources, connection versions, and any returned
instance and health data. Its `arkoren` starts as the native server implementation
version; a string `arkoren` from the identity result overrides it only for this
report. Its `app` summary selects name, version, environment, and state when returned.
Health lists are summarized as status, check count, failing count, and pending
count. `status --verbose` includes full returned application identity/runtime
data and health details without making extra role calls or changing their inputs.
Arkoren's application health tool observes cached checks for the `{}` input;
other servers define the behavior of their advertised roles.

After SDK decoding succeeds, ordinary remote calls preserve domain results,
including unknown fields and exact JSON numbers, without an MCP envelope.
Generic structured content,
catalog schemas and metadata, CLI examples, and approved failure details retain
their wire number tokens. SDK protocol or decoding failures remain failures;
raw capture does not bypass them or provide arbitrary-precision SDK decoding.
Catalog pages and tool calls are sequential within a session, with each raw
result consumed after its SDK request succeeds. This is token fidelity, not
[exact numeric schema enforcement](calling-tools.md#number-precision).

Local reports expose fields rather than completion prose:

| Command | Result fields |
| --- | --- |
| `new` | `created`, `destination`, `module`, `file_count`; dry runs instead include `dry_run`, `yes`, and `conflict_count`. `--verbose` adds `files` and, for previews, `conflicts`. |
| `skills` | `directory`, `state`, `bundled_revision`, installed revision and `modified_file_count` when managed. Install/update reports include operation, dry-run state, write/remove counts and conflict count; successful writes set `completed`. `--verbose` adds modified filenames and per-file actions/conflicts. |
| `key generate` to a file | `created`, `category`, `path`; never key contents. |
| `key check` | `valid`, `category`, absolute `path`. |
| `certificate` generation | `created`, `certificate`, `key`, `trust_changed: false`. |
| `certificate check` | `certificate`, optional `key` path, `parsed`, `key_matched`, `expiry_checked: false`, `trust_checked: false`. |
| Root trust/untrust | `action`, `completed`, `sha256`, `target`. |
| `--version` | `version`; `--verbose` adds Go/module and available revision identifiers, not compiler settings or dependency dumps. |

Project and skill previews report counts by default, not hundreds of paths.
Add `--verbose` for complete lists. A conflicting preview writes one result on
stdout and exits 2 without repeating its list on stderr. Non-preview conflicts
write a concise error on stderr with `conflict_count` and `destination`;
verbose diagnostics add a structured `conflicts` array, never a comma-joined
list inside `message`. No files are written in either conflict path.

`--verbose` is presentation-only: it does not alter remote request bodies,
successful remote results, follow records, authorization, or raw key exports.
Help is already an explicit detail request. Key/certificate receipts need no
extra payload, and trust warnings retain their safety scope even without verbose
mode. Neither mode prints the configured bearer token or implicit private-key
material.

Follow emits compact records as NDJSON, with `dropped`, `instance_changed`, or
`truncated` notices on the same stdout stream. It does not emit page envelopes
or persisted cursor checkpoints. See [Waiting and Following](waiting-and-following.md).

Errors go to stderr as a JSON document with an `error` object. Diagnostics use
the same indentation, `--compact`, `--no-color`, and `NO_COLOR` policy as results.
Color depends on whether stderr itself is a terminal, independently of stdout;
redirected stderr is uncolored. Use `--compact --no-color` for one JSON record
per line in captured diagnostics.
`error.message` is always present. Optional context includes `type`, actual HTTP
`status`, safe `details`, and a `hint`; empty fields and duplicate protocol
title/detail messages are omitted. Validation failures
retain JSON pointers in `fields` and map them to supplying flags or arguments
in `sources`. For example, `/address/city` can map to `-address.city`.
Validation fields are not repeated in `details`. Discovery and health failures
inside summary reports use the same error object structure.
Default diagnostics bound long messages and show at most three validation fields
with one message each. `truncated`, `omitted_fields`, or `details_omitted` identify
abbreviations. Small safe context remains visible; large context is opt-in with
`--verbose`. Verbose mode includes complete captured safe diagnostics, not raw
request headers or protocol traces. Choose it on the original invocation when
you need diagnostic detail; never replay a mutation merely to obtain verbose
output.
Trust commands write a JSON `warning` object on stderr before mutation, even
with `--yes`; terminal confirmation prompts remain text.
Safe structured tool failures use result `_meta["arkoren.dev/failure"]`, outside
successful content. Internal handler and availability errors are redacted.
Failure after a mutation does not prove rollback.

Input-required responses fail with exit 1 after one call and produce no
successful stdout result. Automatic continuation and mutation replay are
disabled; an input request is not permission to repeat the handler.

Output can be partial when a stdout write fails. That failure exits 1 and takes
precedence over the command's other status, including clean follow cancellation.
Do not treat bytes already printed as proof of command success.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Successful command or clean follow cancellation. Immediate false conditions can still be successful reads. |
| 1 | Connection, protocol, execution, structured tool failure, deadline, or stdout failure; also a direct health-role call reporting `status: failing`. |
| 2 | Invalid usage/configuration, unapproved file URL with inherited token, missing endpoint, unknown command, or ambiguous command alias. |
| 4 | Actual HTTP 401 authentication failure. A tool failure's code is not HTTP authentication. |

The direct health-role verdict applies after printing its report; a pending or
stale report is not that failing verdict. `status` does not apply this gate.
A broken pipe is failure, not clean cancellation, even if the remote tool
succeeded.

## Retry rules

Ordinary calls do not retry automatically. Provisional wait hints can enable
discovery transport retries with backoff from 50ms, growing by 1.5 up to 500ms;
they do not authorize execution retries or own schema tokens. After parsing,
only tools annotated read-only, declaring boolean `wait`, and receiving JSON
`true` get the condition-wait execution budget and transport retry policy.
Omitted/zero duration selects 30s, counted from before discovery and input
processing, not from the first tool call. Only a declared string `timeout`
participates in duration parsing and remaining-budget updates. Non-string or
undeclared timeout fields remain tool data; invalid non-string JSON input is
not repaired. Execution retries pause 50ms and use the remaining budget.

HTTP, authentication, certificate-verification, protocol, and tool failures are
terminal. Actions and follow are never replayed. Lost responses and failed
output validation can follow completed effects: verify state and use domain
idempotency instead of automatically retrying actions.

For schemas, annotations, and CLI metadata, see the [MCP API](https://pkg.go.dev/arkoren.dev/core/mcp).
