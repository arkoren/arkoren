// Package llm provides a vendor-neutral contract for large language models and
// a facade for turns, streaming, and embeddings.
//
// # Concepts
//
// A [Driver] translates one [Request] into one vendor call and nothing else:
// it applies no defaults, retries nothing and keeps nothing. The facades add
// the rest. [Model] ([New]) fills the model ID and token limit, validates the
// request, and runs [Model.Generate] or [Model.Stream]; [Embedder]
// ([NewEmbedder]) runs [Embedder.Embed].
//
// A conversation is a slice of [Message] values, each a [Role] plus [Part]
// values: [Text], [Image], [Document], [ToolCall], [ToolResult] and
// [Reasoning]. System text lives in [Request.Instructions], and tool results
// travel as parts of a user message. A [Message] encodes to JSON with a "type"
// tag on every part, so history an application saves round-trips.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.MaxTokens(4096)
//	})
//	response, err := model.Generate(ctx, llm.Request{
//		Instructions: "Answer in one sentence.",
//		Messages:     []llm.Message{llm.UserText("Why is the sky blue?")},
//	})
//
//	for event, err := range model.Stream(ctx, request) {
//		if err != nil {
//			return err
//		}
//		if delta, ok := event.(llm.TextDelta); ok {
//			fmt.Print(delta.Text)
//		}
//	}
//
// Stream and RunStream return collection.TryLazySlice[Event], with native
// Filter, Map, Take and Items operations. Construction does no I/O. Each
// traversal starts new provider work, potentially billed again, rather than
// replaying earlier events. Use Iter for standard iterator consumers:
//
//	events := model.Stream(ctx, request)
//	for item, err := range events.Iter() {
//		if err != nil {
//			return err
//		}
//		fmt.Println(item)
//	}
//
// Items retains the successful prefix on error, not a completed response.
// Filtering Done does not disable source completion validation. Reaching a Take
// limit is an early consumer stop. Materialization requires a finite stream;
// JSON encoding materializes events and is not a network-streaming transport.
//
// # Agents and tools
//
// A [Tool] ([NewTool]) pairs a function with a [validate.Schema] of its
// arguments, and an [Agent] ([NewAgent]) bundles instructions, tools and an
// optional output schema. Both are immutable definitions, normally
// package-level variables, and report the first invalid input from Err.
// [Model.Run] drives the loop: it calls the model, decodes and validates each
// tool call, runs the tools (concurrently within a turn, unless
// [AgentBuilder.ToolConcurrency] says otherwise), sends the results back and repeats
// until the model ends its turn or the step limit ([ModelBuilder.MaxSteps],
// [AgentBuilder.MaxSteps], [MaxSteps]) is reached. [Model.RunStream] yields the same
// loop as [Event] values. A tool shows the model only the text of a
// [ToolError]; any other error becomes "the tool failed", and [Permanent]
// stops the run.
//
//	var lookup = llm.NewTool("lookup-order", "Find an order.", orderArgs, findOrder)
//	var triage = llm.NewAgent[Ticket]("triage", func(agent *llm.AgentBuilder[Ticket]) {
//		agent.Tools(lookup)
//		agent.Output(ticketSchema)
//	})
//
// NewAgent callbacks share one draft and run once in order. Settings return
// that draft; publication detaches owned containers and rendered schema bytes.
// A retained draft cannot alter the Agent. Tools replaces earlier tools and
// the first invalid input stays invalid. Nil callback entries panic; omitted
// callbacks and expanded nil or empty slices leave the defaults unchanged.
//
//	result, err := model.Run(ctx, triage, []llm.Message{llm.UserText(text)})
//
// A fatal tool or capability-policy failure cancels sibling calls; its original
// cancellation cause is preserved even if another handler returns a permanent
// cancellation error afterward. The run joins admitted calls. Cancellation is
// cooperative: a handler that ignores its context can keep that join waiting.
// RunStream emits ToolStarted only after concurrency admission. A consumer stop
// cancels and joins already admitted calls without announcing queued calls.
//
// Model.Stream consumer breaks, including a break at Done, publish no response
// event. Let iteration finish normally when the completed-turn event is needed.
// Synchronous callback and consumer panics propagate unchanged; started spans
// close with the bounded class "llm.interrupted", without retaining panic values
// or publishing an invented success/failure event. Tool goroutine panics are not
// recovered or converted into model feedback.
//
// Errors a vendor reports map onto the package sentinels such as
// [ErrRateLimited] and [ErrContextLength], with the original error kept in the
// chain, so retry and fallback code matches on [errors.Is] for any vendor.
//
// # Telemetry
//
// With [ModelBuilder.Telemetry], or [telemetry.Default] when it is not set, each
// [Model.Generate] and [Model.Stream] emits a client span "chat {model}", and
// each [Embedder.Embed] a span "embeddings {model}". Both carry the
// OpenTelemetry generative AI attributes (gen_ai.*), and the
// gen_ai.client.operation.duration and gen_ai.client.token.usage metrics are
// recorded. The conventions are pinned to schema gen-ai/1.42.0; attributes
// without a gen-ai name use the arkoren.llm namespace. Prompts, outputs, tool
// arguments and error text are never recorded. Disabled telemetry costs
// nothing.
//
// [Model.Run] adds an "invoke_agent {agent}" span per run, parent of the
// "chat {model}" span of each turn and of an "execute_tool {tool}" span per
// tool call, with the arkoren.llm.agent.steps attribute. Tool arguments and
// results are never recorded.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.Telemetry(app.Telemetry())
//	})
//
// New and NewEmbedder accept ModelBuilder and EmbedderBuilder callbacks with
// only their applicable defaults. Callbacks run directly once in order on a
// fresh draft; nil entries panic and nil/empty slice expansion omits edits.
// Config getters return independent reporting records, not constructor inputs.
// Published roots detach from retained drafts; drivers and services stay shared.
//
// # Events
//
// With [ModelBuilder.Events] or [EmbedderBuilder.Events], the facades publish [ResponseGenerated] and
// [ResponseFailed] ("llm.response.generated", "llm.response.failed") for
// [Model.Generate] and completed [Model.Stream] calls, and
// [EmbeddingsGenerated] and [EmbeddingsFailed] for [Embedder.Embed]. Payloads
// carry the model, counts, usage and duration, never content or raw errors.
// Failure ErrorType fields contain a package sentinel name, context.Canceled,
// context.DeadlineExceeded, or "unknown", never error messages or native type
// names. Returned errors preserve original causes; do not forward them to logs
// or clients without applying your application's privacy policy. [Model.Run]
// publishes [AgentCompleted] and [AgentFailed] per run and [ToolInvoked]
// ("llm.tool.invoked") and [ToolFailed] per tool call; the turns inside a
// run publish no response events.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.Events(app.Events())
//	})
package llm
