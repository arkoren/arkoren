---
title: Defining Tools
description: Expose typed application operations with validated arguments, capability policies, and resource authorization.
order: 50
summary:
  - Define typed tool arguments and return only results approved for the model provider.
  - Separate capability discovery from resource authorization inside the handler.
  - Choose model-safe feedback or a permanent failure without exposing backend diagnostics.
---

# Defining Tools

A **tool** is an application function the model can request by name. The model
receives its description and input schema; an [agent run](running-agents.md#adding-tools)
checks the request before calling the typed handler. Treat model arguments as
untrusted input, even when the provider enforces a schema.

The examples use `llm` from `arkoren.dev/core/service/llm`, `validate` from
`arkoren.dev/foundation/validate`, and standard `context` and `errors`. They
assume an application-owned `windows` service. Its `FindAuthorized` method has
signature `func(context.Context, string) (WindowSummary, error)` and checks the
authenticated caller's access to the selected service before returning data.
Neither that service nor its authorization is supplied by Arkoren.

## Defining Inputs

To answer questions about maintenance, let the model select a service by ID.
Keep the input small and describe the field's meaning in the schema:

```go
type WindowQuery struct {
	ServiceID string `json:"service_id"`
}

var windowQuery = validate.Struct(func(object *validate.StructBuilder[WindowQuery]) {
	object.Field("ServiceID", func(id *validate.StringBuilder[string]) {
		id.Required()
		id.Describe("The service identifier supplied by the user.")
	})
})
```

`NewTool` renders this schema for the provider. At execution, it decodes JSON
with json/v2 and calls the schema's `Parse`, applying defaults before the handler.
Decoding failures and validation violations become model-facing error results;
the handler does not run. Keep schema descriptions and validation diagnostics
free of secrets because the provider can receive them.

The OpenAI driver requires JSON-object schema roots and applies mode-specific
[strict-schema rewriting](drivers.md#strict-schemas). Provider enforcement does
not replace application validation or permission checks.

## Returning Results

Return a narrow result instead of a complete internal record. This tool exposes
only the approved schedule fields:

```go
type WindowSummary struct {
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

func findWindow(ctx context.Context, in WindowQuery) (WindowSummary, error) {
	return windows.FindAuthorized(ctx, in.ServiceID)
}
```

`FindAuthorized` must obtain the principal from trusted context, not from a
model-supplied user ID. A valid service ID is not proof of access. Send the
authenticated caller's context into the run and onward to blocking dependencies.

Publish the tool once, normally at package scope:

```go
var lookupWindow = llm.NewTool(
	"lookup-window", "Find the authorized maintenance window for a service.",
	windowQuery, findWindow,
)
```

Strings go to the model unchanged; other results are JSON-encoded. Tools have
no output schema. The result must contain only information the caller, model,
and provider may receive.

Check `lookupWindow.Err()` during construction. Invalid names, nil schemas or
handlers, and schema-rendering failures make the tool unusable; the zero tool
is invalid. Names use 1–64 bytes of lowercase letters, digits, and inner
hyphens. An agent containing an invalid tool fails before provider I/O.

## Offering Capabilities

An account might enable maintenance lookups without granting access to every
service. Use availability to offer that capability, while retaining the
resource check in `FindAuthorized`. Here `canViewMaintenance` is your
observational, concurrency-safe `func(context.Context) (bool, error)`:

```go
var lookupWindow = llm.NewTool(
	"lookup-window", "Find the authorized maintenance window for a service.",
	windowQuery, findWindow, func(tool *llm.ToolBuilder) {
		tool.Available(canViewMaintenance)
	},
)
```

This replaces the earlier declaration. Without a policy, a tool is available;
explicitly supplying a nil policy is a declaration error.

At discovery, `Run` and `RunStream` evaluate each policy once before model I/O,
preserving declaration order. The advertised list remains fixed for that run.
Hidden tools remain guarded in the registry: guessing a name does not bypass
the policy. Execution checks availability again after acquiring a concurrency
slot, before JSON decoding, schema parsing, or the handler. Denial sends only
`tool unavailable` to the model and lets the loop continue.

A newly granted capability appears only in a new run. Revocation blocks later
calls but cannot retract metadata already sent to the provider. Keep sensitive
facts out of names, descriptions, and schemas. `lookupWindow.Available(ctx)`
previews the policy without parsing arguments or invoking the handler; it does
not authorize a specific resource.

A policy error preserves its cause under `llm.ErrToolAvailability` and never
sends its diagnostic to the model, even if it wraps `ToolError`. Discovery
failure prevents model I/O. Execution-policy failure also matches
`llm.ErrPermanent`, cancels siblings, and ends the run. The initiating failure
is not replaced by a sibling's later cancellation.

## Handling Failures

Ordinary handler errors become the generic model-facing text `the tool failed`;
the model can continue or request another call. Use `ToolError` only for feedback
you explicitly approve for publication. For example, when the authorized lookup
returns your application sentinel `ErrNoWindow`:

```go
window, err := windows.FindAuthorized(ctx, in.ServiceID)
if errors.Is(err, ErrNoWindow) {
	return WindowSummary{}, llm.ToolError("No maintenance window is scheduled.")
}
return window, err
```

Do not expose unauthorized resource existence through an error message. Wrapped
`ToolError` text is also visible to the model. Validation feedback follows its
own publication path; an operational schema error is otherwise an ordinary
redacted tool failure, not permission to send its diagnostics.

For a failure that must end the run, return `llm.Permanent(err)`, with a non-nil
cause. The error matches `llm.ErrPermanent`; siblings receive cancellation and
are joined before the run returns. Cancellation does not undo effects or
terminate a noncooperative handler. Follow the [tool panic warning](running-agents.md#canceling-work)
because handlers run in separate goroutines without panic recovery.

## Sharing Definitions

Tool definitions are immutable, but schemas, handlers, policies, and captured
dependencies remain shared. Rendered schema bytes are copied at construction;
retaining a builder cannot change the published tool. Configure shared values
before concurrent use and avoid request-specific state in package-level captures.

For the same operation exposed through LLM and MCP, share the typed handler,
schema, and context-only policy, then declare separate transport tools. There
is no automatic converter. [MCP authorization](../mcp/availability-and-authorization.md)
explains the same distinction between offering a capability and authorizing a
resource.

For tool declarations and errors, see the [llm API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).
