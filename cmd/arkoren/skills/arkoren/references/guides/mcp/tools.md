---
title: MCP Tools
nav: Tools
description: Define validated application operations, test their behavior, and serve them through your existing HTTP service.
order: 30
---

# MCP Tools

## Introduction

After your [first MCP connection](first-server.md), replace the fixed status
result with an operation your application owns. Keep its typed handler in the
application or service package; define a tool around it, then register that
definition at the HTTP composition boundary. Neither the handler nor its input
schema needs the protocol SDK.

The fragments use standard packages `context` and `encoding/json/jsontext`,
plus these application imports: `arkoren.dev/core/mcp`,
`arkoren.dev/foundation/validate`, `arkoren.dev/driver/mcp/mcpserver`,
`arkoren.dev/core/http/router` and `arkoren.dev/core/http/middleware/csrf`.
Direct test fragments run inside a test with
`t *testing.T` from the standard `testing` package.

## Define typed application behavior

For a small example, echo a document identifier rather than pretending to load
a real document. Define the argument, result, and handler:

```go
type Document struct {
	ID string `json:"id"`
}

func echoDocument(ctx context.Context, input Document) (Document, error) {
	return input, nil
}
```

Publish the handler once with metadata clients can discover:

```go
var echo = mcp.NewTool("documents.echo", echoDocument, func(action *mcp.ToolBuilder[Document, Document]) {
	action.Description("Echo a document identifier.")
	action.ReadOnly(true)
	action.OpenWorld(false)
})
```

The Go types supply default object schemas. The JSON argument is `id`; the Go
field is `ID`. Arguments and results must be objects, so wrap lists or scalars
in a struct. For a real lookup, pass an already-authorized application service
method instead of this echo handler.

Definitions are immutable and can share a heterogeneous `[]mcp.Tool`. Check
`Err()` when assembling them; server construction also rejects invalid tools
and duplicate names. Input and output schema bytes are copied when captured and
published; editing a `Describe()` report or a retained construction draft cannot
change the published schemas. Handler captures and immutable schema
implementations remain shared and must support concurrent use.

## Validate input before effects

A blank identifier should fail before a service call. Define the input schema
separately:

```go
var input = validate.Struct(func(object *validate.StructBuilder[Document]) {
	object.Field("ID", func(value *validate.StringBuilder[string]) {
		value.Trim()
		value.Required()
	})
})
```

Add `action.Input(input)` to the tool's construction callback. A call with
`{"id":" doc-42 "}` now returns `{"id":"doc-42"}`; whitespace-only input
fails before the handler. Field selection uses the Go name, not the JSON tag.

Core checks [availability](availability-and-authorization.md) before decoding.
For an available tool, core strictly decodes arguments. After successful decoding,
core parses the input schema once, then invokes the handler once if parsing and
cancellation checks pass. Malformed JSON, nonobjects, duplicate members, unknown
struct fields, and invalid values fail before handler effects. The endpoint
returns a failed tool result rather than decoder diagnostics.

Direct calls distinguish invalid arguments from a failed schema dependency.
Decoding errors and actual rule violations wrap `mcp.ErrInvalidInput`. An
operational error from an input `Check` instead wraps `mcp.ErrInputSchema`, not
`ErrInvalidInput`, while preserving the original cause for `errors.Is` and
`errors.As`. Use `validate.Fail` to report a caller-correctable Check violation;
do not turn a dependency outage into validation advice.

The advertised JSON Schema cannot express every custom rule or normalization.
The typed parser remains authoritative; see [validation schemas](../validation/overview.md).

## Return the exact promised object

Input validation protects the operation before it runs. Output validation checks
the JSON the handler emits, rather than normalizing the typed result. To require
a nonempty ID in the emitted object, define a separate output schema:

```go
var output = validate.Struct(func(object *validate.StructBuilder[Document]) {
	object.Field("ID", func(value *validate.StringBuilder[string]) {
		value.Required()
		value.Min(1)
	})
})
```

Add `action.Output(output)` to the declaration. The adapter validates the encoded
result against this advertised schema; it does not repair the result.
It does not run output `Parse`, apply defaults, trim values, or execute custom
`Check` callbacks. Only JSON-expressible output rules reach that check. Success
returns the same JSON object as both `structuredContent` and text content.

Output checks happen after the handler. A failed response therefore does not
prove that a mutation did not happen. The adapter never retries a handler;
implement transactions and business idempotency in the service. Cancellation is
cooperative, not rollback: pass context to dependencies.

### Numeric validation and limits

Managed output uses `validate.FromJSONSchemaStrict`, which compares the exact
decimal values in the schema and emitted JSON. For example, `9007199254740993`
fails a maximum of `9007199254740992`, and distinct large integers do not become
duplicates in `uniqueItems`. Numbers such as `1e1000` are accepted within the
validator's limits. Precision already lost in an application `float64` cannot
be recovered by encoding it later.

Validation distinguishes missing members, explicit `null`, and zero. Defaults
and formats are annotations, not inserted values or format assertions. The
supported draft-07/2020-12 vocabulary includes compositions and local references;
external references never trigger a fetch. See
[FromJSONSchemaStrict](https://pkg.go.dev/arkoren.dev/foundation/validate#FromJSONSchemaStrict)
for the exact vocabulary and dialect differences.

The validator bounds resource use: each output schema/emitted object is at most
8 MiB with 256 container levels, numeric coefficients have at most 4096 digits, and explicit
decimal exponents range from -4096 to 4096. The sum of coefficient digits plus
absolute exponents is capped at 64 × 1024 × 1024 per decoded document.
Equality/uniqueness structural keys have a cumulative 64 MiB encoding/data budget
per validation.
This is not an exact Go heap limit: capacity growth, copies, and runtime metadata
also consume memory, and key prefixes are charged conservatively.
Compilation and evaluation have additional node, work, reference-depth, and
pointer limits documented by
[EncodedSchema.Validate](https://pkg.go.dev/arkoren.dev/foundation/validate#EncodedSchema.Validate).
A resource refusal or canceled validation returns `IsError=true`, message
`tool failed`, and failure metadata code `internal`, not `invalid_output`.
This does not prove that the handler emitted invalid JSON or undo its effects.

Migration: registration now rejects unknown assertion keywords, unsupported type
names, and unsupported dialects instead of silently ignoring them. Correct such
declarations before upgrading; there is no permissive validation fallback.
Recognized `x-*` extensions remain annotations. Application input `Parse`
behavior is unchanged.

The pinned MCP SDK separately probes every numeric literal in both advertised
schemas during registration. A literal such as `"default":1e1000` exceeds its
decoder's range, so `mcpserver.New` returns an error before SDK registration.
The probe discards its decoded values: published schema bytes stay exact and
managed emitted numbers are not restricted to `float64` range. A client's
generic decoder can still round or reject values the server accepts; see
[CLI number precision](../cli/calling-tools.md#number-precision).

## Set hints and return safe failures

Set `ReadOnly(true)` for nonmutating operations and `OpenWorld(false)` for a
closed domain, as above. Annotations describe behavior; they do not enforce
authorization, confirmation, retries, deduplication, or network restrictions.

When an application failure has an approved public explanation, return a safe
marker from the handler. For example, this deliberately unavailable operation
uses the existing `Document` type:

```go
func readDocument(ctx context.Context, input Document) (Document, error) {
	return Document{}, mcp.ToolError("document is not available")
}
```

The sample returns `IsError=true` with `document is not available`, without any
wrapping context. Plain `ToolError` messages are limited to 4096 UTF-8 bytes,
truncated at a code-point boundary. Short messages, including an explicitly empty
message, are unchanged; malformed UTF-8 becomes `tool failed`. Core retains the
complete original cause and approved text for local inspection. Only mark text
that is safe to disclose, including any record-existence claims.

For approved structured diagnostics, use `ToolFailure`; its code, message, and
optional object details appear under result `_meta["arkoren.dev/failure"]`, not
successful structured content. The plain `ToolError` text cap is not a general
size limit for structured failures or successful output.

Framework error classifications take precedence over nested `ToolError` and
`ToolFailure` markers. Argument-decoding errors and input rule violations become
`invalid tool input`; output encoding and schema violations become
`invalid tool output`. Operational input Check failures and output-validator
resource or cancellation failures instead become `internal` failures with
`tool failed`.
Ordinary unmarked handler errors and recovered panics also become `tool failed`.
A schema, decoder, or encoder cause does not approve client diagnostics merely by
containing a marker.

Input violations can include validation pointers and rendered schema messages,
but not violation parameters, input values, or decoder diagnostics. Keep those
schema messages free of secrets.

Availability failures have a separate [redaction boundary](availability-and-authorization.md#understand-evaluation-and-failure).
Original errors and panic values are logged locally. Protect log access because
application diagnostics can contain secrets despite safe client responses.

## Test behavior before transport

With `echo` configured with the input schema above, a test in the same package
can call core directly:

```go
result, err := echo.Call(context.Background(), jsontext.Value(`{"id":" doc-42 "}`))
if err != nil {
	t.Fatal(err)
}
if string(result) != `{"id":"doc-42"}` {
	t.Fatalf("result = %s", result)
}
```

This verifies parsing and handler output without an HTTP server. Test denial,
invalid input, cancellation, and resource authorization too. Core preserves
error identity for `errors.Is` and propagates panics. It does not exercise
adapter recovery or advertised-output validation, including its
[resource limits](#numeric-validation-and-limits). Use a real SDK client
for those boundaries, following the adapter's
[HTTP tests](https://github.com/arkoren/arkoren/blob/main/driver/mcp/mcpserver/server_test.go).

## Register and mount explicitly

With the `status` definition from the first server and `echo` above, assemble
the endpoint:

```go
endpoint, err := mcpserver.New("reporting", "1.0.0", func(listener *mcpserver.Builder) {
	listener.Tools(status, echo)
})
```

Handle `err` before using `endpoint`. Construction opens no network port and
rejects invalid declarations, duplicate names, and unsupported output schemas.
Only explicit registrations are exposed; the adapter does not inspect services
for methods. `Tools` appends definitions and copies supplied slices.

For fixed, nonsensitive local tools, mount the checked handler on an Arkoren
router:

```go
routes := router.New()
routes.Use(csrf.Middleware())
routes.Mount("/mcp", endpoint)
```

This mount authenticates nobody. Keep the listener on loopback until you add
[typed bearer authentication, TLS, Host, and Origin policy](securing-mcp.md).
CSRF protection is not authentication. That guide adds typed authentication
and optional CORS configuration for trusted browser clients.

## Use your existing lifecycle

The MCP endpoint owns no lifecycle resources and has no `Start` or `Stop`.
Keep your HTTP server's existing timeouts, execution deadlines, and shutdown
path. With Arkoren's application lifecycle, register the HTTP listener's `Stop`
for cleanup, `Start` for startup, and `Serve` as a runner—not the MCP endpoint.
The listener binds at startup and drains on shutdown.
[Running the Server](../http-networking/server.md) covers drain limits and timeouts.
The adapter has no tool-duration setting; a write timeout alone does not stop
application work. The stateless transport has no durable session or resumable
delivery; its [protocol limits](#transport-and-protocol-versions)
also describe client-version-dependent disconnect cancellation.

## Transport and protocol versions

The adapter uses stateless Streamable HTTP with JSON responses, the SDK's
default 4 MiB request-body limit, and no event store. It issues no persistent
session ID; stateless GET and DELETE return 405 and oversized bodies return 413.
It does not serve the separate legacy HTTP+SSE transport or provide a managed
resources/prompts API or server-initiated requests.

The module pins `github.com/modelcontextprotocol/go-sdk v1.8.0`; its transitive
`github.com/google/jsonschema-go v0.4.3` is not the managed output validator.
Supported protocol versions are
2026-07-28, 2025-11-25, 2025-06-18, 2025-03-26, and 2024-11-05. Adapter tests
exercise SDK clients using 2026-07-28 and 2025-06-18 Streamable HTTP.

Request cancellation propagation is enabled, but the SDK applies it only to
2026-07-28 and later. An older client's disconnected HTTP request therefore
does not necessarily cancel its tool. Outer middleware context values remain
available to managed handlers.

SDK localhost DNS-rebinding protection remains enabled. Your application still
owns Host allowlisting and browser Origin protection. There is no adapter
setting for a custom body limit, execution timeout, OAuth metadata, or token
verifier; configure the appropriate outer boundary as described in
[Securing MCP](securing-mcp.md).

## Observe calls without payload capture

With an existing telemetry service named `signals`, add this setting to the
endpoint's construction callback:

```go
listener.Telemetry(signals)
```

Omitted or nil telemetry follows `telemetry.Default()` per call. Managed calls
emit `mcp call_tool` spans, call counts, and duration in seconds, labeled with
the registered tool name and success/error outcome. They do not capture
arguments, output, raw errors, or model-inference attributes. Protected error
logs remain separate; `listener.Logger(logger)` selects your existing
`*slog.Logger`. Configure [telemetry export](../observability/telemetry.md)
and [diagnostic capture](../observability/exposing-diagnostics.md) separately.

## Extend through the native SDK deliberately

`endpoint.Server()` exposes the official SDK server for resources, prompts,
native tools, or another SDK transport such as stdio. There is no Arkoren
stdio facade. Native tools remain visible in `tools/list` but bypass managed
availability, typed validation, safe-error mapping, recovery, telemetry, and CLI
metadata. Do not replace managed registrations or their list middleware: that
can bypass guards or leave metadata stale.

## Add optional CLI presentation

MCP `tools/list` is the only catalog. Optional presentation metadata does not
rename the protocol operation or grant access. For `echo`, add these settings
inside its construction callback:

```go
action.CLI("documents", "echo")
action.Positional("id")
action.Example("Echo an identifier", `{"id":"doc-42"}`)
```

The [Arkoren CLI](../cli/overview.md) consumes this metadata and ordinary MCP
schemas; no separate inspection protocol is needed. Examples are checked for
typed JSON compatibility during construction, never executed.

The adapter serializes CLI metadata and approved failure details as encoded JSON,
without decoding and re-encoding them through `float64` values. Raw number tokens
in these records, published schemas, and emitted raw JSON output remain intact in
the server's MCP responses. This does not guarantee exact numbers after a client
decodes them into generic values; see
[CLI number precision](../cli/calling-tools.md#number-precision).

For exhaustive declarations, defaults, errors, and transport contracts, see
the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp) and
[MCP server API](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver).
