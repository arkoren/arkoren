---
title: Calling Tools
description: Choose a discovered command, prepare its inputs, and interpret its result without replaying effects.
order: 30
summary:
  - Select an advertised tool and inspect its schema without invoking a handler.
  - Call a route-listing tool with flags, inline JSON, or a file-backed object.
  - Interpret JSON, failure diagnostics, exit status, and numeric fidelity without replaying mutations.
---

# Calling Tools

When you need an application observation or action, choose it from the current
MCP catalog rather than assuming a built-in command exists. Start with a
[verified connection](connecting.md). The endpoint supplies tools and schemas on
every invocation and authorizes each call; CLI help grants no permissions.

## Choose Tools

List what your caller can discover:

```sh
arkoren tools
```

This initializes MCP and reads `tools/list` without invoking any tool, including
identity or health. The JSON catalog includes `command`, description, `kind`,
and `follow`, plus `native_name` when it differs from the CLI spelling.
`kind: "read"` reflects the owner's read-only annotation, not an enforced
guarantee about its handler.

Optional [CLI metadata](../mcp/tools.md) maps a name such as `public.routes` to
`public:routes`. Without metadata, use the exact MCP name. Exact names remain
callable and take precedence over matching aliases. If several tools advertise
one alias and no exact name matches, the alias fails with exit 2 before a call;
choose an exact name from the catalog instead.

Local command names win unqualified: `arkoren new` creates a project, not a
remote tool call. There are no built-in diagnostic shortcuts, and tool spelling
alone does not declare positional arguments or following support.

## Read Help

Suppose discovery lists the router command `public:routes`. Inspect its inputs
before calling it:

```sh
arkoren public:routes --help
```

Help is text generated from the current schema and metadata. It shows flags,
declared positionals, owner-supplied examples, output schema when present, and
follow support. It performs discovery only. Use `tools --verbose` if you need
the advertised schemas and metadata as JSON instead of a command manual.

## Pass Inputs

To find registered POST routes whose patterns start with `/users`, call the
discovered router tool:

```sh
arkoren public:routes --method POST --path /users
```

This invokes the listing tool, not a POST route handler. Its JSON result contains
matching registrations in `items`; an empty list means no registrations matched
on that router. Connection setup does not invoke identity as an extra step.

Flags map to schema properties. For a nested input or a schema outside generated
flag support, pass one JSON object. The exact MCP name for this example is
`public.routes`:

```sh
arkoren public.routes --input '{"method":"POST","path":"/users"}'
```

This sends the same filters. If you keep that object in `routes.json`, load it
from the working directory instead:

```sh
arkoren public.routes --input @routes.json
```

`--input -` reads the object from stdin. Repeated `--input` selects only the
final source; earlier sources are not read or merged. Loaded bytes stay JSON
data, never another file or stdin selector.

Base JSON is applied first, metadata-declared positionals next, then flags in
command-line order. Later values override earlier ones. Snake_case properties
also accept kebab-case flags, but exact property names win. The
[input reference](reference.md#input-syntax) covers reserved names, nested paths,
arrays, literal map keys, and tokens that look like flags.

## Interpret Results

A successful non-follow call prints one JSON document, without an MCP envelope.
Structured content is preferred. A single text block containing JSON prints as
JSON; other text prints as a JSON string. Multiple or multimodal blocks retain
their protocol content records.

For an uncolored, one-line result suitable for capture, add presentation flags:

```sh
arkoren public:routes --method POST --path /users --compact --no-color
```

These flags do not change the request or the tool's successful result. Check
the process exit status even when stdout contains a document: a direct
health-role call prints its report before exiting 1 for `status: "failing"`.
An immediate readiness observation can instead succeed with `ready: false`;
use a [condition wait](waiting-and-following.md#gate-readiness) to gate work.

## Handle Failures

> [!WARNING]
> Calling a mutating tool executes it without a confirmation prompt. A lost
> response, output-validation failure, or broken stdout does not prove rollback.
> Check application state and use domain idempotency before deciding to retry.

Ordinary calls do not retry automatically. An input-required MCP response fails
with exit 1 after one call, without a successful stdout result. The CLI does not
fulfill the request, continue automatically, or replay the handler.

Errors appear on stderr as JSON with an `error` object. Server validation
failures can include JSON pointers and the flags or arguments that supplied
them. Choose `--verbose` on the original invocation if you need complete
captured safe diagnostics; never replay a mutation merely to get more detail.

Approved Arkoren failures use result `_meta["arkoren.dev/failure"]`, not
successful content. Managed tools redact internal handler and availability
failures; their [safe-failure contract](../mcp/tools.md#safe-failures)
does not extend to arbitrary external servers. Treat captured results as
application data with the appropriate access controls.

Stdout failure exits 1 and takes precedence over other verdicts. Already printed
bytes can be partial and are not proof of success. See the
[exit-code reference](reference.md#exit-codes) before using results in scripts.

## Number Precision

Input JSON retains its number tokens, and numeric flags avoid floating-point
rounding; decimal integer flag spellings are normalized. After a successful
SDK response decode, captured wire JSON also preserves numbers in generic
structured results, unknown domain fields, catalog schemas and examples, and
approved failure details. Formatting can change whitespace and key order; this
is not a byte-for-byte protocol dump.

The SDK still owns decoding and protocol failures. Capture cannot recover a
response the SDK rejects or extend its supported numeric range. Token fidelity
is not output-schema enforcement and cannot repair precision already lost in
application values. Managed servers enforce their own
[numeric validation limits](../mcp/tools.md#schema-limits);
use string-valued fields when your domain or other clients require them.

For a route-to-trace investigation, continue with
[Application Diagnosis](../diagnostics/application-diagnosis.md). For tool
declarations, see the [MCP API](https://pkg.go.dev/arkoren.dev/core/mcp).
