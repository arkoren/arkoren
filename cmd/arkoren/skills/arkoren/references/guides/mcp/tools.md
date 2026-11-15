---
title: Tools
description: Define typed tool operations, validate their input and output contracts, return safe failures, and test them directly.
order: 20
summary:
  - Define a typed shipping quote with input validation and encoded-output checks.
  - Understand strict schema limits, safe failures, and effects that validation cannot undo.
  - Test a tool definition directly with Tool.Call.
  - Attach CLI presentation metadata without changing the protocol name.
---

# Tools

Expose an operation your application owns as a tool. Keep its typed handler
independent of MCP transport, then publish an immutable definition with
`arkoren.dev/core/mcp`. A definition opens no port and discovers nothing by
itself: the [MCP Server ark](../mcp-server/serving.md) registers the definitions
you select and serves them over HTTP.

The examples use `context` and `encoding/json/jsontext` from the standard
library, plus `arkoren.dev/core/mcp` and `arkoren.dev/foundation/validate`.
Focused fragments omit routine error propagation; complete functions handle
errors.

## Application Operation

A domestic shipping quote needs a parcel weight and an object result. Define
those shapes with explicit JSON names:

```go
type QuoteRequest struct {
	WeightGrams int `json:"weight_grams"`
}

type Quote struct {
	PriceCents int `json:"price_cents"`
}
```

For this sample tariff, parcels through 1000 grams cost 500 cents; heavier
parcels cost 900 cents. Keep the calculation in a typed operation:

```go
func quote(ctx context.Context, input QuoteRequest) (Quote, error) {
	price := 500
	if input.WeightGrams > 1000 {
		price = 900
	}
	return Quote{PriceCents: price}, nil
}
```

This function calculates a quote without booking a shipment or contacting a
carrier. For an operation accessing private records, pass an already-authorized
shared service method instead; [Capability Access](availability-and-authorization.md)
explains why transport checks do not replace resource policy.

## Input Rules

Limit this tariff to parcels from 1 through 5000 grams. Build a schema for the
typed input before declaring the tool:

```go
var input = validate.Struct(func(object *validate.StructBuilder[QuoteRequest]) {
	object.Field("WeightGrams", func(weight *validate.IntBuilder[int]) {
		weight.Min(1)
		weight.Max(5000)
	})
})
```

`Field` selects the Go field `WeightGrams`; clients send `weight_grams`.
An omitted integer decodes as zero and fails this minimum. This input needs no
normalization. If your schema includes normalizers, core applies them during
input parsing before calling the handler.

Publish the handler with that schema:

```go
var shipping = mcp.NewTool("shipping.quote", quote, func(action *mcp.ToolBuilder[QuoteRequest, Quote]) {
	action.Description("Quote domestic shipping in cents for a parcel of 1–5000 grams.")
	action.Input(input)
	action.ReadOnly(true)
	action.OpenWorld(false)
})
```

`NewTool` does not execute the handler or its availability policy. Check `Err()`
when assembling definitions; server construction also checks it. Names contain
1–128 ASCII letters, digits, underscores, dots, or hyphens. Definitions are
immutable and fit together in a heterogeneous `[]mcp.Tool`.

For an available tool, core strictly decodes one object and parses the input
schema once. Nonobjects, malformed JSON, duplicate members, unknown struct
fields, and rule violations prevent handler execution. Nil arguments mean `{}`.
Cancellation is checked before policy evaluation and again before the handler.

Decoding and rule violations wrap `mcp.ErrInvalidInput`. A dependency error from
an input `Check` instead wraps `mcp.ErrInputSchema` and preserves its original
cause. Use `validate.Fail` for caller-correctable Check violations, not outages.
The [typed parser](../validation/overview.md) remains authoritative: advertised
JSON Schema cannot express every normalization or custom check.

## Output Contract

Both argument and result schemas must have an object root. Wrap a scalar or
list in a result struct. Schemas are inferred when omitted; for this quote,
explicitly require a positive price in the emitted JSON:

```go
var output = validate.Struct(func(object *validate.StructBuilder[Quote]) {
	object.Field("PriceCents", func(price *validate.IntBuilder[int]) {
		price.Min(1)
	})
})
```

Add `action.Output(output)` inside the tool's construction callback. A protocol
adapter such as the [MCP Server ark](../mcp-server/serving.md) checks the encoded
result against the published schema. It does not run output
`Parse`, trim values, insert defaults, or execute custom `Check` callbacks. Only
JSON-expressible output rules participate. A successful 1200-gram quote returns
the same object as `structuredContent` and text content:

```json
{
  "price_cents": 900
}
```

Output checks happen after execution. They do not make effects transactional,
and the MCP Server ark never retries the handler. Put transactions and business
idempotency in your service. Pass context to dependencies; cancellation is
cooperative and does not roll back completed effects.

Schema bytes are copied during publication and when returned by `Describe()`.
Editing a report or retaining a construction draft cannot mutate the definition.
Handler captures and schema implementations remain shared and must support
concurrent calls.

### Schema Limits

The MCP Server ark validates emitted output with `validate.FromJSONSchemaStrict`, a bounded validator for
draft-07 and 2020-12. It preserves missing members, `null`, and zero as different
states. Defaults and formats are annotations, not transformations or format
assertions. Local references are supported; external references fail without
network access. Unknown assertion keywords, unsupported types, and unsupported
dialects fail registration rather than receiving a permissive fallback;
recognized `x-*` extensions remain annotations.

Numeric comparisons use exact decimal values: `9007199254740993` exceeds a
maximum of `9007199254740992`, and `1e1000` is valid within the resource bounds.
Precision already lost in a Go `float64` cannot be recovered. Integer fields and
`jsontext.Value` retain numeric precision; numbers decoded into `any` follow
JSON v2's `float64` semantics.

Each schema or emitted document is limited to 8 MiB and 256 container levels.
Numeric coefficients allow 4096 digits, explicit exponents range from -4096
through 4096, and coefficient digits plus absolute exponents have a cumulative
64 Mi-digit budget per decoded document. Equality/uniqueness structural keys
have a cumulative 64 MiB encoding budget per validation. These are data/work
bounds, not exact Go heap limits. Additional node, work, reference, and pointer
limits are specified by
[EncodedSchema.Validate](https://pkg.go.dev/arkoren.dev/foundation/validate#EncodedSchema.Validate).

A validator resource refusal or cancellation produces `IsError=true`, message
`tool failed`, and failure code `internal`, not `invalid_output`. This neither
proves invalid business output nor undoes effects. See
[FromJSONSchemaStrict](https://pkg.go.dev/arkoren.dev/foundation/validate#FromJSONSchemaStrict)
for vocabulary and dialect details.

The MCP Server ark's SDK separately probes numeric literals in both advertised schemas.
For example, a schema containing `"default":1e1000` exceeds that probe's decoder
range and fails construction. The probe does not replace schema bytes or impose
a `float64` range on emitted raw JSON. Client decoders can still round or reject
numbers; see [CLI number precision](../cli/calling-tools.md#number-precision).

## Safe Failures

Annotations describe behavior, not enforcement. `ReadOnly` does not authorize
access, `Idempotent` does not deduplicate calls, and `OpenWorld(false)` does not
restrict networking. Defaults are read-only false, destructive true,
idempotent false, and open-world true.

When a handler has an approved public failure explanation, return a
`mcp.ToolError`. For example, a carrier-backed implementation can decline a
quote without exposing its upstream error:

```go
return Quote{}, mcp.ToolError("shipping quotes are temporarily unavailable")
```

The MCP Server ark returns a failed tool result with that exact approved message,
without wrapping context. Plain ToolError text is capped at 4096 UTF-8 bytes,
truncated at a code-point boundary; empty text stays empty and malformed UTF-8
becomes `tool failed`. Core retains the original error for local inspection.
Never mark credentials, private identifiers, or sensitive existence claims safe.

For approved structured diagnostics, `mcp.ToolFailure` places a code, message,
and optional object details under result `_meta["arkoren.dev/failure"]`, not
successful structured content. The plain text cap is not a general size limit
for structured failures or successful output.

Framework classifications override nested safe markers. Invalid input becomes
`invalid tool input`; encoding failures and output schema violations become
`invalid tool output`. Operational input Check errors, validator resource or
cancellation failures, unmarked handler errors, and recovered panics become
`tool failed`. Availability has its own
[failure behavior](availability-and-authorization.md#evaluation-failures).

Input violations may expose validation pointers and rendered schema messages,
but not violation parameters, input values, or decoder diagnostics. Keep schema
messages free of secrets. Original errors and panic values are logged locally;
protect log storage and access even though client responses are redacted.

## Focused Tests

After verifying an actual MCP connection, test operation behavior directly.
Inside a Go test with `t *testing.T`, call the published `shipping` definition:

```go
result, err := shipping.Call(context.Background(), jsontext.Value(`{"weight_grams":1200}`))
if err != nil {
	t.Fatal(err)
}
if string(result) != `{"price_cents":900}` {
	t.Fatalf("result = %s", result)
}
```

This checks typed decoding, input parsing, and the handler. Add tests for invalid
weights, denied capabilities, cancellation, and resource policy. Direct core
calls preserve error identities and propagate panics; they do not exercise
adapter recovery, redaction, or advertised-output validation. Test those
boundaries with an MCP client against the HTTP handler, as described in
[Testing Over HTTP](../mcp-server/serving.md#testing-over-http).

## CLI Presentation

Optional CLI metadata makes a tool easier to call without renaming its protocol
operation or granting access. Add these settings to the `shipping` declaration:

```go
action.CLI("shipping", "quote")
action.Positional("weight_grams")
action.Example("Quote a 1200-gram parcel", `{"weight_grams":1200}`)
```

The MCP Server ark advertises this under tool `_meta["arkoren.dev/cli"]` in
`tools/list`, the only catalog. Examples are checked for typed JSON compatibility
during construction, not executed as calls or parsed through contextual rules.
The [Arkoren CLI installation](../cli/overview.md#installing-arkoren) and
[calling guide](../cli/calling-tools.md) explain client use. Reports are JSON;
follow output is NDJSON, as detailed in
[Output Formats](../cli/reference.md#output-formats). See
[Failure Diagnostics](../cli/reference.md#failure-diagnostics) for failure records.

The MCP Server ark serializes schemas, CLI metadata, approved failure details,
and raw JSON output without routing their number tokens through `float64`. This
does not guarantee precision after a client decodes those records generically.

For declarations, see the [core MCP API](https://pkg.go.dev/arkoren.dev/core/mcp).

