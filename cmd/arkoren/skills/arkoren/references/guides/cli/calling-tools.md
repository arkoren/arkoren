---
title: Calling Tools
description: Discover available tools, construct inputs, and interpret native MCP results from the CLI.
order: 30
---

# Calling Tools

## Introduction

With a [verified CLI connection](connecting.md), discover what your caller can
use before choosing a command. Tools and schemas come from MCP `tools/list` on
every invocation. The server validates and authorizes calls; CLI help does not
grant permissions.

## Finding the tool and its inputs

List tools, then inspect the operation you intend to call:

```sh
arkoren tools
arkoren public:routes --help
```

Both commands initialize MCP and read `tools/list` only. Neither invokes any
tool, including identity or health tools. The first lists what your caller can
discover. Use its advertised names for the second command, which assumes router
tools in group `public`. Help shows schema-derived flags, declared positionals,
examples, output schema, and whether following is available. `tools` returns a
focused JSON catalog: command, description, kind, follow support, and the native
name when it differs. By default, it omits schemas and CLI manuals; use the
second command for those tool details, or `tools --verbose` for advertised schemas
and metadata in JSON. `--json` remains accepted for compatibility, with the same
focused output.

Optional [CLI metadata](../mcp/tools.md) maps an exact MCP name such as
`public.routes` to `public:routes`. It does not rename the protocol tool: the exact
name remains callable and takes precedence over a matching metadata alias. If
no exact name matches and several tools advertise the same alias, that alias
fails with exit 2 without calling a tool; use an exact name from the catalog's
`native_name` instead.
Without metadata, use the exact advertised name; do not infer positionals or
cursor support from spelling. There are no built-in diagnostic shortcuts.
Local names still win unqualified: `arkoren new --help` describes project
creation, not a remote tool.

The catalog's `kind: read` reflects the server's read-only annotation. Neither
that hint nor a semantic role such as `identity` or `health` guarantees that a
handler is read-only. Unlike discovery, [requesting status](connecting.md#requesting-an-application-status-report)
explicitly invokes the advertised identity and health roles. Review the
endpoint's contracts before calling tools or requesting that report.

## Calling with flags or JSON

To list registered POST routes below `/users`, call the discovered router tool:

```sh
arkoren public:routes --method POST --path /users
```

The result is a JSON object containing matching registrations in `items`.
An empty list means no registrations matched these filters on this router.
Unlike help, this command invokes the selected MCP tool. The router's tool
neither sends a POST request nor executes a route handler; no identity tool is
invoked as part of connection setup.

Flags map to schema properties; snake_case properties also accept kebab-case
flags. For nested objects or schemas outside the generated-flag subset, supply
the argument object directly:

```sh
arkoren public.routes --input '{"method":"POST","path":"/users"}'
```

To load the same input from a file, save that object in `routes.json`:

```sh
arkoren public.routes --input @routes.json
```

The file must contain one JSON object. `--input -` reads that object from stdin.
If you repeat `--input`, only the final source is read; the sources are not merged.
Loaded contents remain data, never another `@file` or stdin selector.
Base JSON is applied first, declared positionals next, then flags in command-line
order; later values override earlier ones. Positionals exist only when metadata
declares their order. See [input syntax](reference.md#input-syntax) for token
ownership, cancellation, nested paths, arrays, literal map keys, and reserved names.

## Interpreting success and failure

Successful calls prefer structured MCP output. A single text block containing
JSON prints as JSON, not an encoded JSON string; non-JSON text prints as a JSON
string. Multiple or multimodal content blocks retain their protocol records.
Use `--compact --no-color` for single-document machine output.

Errors go to stderr as JSON with an `error` object and exit nonzero. Approved
structured failures can arrive in result `_meta["arkoren.dev/failure"]`; that is
failure metadata, not successful structured content.

An input-required MCP response also fails with exit 1 after one call, without a
successful stdout result. The CLI does not fulfill the requested input, continue
the exchange automatically, or replay a mutation.

For managed Arkoren tools, framework classifications override nested `ToolError`
or `ToolFailure` markers. Operational input Check errors and
output-validator resource or cancellation failures remain `internal`/`tool failed`, not
`invalid_input` or `invalid_output`. Internal handler and availability
diagnostics also remain redacted.

Plain `ToolError` messages are capped at 4096 UTF-8 bytes at a code-point boundary;
malformed UTF-8 becomes `tool failed`, while an approved empty message stays empty
at the server boundary. These managed guarantees do not apply to native SDK
registrations or arbitrary external servers;
see [MCP safe failures](../mcp/tools.md#set-hints-and-return-safe-failures).

Check the exit status even if stdout contains output: a direct
health-role call prints its report before exiting 1 for `status: failing`. A
stdout write failure takes precedence over the command's verdict and exits 1;
it does not undo the remote call. A false immediate readiness observation can
still be a successful read:
use a [condition wait](waiting-and-following.md) as an automation gate.

> [!WARNING]
> A mutating tool runs as soon as you call it. Lost responses and output-validation
> failures do not prove rollback. Check the actual application state before
> deciding whether to retry; use domain idempotency where duplicate effects harm.

### Number precision

After a successful SDK response decode, the CLI restores generic result and
catalog fields from captured wire JSON. Structured content, including unknown
domain fields, advertised schemas and CLI examples, and approved failure details
retain their exact JSON number tokens rather than rounded `float64` values.
Input JSON and numeric flags also retain numeric tokens; updating a wait budget
or adding cursor fields does not round unrelated input values. Output formatting
can change whitespace and key order; it is not a byte-for-byte protocol dump.

The SDK still owns protocol and decoding failures. Raw capture does not recover
a response that the SDK rejects, including a number outside an SDK decoder's
supported range. This is not arbitrary-precision SDK decoding. Catalog pages and
tool calls remain sequential within each CLI session; raw results are consumed
after their corresponding SDK request succeeds, not used as a parallel request
interface. `--compact` and `--no-color` affect presentation, not these boundaries.

Token preservation is not output-schema enforcement. Managed Arkoren servers
check exact numeric values within their [strict validation limits](../mcp/tools.md#numeric-validation-and-limits).
This does not extend the CLI SDK's decoding range or repair precision already
lost in application values. Use string-valued fields where the domain or other
clients require them.

For a route-to-trace investigation, follow [Diagnosing an
Application](../observability/diagnosing-an-application.md). For declaration
settings, see the [MCP API](https://pkg.go.dev/arkoren.dev/core/mcp).
