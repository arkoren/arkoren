---
title: Agents and Tools
description: Give a model functions to call, define an agent that runs the tool loop, and control steps, concurrency, and errors.
order: 20
---

# Agents and Tools

## Introduction

A model cannot look up an order or send an email. It can ask your code to: a
reply may be a request to call a **tool**, with arguments the model made up from
the conversation. Your code runs the function and sends the result back, and the
model continues. An **agent** is the definition of that loop: instructions, the
tools it may use, and optionally the type of its final answer.

This is an LLM execution loop inside your service, not an installed coding-agent
skill. It does not expose tools over MCP; an [MCP server](../mcp/first-server.md)
and CLI clients work independently of this loop.

Tools and agents are immutable definitions. Declare them as package-level
variables, configure them through construction callbacks, and run agents with
`Model.Run`. Like [workflows](../workflows/overview.md) and [queue
jobs](../queues/producing-jobs.md), they are values you define once, not objects you
construct per request.

The examples use `llm` from `arkoren.dev/core/service/llm`, `validate` from
`arkoren.dev/foundation/validate`, and the standard packages `context`, `errors`,
and `fmt`. In calling code, use the `model` and `ctx` from the
[overview setup](overview.md#creating-a-model); `history` is your stored
`[]llm.Message`, or a new slice containing `llm.UserText` for the current question.

## Defining a tool

A support agent needs your application's data to answer an order question.
Expose the lookup as a tool: its name and description tell the model when to
request it, while a [`validate` schema](../validation/overview.md) checks the
arguments before your function runs. Here `orders` is your application's order
repository and `Order` is its result type; neither is supplied by Arkoren:

```go
type OrderArgs struct {
	Number int `json:"number"`
}

var orderArgs = validate.Struct(func(object *validate.StructBuilder[OrderArgs]) {
	object.Field("Number", func(number *validate.IntBuilder[int]) {
		number.Required()
		number.Min(1)
		number.Describe("The order number, printed on the receipt.")
	})
})
```

Pass the schema and a typed handler to `NewTool`:

```go
func findOrder(ctx context.Context, in OrderArgs) (Order, error) {
	return orders.Find(ctx, in.Number)
}

var lookupOrder = llm.NewTool("lookup-order", "Find an order by number.", orderArgs, findOrder)
```

The same schema does two jobs. The model receives it as JSON Schema, with the
`Describe` text as the description of each argument, and the arguments the model
sends are decoded and checked against it before your function runs. Arguments
that fail the schema never reach your code: the model gets the validation
messages back as an error result and usually corrects itself.

Keep validation messages and descriptions safe to send to the model provider;
custom rules must not include secrets in their rendered errors.

The handler's result is returned to the model as text or JSON. Return only the
fields needed for the answer; tools do not have an output schema. `lookupOrder.Err()`
reports an invalid declaration before a run can use it; the
[`Tool` API reference](https://pkg.go.dev/arkoren.dev/core/service/llm#Tool)
describes declaration validation and ownership.

Validation checks argument shape, not permission. Authorize access inside your
tool using the authenticated caller's context; a model-supplied order number
is not evidence that the caller may read that order. Return only fields the
model and its provider may receive.

## Offer tools by capability

A role or paid plan may unlock order tools without granting access to every
order. Use availability to decide whether to offer a capability, and keep
resource authorization inside `findOrder`. Here `permissions.CanLookupOrders`
is your application's context-based rule, with signature
`func(context.Context) (bool, error)`:

```go
var lookupOrder = llm.NewTool("lookup-order", "Find an order by number.", orderArgs, findOrder,
	func(action *llm.ToolBuilder) {
		action.Available(permissions.CanLookupOrders)
	})
```

This replaces the earlier `lookupOrder` declaration. Without a policy the tool
is available. Policies must be observational and concurrency-safe.
Pass the authenticated caller's context into each run and into policy lookups.

### Discovery and revocation

`Run` and `RunStream` evaluate each tool's availability once before model I/O,
preserving declaration order in the advertised list. That list stays fixed for
the run. Hidden tools remain in the guarded registry, so guessing a name cannot
bypass the policy. Each attempted execution checks availability again after
acquiring its concurrency slot, before JSON decoding, schema `Parse`, or the
handler. A denial returns only `tool unavailable` to the model and permits the
loop to continue.

A grant during a run does not add a tool to the advertised list; start a new run
to discover it. A revocation blocks subsequent calls, but cannot retract names,
descriptions, or schemas already sent to the provider. Keep sensitive information
out of that metadata.

If you need your own capability preview, call `lookupOrder.Available(ctx)`.
It checks the policy without parsing arguments or running the handler. A policy
error preserves its cause under `llm.ErrToolAvailability`, even if it wraps a
`ToolError`; its diagnostic is never sent to the model. Discovery failures stop
before model I/O. An execution policy failure also matches `llm.ErrPermanent`,
cancels sibling calls, and ends the run. The initiating failure remains the
returned cause; a sibling's later cancellation error does not replace it.

For an operation exposed through both LLM and MCP, share the typed handler,
`validate` schema, and context-only availability function. Declare separate
tools for each transport; there is no tool converter and LLM core does not
import MCP. The [MCP authorization guide](../mcp/availability-and-authorization.md)
shows how to share application rules while keeping resource checks in the service.

## Defining an agent

An agent bundles instructions and tools:

```go
var support = llm.NewAgent("support", func(agent *llm.AgentBuilder[string]) {
	agent.Instructions("You help customers with their orders. Look orders up before answering.")
	agent.Tools(lookupOrder)
})
```

`support.Err()` reports invalid names, invalid or duplicate tools, and output
schema declaration errors. A zero agent is invalid. Construction publishes
independent tool containers and rendered schema bytes; schema implementations,
handlers, policies, and their captures remain shared. Configure those shared
dependencies before using the definition concurrently.

Run it against a conversation, with the same `Model` that serves everything
else:

```go
result, err := model.Run(ctx, support, []llm.Message{
	llm.UserText("Where is order 4012?"),
})
fmt.Println(result.Text)
```

`Run` calls the model, checks and executes requested tools, appends their results,
and calls the model again until it answers without asking for a tool or the run
reaches a limit or fails. Availability and argument validation still guard each
tool call; a request from the model does not bypass them.
`result.Steps` counts the model turns, and `result.Usage` sums their tokens. The
type parameter is the type of the final answer: `string` for text, or a struct
for [structured output](structured-output.md).

### Per-call options

Options vary one run without changing the definition. To add caller-specific
instructions, use `model` from [the overview](overview.md#creating-a-model) and
your stored `history`:

```go
result, err := model.Run(ctx, support, history, llm.Instructions("The customer is Ana."))
```

`llm.Instructions` appends to the agent's instructions. It changes the start of
the prompt, which can reduce reuse of the vendor's prompt cache, so keep stable text in
the agent and per-user facts in a message where you can.

Use `llm.UseModel` when one run needs another model, or `llm.MaxSteps` to set
that run's turn budget; neither changes the shared definition.

## Errors a tool returns

A tool error does not stop the run. The model receives an error result, so it can
try again or tell the user. To protect your internals, the model sees only the
text "the tool failed" for an ordinary error. Wrap `llm.ToolError` around a
message you want it to read. Here `ErrOrderNotFound` is the sentinel returned
by your application's order repository:

```go
order, err := orders.Find(ctx, in.Number)
if errors.Is(err, ErrOrderNotFound) {
	return Order{}, llm.ToolError("no order with that number")
}
return order, err
```

Return `llm.Permanent(err)` for a failure that should end the run, such as a
database that is down. `Run` then cancels the tool calls still running and
returns the error, matching `llm.ErrPermanent`.

Cancellation signals the sibling calls' contexts; it does not forcibly stop
their functions or undo effects already performed. `Run` waits for those calls
to finish, so pass the context to blocking dependencies and make handlers honor
cancellation.

> [!WARNING]
> Panics in a tool function are not recovered. Tool calls run in their own
> goroutines, so HTTP recovery middleware around `Run` cannot catch them; an
> unrecovered panic terminates the process. Return errors for expected failures,
> and recover inside the tool if a library it calls can panic.

## Parallel tool calls

A model can ask for several tools in one reply. They run concurrently, and
their results are returned in the order the model asked for them. Limit the
concurrency with `ToolConcurrency(n)` on the agent: `1` runs the calls one at a
time, and the default `0` is unlimited. A tool that shares a connection or an
external rate limit should usually set it.

## Step limits and conversations

Each model turn is a step. The limit is the run option, then the agent's
`MaxSteps`, then the model's `llm.ModelBuilder.MaxSteps`, and finally 10. When the model
still asks for tools on the last allowed step, `Run` answers those calls with an
error result without running them and returns `llm.ErrMaxSteps`.

After the first model turn, `Run` returns the `Result` together with any error,
so a failed run still gives you the messages and usage so far:

```go
result, err := model.Run(ctx, support, history)
if err != nil && !errors.Is(err, llm.ErrMaxSteps) {
	return err
}
history = result.Messages
```

After `ErrMaxSteps`, `result.Messages` includes error results for the unexecuted
tool calls and can be used to resume. Other failures can leave a partial turn;
inspect that history before continuing.
[Language Models Overview](overview.md#running-an-agent) shows storing history.

## Events and telemetry

An observed run produces an `invoke_agent` span, with a `chat` span per turn and
an `execute_tool` span for each known tool call admitted to execution. With
`llm.ModelBuilder.Events`, a returning observed run publishes `llm.agent.completed` or
`llm.agent.failed`, and observed tool outcomes publish `llm.tool.invoked` or
`llm.tool.failed`. Invalid definitions and discovery-policy failures return
before the agent observation begins. Payloads hold names, counts, and durations,
never arguments or results; agent failures expose only the bounded
[`ErrorType` classification](overview.md#usage-events-and-telemetry).

Consumer stops and panics do not invent an agent outcome event. A panic still
propagates, while any observation it unwinds is finalized without recording
panic data. Already completed tool observations remain published. See
[stopping a stream](streaming.md#stopping-a-stream) for cancellation and joining
admitted tool calls.

To show progress while an agent works, use `RunStream`; see [Streaming
Replies](streaming.md).

For every function and option, see [the `llm` API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).
