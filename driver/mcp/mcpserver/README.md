# mcpserver

Expose typed `arkoren.dev/core/mcp` tools through the official MCP Go SDK's
stateless Streamable HTTP transport. This is a protocol adapter, not an LLM
provider or an application lifecycle component. Diagnostics use native MCP tools.

Install in your application's module directory:

```sh
go get arkoren.dev/core arkoren.dev/driver/mcp
```

Import `mcpserver` from `arkoren.dev/driver/mcp/mcpserver` and `mcp` from
`arkoren.dev/core/mcp`. With your published `lookup mcp.Tool` and existing
`mux *http.ServeMux`, register only the operations intended for this endpoint:

```go
endpoint, err := mcpserver.New("catalog", "1.0", func(listener *mcpserver.Builder) {
    listener.Tools(lookup) // A published core/mcp.Tool.
})
mux.Handle("/mcp", endpoint) // Put your authentication middleware outside it.
```

The application owns the HTTP listener, authentication, authorization, origin
policy, and timeouts. `New` performs no network I/O of its own. Tools run concurrently and
receive the outer request context. The SDK bounds bodies to 4 MiB by default.
Managed calls never replay a handler after an output-validation failure.
Bind a nonsensitive local demonstration to loopback; require authentication,
Host/Origin policy, and TLS before exposing private tools or remote access.
Follow [Your First MCP Server](../../../cmd/arkoren/skills/arkoren/references/guides/mcp/first-server.md)
for a complete process and a client-verified tool result.

This module pins `github.com/modelcontextprotocol/go-sdk v1.8.0`. Managed output
uses Arkoren's strict encoded-JSON validator, not the SDK's transitive
`github.com/google/jsonschema-go` validator. It checks exact numeric values without
defaults or normalizers. Safe generic messages cross the
protocol boundary; handler `mcp.ToolError` messages are exposed without wrapping
context, up to 4096 UTF-8 bytes at a code-point boundary. Short messages, including
empty ones, are unchanged; invalid UTF-8 becomes `tool failed`. Core retains the
complete original error and approved message for local inspection.
Use `mcp.ToolFailure(code, message, details)` to approve a structured diagnostic:
the adapter publishes it under result `_meta["arkoren.dev/failure"]`, never in
successful structured output. Details must be an optional JSON object. Invalid
input reports only validation pointers and rendered schema messages, not raw
decoder errors or validator parameters. Keep schema messages free of secrets.
Protect local logs because they retain the original failure details.

Framework classifications override approval markers nested in input or output
errors. Input rule violations remain `invalid_input`; an operational input
`Check` failure wraps `mcp.ErrInputSchema` in core and becomes an `internal`
failure with `tool failed` over MCP, even if its cause contains `ToolError` or
`ToolFailure`. Use `validate.Fail` for caller-correctable schema violations.
Output encoding failures remain `invalid_output` without disclosing their causes.

Schema bytes are captured at tool construction and detached from retained drafts
and metadata reports. Raw JSON arguments, output, CLI examples, and approved
failure details retain number tokens across SDK serialization. Typed integer
inputs remain exact; use `jsontext.Value` fields for opaque numbers rather than
`any`, which follows the standard decoder's `float64` semantics.
Clients that decode protocol values into `any` can still round them; exact wire
tokens do not guarantee exact values in a client's generic decoded records.

Output constraints compare exact decimal values, including nested equality and
uniqueness checks and numbers outside `float64` range such as `1e1000`. Missing
members, explicit null, and zero remain distinct. Formats and defaults are
annotations; validation never inserts values. The validator has fixed resource
limits, including 8 MiB per output schema/emitted object and decimal exponents
from -4096 to 4096. The sum of coefficient digits plus absolute exponents is
capped at 64 × 1024 × 1024 per decoded document. Equality/uniqueness structural keys
have a cumulative 64 MiB encoding budget per validation, not an exact Go heap cap;
see [EncodedSchema.Validate](https://pkg.go.dev/arkoren.dev/foundation/validate#EncodedSchema.Validate)
for the complete bounds. Resource refusals and cancellation fail internally with
`tool failed`, not `invalid_output` or raw diagnostics. No failure replays the handler.

Registration rejects unsupported assertions, type names, dialects, and external
references instead of silently ignoring them. Correct unsupported declarations
before upgrading; the supported vocabulary is documented by
[FromJSONSchemaStrict](https://pkg.go.dev/arkoren.dev/foundation/validate#FromJSONSchemaStrict).
The SDK also probes both declared schemas with its own numeric decoder. A schema
literal such as `"default":1e1000` exceeds that decoder's range and makes `New`
return an error before registration, rather than panic. This declaration-only
probe discards its decoded values; it does not round published schema bytes or
restrict emitted numbers to `float64` range. Client decoder limits remain separate.

Use `ToolBuilder.Available(func(context.Context) (bool, error))` for capability
visibility based on the caller's role or plan. The policy is observational,
concurrency-safe, and evaluated per list request and again before calls, without
cross-request caching. Construction never evaluates it. A false result hides
the managed tool from `tools/list`; direct calls return "tool unavailable"
before argument decoding, normalization, or handler execution. Policy errors
and panics fail the entire list request with sanitized text, and fail calls
without exposing even `ToolError` policy diagnostics. Keep argument-dependent
resource authorization inside handlers.
Use `tool.WithAvailability(policy)` to add application policy to a tool returned
by another package. The returned copy requires both policies; it cannot bypass
the owner's policy. Nil is invalid and construction evaluates neither callback.

Tool discovery is deliberately unpaginated: one response contains the complete
filtered catalog, even above the SDK's default page size. No cursor is emitted;
nonempty input cursors are rejected. This avoids filtering after a page boundary
and does not rebuild servers or compile schemas during requests. Large catalogs
therefore use proportionally larger responses and evaluate all policies per list.
The SDK page-size setting also makes native resource and prompt lists unpaginated.

Clients use Streamable HTTP POST, with JSON responses. The adapter does not
offer the separate legacy HTTP+SSE transport. Older Streamable HTTP protocol
versions remain usable as supported by the SDK (tested with 2025-06-18).
HTTP request cancellation propagates for protocol 2026-07-28 and later; the
SDK does not offer this guarantee for older versions. Stateless GET and DELETE
return 405. There is no session persistence, event-store replay, server-initiated
request support, or managed resources/prompts API.

`tools/list` is the only catalog; listing never calls handlers. Optional
`ToolBuilder.CLI(group, operation)` presentation appears under tool
`_meta["arkoren.dev/cli"]`, independently of exact protocol names. Configure
`GroupSummary`, `Role`, `Cursor`, `Positional`, and `Example` on the same draft.
Publication copies positional names and example bytes. Positional names must
exist in the final input schema. Examples must decode strictly into the input
type; contextual validation rules and handlers never run during construction.
Custom JSON unmarshaling methods can still run while `mcp.NewTool` checks those
examples; keep those application hooks safe for construction, before any
request-time availability check. Roles describe
diagnostic meaning for clients, not command aliases, authorization, or proof of
read-only behavior.
Cursor follow uses repeated bounded tool calls, not a separate stream.

`Server()` permits advanced SDK use,
but native additions bypass managed validation, safe errors, telemetry, and
CLI metadata. Native tools remain listed without availability checks. Do not
replace managed names or the managed-list middleware through the native accessor;
doing so can bypass execution guards.

See the [API reference](https://pkg.go.dev/arkoren.dev/driver/mcp/mcpserver)
for construction, execution, observability, and compatibility details.
