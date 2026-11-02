---
title: Structured Output
description: Decode a model's final answer into a validated Go value and control correction attempts.
order: 60
summary:
  - Describe typed output with a validation schema and read the accepted result.
  - Distinguish correctable replies from operational validation failures.
  - Account for strict-schema limits and validate single-turn output yourself when bypassing an agent.
---

# Structured Output

A maintenance notice may describe a service and an outage duration in prose,
while your application needs fields it can compare or store. Structured output
asks the model for JSON, then decodes and validates the final reply into a Go
value. The provider's schema support guides generation; application validation
still decides whether to accept the result.

Use a configured `model` and operation context `ctx` from [Drivers](drivers.md).
The examples import `llm` from `arkoren.dev/core/service/llm`, `validate` from
`arkoren.dev/foundation/validate`, and standard `fmt`. Here `notice` is text your
application has approved for the provider.

## Defining Output

Start with the fields your application actually consumes:

```go
type Maintenance struct {
	Service string `json:"service"`
	Minutes int    `json:"minutes"`
}
```

Describe their meaning and allowed values using a
[typed validation schema](../validation/overview.md):

```go
var maintenanceSchema = validate.Struct(func(object *validate.StructBuilder[Maintenance]) {
	object.Field("Service", func(service *validate.StringBuilder[string]) {
		service.Required()
		service.Describe("The service affected by the notice.")
	})
	object.Field("Minutes", func(minutes *validate.IntBuilder[int]) {
		minutes.Required()
		minutes.Min(1)
		minutes.Describe("The stated outage duration in minutes.")
	})
})
```

`Describe` text travels with the schema to the model. Rules JSON Schema cannot
express, such as cross-field checks, are not sent but still run during local
validation. Do not mistake a valid shape for factual accuracy or permission to
perform an action.

## Running Validation

Attach the schema to an immutable agent definition:

```go
var extractMaintenance = llm.NewAgent("extract-maintenance", func(agent *llm.AgentBuilder[Maintenance]) {
	agent.Instructions("Extract only the service and duration stated in the notice.")
	agent.Output(maintenanceSchema)
})
```

Run the agent with the notice as a user message:

```go
result, err := model.Run(ctx, extractMaintenance, []llm.Message{llm.UserText(notice)})
fmt.Println(result.Output.Service, result.Output.Minutes)
```

On success, `Output` is the decoded value after schema defaults and validation.
`Text` remains the raw final reply. On failure, `Output` stays zero; inspect
the error before consuming fields. Only the exact `string` type can omit an
output schema; named string types also need one.

Check `extractMaintenance.Err()` at application construction. Invalid or
unrenderable schemas fail before provider I/O. Rendered bytes are copied, but
the schema implementation remains shared; follow [definition ownership](running-agents.md#defining-agents)
before using it concurrently.

## Correcting Replies

If final JSON cannot decode or validation directly returns `validate.Errors`,
the agent appends a user message explaining the violation and asks again.
Custom `Check` callbacks use `validate.Fail` for such correctable violations.
Feedback enters both saved history and a subsequent provider request, so keep
custom diagnostics free of secrets and internal details.

By default, one correction is allowed. To allow two, add this to the existing
agent construction callback:

```go
agent.OutputRetries(2)
```

Zero selects the default of one retry; a negative value disables retries.
Each correction attempt takes another model turn and counts against
[MaxSteps](running-agents.md#limiting-runs). When retries run out, the error
matches `llm.ErrInvalidOutput` and preserves the decoding or validation cause.
The returned result retains history so far. If the step limit runs out first,
the error is `llm.ErrMaxSteps`; correction feedback may already be in history.

Operational schema errors are different. A failing dependency in `Check`,
cancellation, or an error wrapping rather than directly returning
`validate.Errors` stops output processing immediately. Its cause remains
available without adding `llm.ErrInvalidOutput`, consuming an output retry, or
appending diagnostics to history. Failure events expose only the
[bounded classification](overview.md#observing-calls), not the diagnostic.

These retries correct output; they do not retry provider failures. Truncation
and refusal stop the run before output decoding.

## Schema Limits

Keep optional fields optional in the portable schema. The OpenAI driver's
[strict rewriting](drivers.md#strict-schemas) makes optional properties required
but nullable; returned null decodes to Go zero, which validation treats as
absent. Strict mode changes the transmitted schema, not local validation.

OpenAI tool and output schema documents must themselves be JSON objects in
either mode; raw JSON arrays, scalars, and null fail locally before the request
hook or HTTP request. This is not a promise that every schema expressed as an
object is supported by the server. Open maps without properties are
not closed by strict rewriting and do not meet strict-object requirements.
Turning rewriting off does not establish server support for those schemas.

If a server rejects the structured-output format, the driver returns
`llm.ErrUnsupported`. There is no prompt-based fallback, and output correction
does not repair unsupported provider features.

## Single-Turn Output

If your application manages turns itself, request the schema directly without
an agent:

```go
schema, err := maintenanceSchema.JSONSchema()
reply, err := model.Generate(ctx, llm.Request{
	Messages: []llm.Message{llm.UserText(notice)},
	Output:   schema,
})
```

`reply` is still an `llm.Response`, not a validated `Maintenance`. Check its
stop reason, collect text, decode, and validate it yourself. Prefer the agent
when you want Arkoren to own that acceptance and correction loop.

For all options, see the [llm API reference](https://pkg.go.dev/arkoren.dev/core/service/llm)
and [validate API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
