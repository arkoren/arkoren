---
title: Structured Output
description: Make an agent return a typed Go value validated against a schema, with retries on invalid output.
order: 30
---

# Structured Output

## Introduction

Suppose you want to route support tickets by category and flag the urgent ones.
A paragraph from the model is useful to a person, but your routing code needs
fields it can read without interpreting prose. Structured output asks the model
for JSON that matches a schema, then decodes and
validates it into a Go value. You describe the shape once, with the same
[`validate` schemas](../validation/overview.md) you use for requests, and the agent
returns that type.

Reuse `model` and `ctx` from the [overview setup](overview.md#creating-a-model).
Import `llm` from `arkoren.dev/core/service/llm`, `validate` from
`arkoren.dev/foundation/validate`, and `fmt` from the standard library. In the
calling examples, `body` is the support-ticket text your application may send
to the model provider.

## Returning a typed value

Start with the fields your ticket-routing code needs:

```go
type Ticket struct {
	Category string `json:"category"`
	Urgent   bool   `json:"urgent"`
	Summary  string `json:"summary"`
}
```

Describe the model's output constraints with a typed schema:

```go
var ticketSchema = validate.Struct(func(object *validate.StructBuilder[Ticket]) {
	object.Field("Category", func(category *validate.StringBuilder[string]) {
		category.OneOf("billing", "bug", "question")
		category.Required()
		category.Describe("What the ticket is about.")
	})
	object.Field("Urgent", func(urgent *validate.BoolBuilder[bool]) {
		urgent.Describe("True when the customer cannot work.")
	})
	object.Field("Summary", func(summary *validate.StringBuilder[string]) {
		summary.Required()
		summary.Max(140)
	})
})
```

Attach that schema to the agent:

```go
var triage = llm.NewAgent("triage", func(agent *llm.AgentBuilder[Ticket]) {
	agent.Instructions("Classify the support ticket.")
	agent.Output(ticketSchema)
})
```

Use the `model` created in [the overview](overview.md#creating-a-model), with
the ticket text in `body`. `Run` sends the schema to the vendor, decodes the
final reply with json/v2, and validates it with the schema, applying its defaults:

```go
result, err := model.Run(ctx, triage, []llm.Message{llm.UserText(body)})
fmt.Println(result.Output.Category, result.Output.Urgent)
```

`result.Text` still holds the raw reply. An agent with a non-string type
parameter needs `Output`; without it, `Err()` and `Run` report the mistake.
Only the exact `string` type needs no schema; a named string type needs one too.
For schema publication and declaration errors, see
[defining an agent](agents-and-tools.md#defining-an-agent).

Use `Describe` to explain each field's meaning alongside its constraints. This
keeps that explanation in the schema sent to the model rather than relying only
on the agent's instructions.
Rules that JSON Schema cannot express, such as comparisons between fields, are
not sent to the model but are still enforced when the reply is validated.

## When the output is invalid

Models sometimes return JSON that parses but breaks a rule, for example a
summary that is too long. `Run` and `RunStream` retry JSON decoding failures and
schema violations returned directly as `validate.Errors`. They append the
diagnostic as a new user message and ask again, up to `OutputRetries` times,
which defaults to 1. A `Check` callback reports a correctable violation with
`validate.Fail`.

These correction messages become part of the saved conversation and are sent
to the model provider. Keep custom rule messages free of secrets or internal
diagnostics. To allow two retries, replace the earlier definition with:

```go
var triage = llm.NewAgent("triage", func(agent *llm.AgentBuilder[Ticket]) {
	agent.Output(ticketSchema)
	agent.OutputRetries(2)
})
```

Each retry is another step, so it counts against `MaxSteps`. When the retries
run out, `Run` returns `llm.ErrInvalidOutput` with the decoding or validation
error in the chain, and the `Result` holds the conversation so far. Zero selects
the default of one retry; a negative value never retries. Exhausting `MaxSteps`
first returns `llm.ErrMaxSteps` instead.

Other final-output schema errors stop the run immediately: operational `Check`
failures, cancellation, and errors that wrap rather than directly return
`validate.Errors` are not correction feedback. Their causes remain in the
returned error, without adding `llm.ErrInvalidOutput`, consuming an output retry, or appending diagnostics
to `Result.Messages` or a subsequent provider request. Failure events report these
errors through a [bounded classification](overview.md#usage-events-and-telemetry)
instead of including their diagnostics.

## Optional fields and strict mode

Keep optional fields optional in your portable schema. The OpenAI driver adapts
them for the provider's strict mode; a returned `null` decodes to the Go zero
value, which `validate` treats as absent. If you need open-ended maps, check the
[strict-schema constraints](drivers.md#strict-schemas)
before choosing the provider and mode.

Strict mode changes the schema sent to the provider, not the application's
validation boundary: the agent still decodes and validates the final reply.
If the server rejects the structured-output format, the OpenAI driver returns
`llm.ErrUnsupported`; output retries do not provide a prompt-based fallback.

## Structured output without an agent

If you already manage model calls yourself and need the raw response, you can
request structured output without an agent. Set `Request.Output` on a single
`Generate` call, using `JSONSchema()` to render your validation schema:

```go
schema, err := ticketSchema.JSONSchema()
response, err := model.Generate(ctx, llm.Request{
	Messages: []llm.Message{llm.UserText(body)},
	Output:   schema,
})
```

You then decode and validate the text yourself. Prefer an agent unless you need
the raw response.

For every function and option, see [the `llm` API reference](https://pkg.go.dev/arkoren.dev/core/service/llm)
and [the `validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
