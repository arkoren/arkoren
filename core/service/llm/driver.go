package llm

import (
	"context"
	"errors"
	"iter"
)

// Errors reported by drivers and facades. A driver maps vendor errors onto
// them with the original error kept in the chain, and wrapped errors are
// matched with [errors.Is].
var (
	// ErrInvalidRequest means the request is malformed: a nil context or
	// driver, a missing model or messages, or an unknown tool choice.
	//
	//	if errors.Is(err, llm.ErrInvalidRequest) {
	//		return fmt.Errorf("correct the model request: %w", err)
	//	}
	ErrInvalidRequest = errors.New("llm: invalid request")

	// ErrUnsupported means the driver cannot do the operation, part type or
	// setting.
	// Choose a compatible provider rather than automatically retrying.
	//
	//	if errors.Is(err, llm.ErrUnsupported) {
	//		return fmt.Errorf("provider lacks this capability: %w", err)
	//	}
	ErrUnsupported = errors.New("llm: unsupported")

	// ErrNotFound means the vendor does not know the model.
	// It does not describe an application resource requested by a tool.
	//
	//	if errors.Is(err, llm.ErrNotFound) {
	//		return fmt.Errorf("check the configured model ID: %w", err)
	//	}
	ErrNotFound = errors.New("llm: model not found")

	// ErrRateLimited means the vendor rate-limited the call. Retrying later
	// may succeed.
	// Keep any application retry bounded and cancellation-aware.
	//
	//	if errors.Is(err, llm.ErrRateLimited) {
	//		return fmt.Errorf("model capacity is temporarily limited: %w", err)
	//	}
	ErrRateLimited = errors.New("llm: rate limited")

	// ErrUnavailable means the vendor is overloaded or returned a server
	// error. Retrying later may succeed.
	// This is a provider failure, not a tool capability denial.
	//
	//	if errors.Is(err, llm.ErrUnavailable) {
	//		return fmt.Errorf("provider is temporarily unavailable: %w", err)
	//	}
	ErrUnavailable = errors.New("llm: unavailable")

	// ErrContextLength means the input exceeds the model's context window.
	// Retrying the same history does not reduce the input size.
	//
	//	if errors.Is(err, llm.ErrContextLength) {
	//		return fmt.Errorf("shorten the conversation before retrying: %w", err)
	//	}
	ErrContextLength = errors.New("llm: context length exceeded")

	// ErrRefused means the model refused, or a content filter blocked the
	// reply.
	// Generate reports the raw stop reason; Run promotes refusal to this error.
	//
	//	if errors.Is(err, llm.ErrRefused) {
	//		return fmt.Errorf("agent could not answer this request: %w", err)
	//	}
	ErrRefused = errors.New("llm: refused")

	// ErrTruncated means the output hit the token limit.
	// A partial structured reply is not a validated agent output.
	//
	//	if errors.Is(err, llm.ErrTruncated) {
	//		return fmt.Errorf("agent output was incomplete: %w", err)
	//	}
	ErrTruncated = errors.New("llm: output truncated")

	// ErrMaxSteps means [Model.Run] reached its step limit before the model
	// ended its turn.
	// Tools requested on the final allowed turn are not executed.
	//
	//	if errors.Is(err, llm.ErrMaxSteps) {
	//		return fmt.Errorf("agent exhausted its turn budget: %w", err)
	//	}
	ErrMaxSteps = errors.New("llm: step limit reached")

	// ErrInvalidOutput means the agent's final reply failed decoding or
	// validation against its output schema, after the allowed retries. The
	// cause stays in the chain.
	// Operational schema errors are returned directly, never as model feedback.
	//
	//	if errors.Is(err, llm.ErrInvalidOutput) {
	//		return fmt.Errorf("agent did not produce validated output: %w", err)
	//	}
	ErrInvalidOutput = errors.New("llm: invalid output")

	// ErrUnknownTool means the model called a tool the agent does not have.
	// The model is told so and may recover; the error is only reported when
	// the run then fails.
	// A recovered unknown call does not by itself fail a successful run.
	//
	//	if errors.Is(err, llm.ErrUnknownTool) {
	//		return fmt.Errorf("review the agent's tool definitions: %w", err)
	//	}
	ErrUnknownTool = errors.New("llm: unknown tool")

	// ErrToolUnavailable means capability policy denied tool execution. It
	// differs from provider ErrUnavailable: the model receives only the fixed
	// error result "tool unavailable", and the handler is not invoked.
	//
	// Example:
	//
	//	if errors.Is(err, llm.ErrToolUnavailable) {
	//		fmt.Println("tool access denied")
	//	}
	ErrToolUnavailable = errors.New("llm: tool unavailable")

	// ErrToolAvailability classifies capability-policy errors, preserving their
	// original cause under errors.Is and errors.As. Discovery errors abort
	// before model I/O; execution errors also match ErrPermanent and cancel
	// sibling calls. Their text is never a model-facing tool result.
	//
	// Example:
	//
	//	if errors.Is(err, llm.ErrToolAvailability) {
	//		return fmt.Errorf("evaluate tool permissions: %w", err)
	//	}
	ErrToolAvailability = errors.New("llm: tool availability failed")

	// ErrInvalidName means an agent or tool name breaks the naming rules:
	// 1-64 bytes of lowercase letters, digits and inner hyphens.
	// The zero Agent and zero Tool are invalid definitions too.
	//
	//	definition := llm.NewAgent[string]("Invalid")
	//	if errors.Is(definition.Err(), llm.ErrInvalidName) {
	//		return definition.Err()
	//	}
	ErrInvalidName = errors.New("llm: invalid name")

	// ErrPermanent matches, under [errors.Is], errors created by
	// [Permanent], including capability-policy failures during tool execution.
	// Discovery policy errors match ErrToolAvailability without ErrPermanent.
	//
	// Example:
	//
	//	if errors.Is(err, llm.ErrPermanent) {
	//		return err // Do not retry this tool turn.
	//	}
	ErrPermanent = errors.New("llm: permanent tool failure")
)

// Driver performs one model turn or one embeddings request against a vendor.
//
// Drivers translate and do nothing else: they do not apply [Config] defaults
// (the facades fill [Request.Model] and [Request.MaxTokens]), retry, or retain
// the request after a call returns, and they must not modify the request or
// the values it references. Every operation lives here; a vendor that cannot
// do one returns [ErrUnsupported]. There are no capability interfaces.
//
// # Forward compatibility
//
// A driver returns [ErrUnsupported] for any [Part] type it does not know and
// ignores [Request] fields it does not use, so later additions such as
// sampling settings or hosted tools never break an existing driver.
//
// # Errors
//
// Vendor errors map onto the package sentinels ([ErrRateLimited],
// [ErrUnavailable], [ErrContextLength], [ErrNotFound], [ErrInvalidRequest],
// [ErrUnsupported]) with the original error kept in the chain. A reply that
// the model refused or a filter blocked is a successful call with
// [StopRefusal] or [StopContentFilter], not an error.
//
// A [Reasoning] part's Signature is opaque and is sent back to the vendor
// unchanged.
//
//	model := llm.New(driver, "gpt-5.2")
//	response, err := model.Generate(ctx, llm.Request{
//		Messages: []llm.Message{llm.UserText("Explain in one sentence.")},
//	})
//	if err != nil {
//		return err
//	}
//	_ = response.Usage
type Driver interface {
	// Generate performs one model turn and returns the complete assistant
	// message.
	Generate(ctx context.Context, request Request) (Response, error)

	// Stream performs one model turn and yields its events in order. The last
	// event of a successful stream is [Done] carrying the complete [Response].
	// An error ends the stream and is yielded as the second value. Breaking
	// out of the loop cancels the vendor call.
	Stream(ctx context.Context, request Request) iter.Seq2[Event, error]

	// Embed returns one vector per input, in input order. The driver splits
	// the inputs into vendor-sized batches and sums their usage.
	Embed(ctx context.Context, request EmbedRequest) (Embeddings, error)
}

// ToolChoiceKind selects how the model may use the tools of a [Request].
// Empty input selects ToolChoiceAuto; unknown nonempty values are rejected by
// the model facade before it calls a driver. JSON uses the string value.
//
// Example: _ = llm.ToolChoice{Kind: llm.ToolChoiceRequired}
type ToolChoiceKind string

const (
	// ToolChoiceAuto lets the model decide whether to call a tool. It is the
	// default for an empty Kind.
	//
	// Example: _ = llm.ToolChoice{Kind: llm.ToolChoiceAuto}
	ToolChoiceAuto ToolChoiceKind = "auto"

	// ToolChoiceNone forbids tool calls.
	//
	// Example: _ = llm.ToolChoice{Kind: llm.ToolChoiceNone}
	ToolChoiceNone ToolChoiceKind = "none"

	// ToolChoiceRequired makes the model call some tool.
	//
	// Example: _ = llm.ToolChoice{Kind: llm.ToolChoiceRequired}
	ToolChoiceRequired ToolChoiceKind = "required"

	// ToolChoiceNamed makes the model call the tool named by
	// [ToolChoice.Name].
	//
	// Example: _ = llm.ToolChoice{Kind: llm.ToolChoiceNamed, Name: "weather"}
	ToolChoiceNamed ToolChoiceKind = "named"
)

// ToolChoice constrains tool use for one [Request]. The zero value is
// automatic.
// Named choices are validated against Request.Tools before a provider call.
//
//	request := llm.Request{
//		Messages: []llm.Message{llm.UserText("Answer without tools.")},
//		ToolChoice: llm.ToolChoice{Kind: llm.ToolChoiceNone},
//	}
//	_ = request
type ToolChoice struct {
	// Kind is the constraint.
	Kind ToolChoiceKind

	// Name is the tool to call when Kind is [ToolChoiceNamed]. It must name
	// an entry of [Request.Tools].
	Name string
}

// ToolSpec describes a tool the model may call.
// This is raw request data, not an executable or authorized Tool. Model.Run
// obtains specs from validated agent tools after capability filtering.
//
//	spec := llm.ToolSpec{
//		Name: "lookup-order", Description: "Find an order.",
//		InputSchema: []byte(`{"type":"object","properties":{}}`),
//	}
//	request.Tools = []llm.ToolSpec{spec}
type ToolSpec struct {
	// Name identifies the tool in [ToolCall] parts.
	Name string

	// Description tells the model when to use the tool.
	Description string

	// InputSchema is the JSON Schema of the tool's arguments.
	InputSchema []byte
}

// Effort is how much reasoning a reasoning model should spend. The zero value
// leaves the choice to the vendor.
// The facade normalizes empty input to EffortDefault and rejects unknown
// nonempty values before calling a driver.
//
// Example: _ = llm.Request{Reasoning: llm.EffortLow}
type Effort string

const (
	// EffortDefault leaves reasoning to the vendor's default. Empty input
	// selects the same behavior.
	//
	// Example: _ = llm.Request{Reasoning: llm.EffortDefault}
	EffortDefault Effort = "default"

	// EffortOff disables reasoning where the vendor allows it.
	//
	// Example: _ = llm.Request{Reasoning: llm.EffortOff}
	EffortOff Effort = "off"

	// EffortLow spends little reasoning.
	//
	// Example: _ = llm.Request{Reasoning: llm.EffortLow}
	EffortLow Effort = "low"

	// EffortMedium spends moderate reasoning.
	//
	// Example: _ = llm.Request{Reasoning: llm.EffortMedium}
	EffortMedium Effort = "medium"

	// EffortHigh spends the most reasoning.
	//
	// Example: _ = llm.Request{Reasoning: llm.EffortHigh}
	EffortHigh Effort = "high"
)

// Request is one model turn. Sampling settings such as temperature are left
// out on purpose: reasoning models reject them and zero is a valid
// temperature, so drivers offer a native request hook instead.
// The raw record retains caller-owned slices; do not mutate them during a
// call or while consuming a Stream created from the request.
//
//	response, err := model.Generate(ctx, llm.Request{
//		Messages: []llm.Message{llm.UserText("Say hello.")},
//		MaxTokens: 128,
//	})
//	if err != nil {
//		return err
//	}
//	_ = response.Message
type Request struct {
	// Model is the model ID. Empty uses the facade's model.
	Model string

	// Instructions are the system instructions.
	Instructions string

	// Messages is the conversation so far. It must not be empty.
	Messages []Message

	// Tools are the tools the model may call.
	Tools []ToolSpec

	// ToolChoice constrains tool use. The zero value is automatic.
	ToolChoice ToolChoice

	// Output is a JSON Schema for structured output. Empty means plain text.
	Output []byte

	// MaxTokens caps the output. Zero uses [Config.MaxTokens], and when that
	// is zero too, the vendor's default.
	MaxTokens int

	// Reasoning is the reasoning effort. The zero value is the vendor's
	// default.
	Reasoning Effort
}

// StopReason is why a model turn ended.
// Empty and unknown values mean the driver supplied no recognized reason.
// Raw responses preserve them; Run still inspects actual tool-call parts and
// validates final output rather than treating an unknown reason as refusal.
//
//	if response.StopReason == llm.StopMaxTokens {
//		return llm.ErrTruncated
//	}
type StopReason string

const (
	// StopEndTurn means the model finished its reply.
	//
	//	if response.StopReason == llm.StopEndTurn {
	//		_ = response.Message
	//	}
	StopEndTurn StopReason = "end_turn"

	// StopToolUse means the reply ends with tool calls awaiting results.
	//
	//	if response.StopReason == llm.StopToolUse {
	//		_ = response.Message.Parts // Model.Run executes registered tools.
	//	}
	StopToolUse StopReason = "tool_use"

	// StopMaxTokens means the output hit the token limit.
	//
	//	if response.StopReason == llm.StopMaxTokens {
	//		return llm.ErrTruncated
	//	}
	StopMaxTokens StopReason = "max_tokens"

	// StopRefusal means the model refused to answer.
	//
	//	if response.StopReason == llm.StopRefusal {
	//		return llm.ErrRefused
	//	}
	StopRefusal StopReason = "refusal"

	// StopContentFilter means a content filter blocked the reply.
	//
	//	if response.StopReason == llm.StopContentFilter {
	//		return llm.ErrRefused
	//	}
	StopContentFilter StopReason = "content_filter"
)

// Usage counts tokens. A count the vendor does not report stays zero; each
// driver's usage page lists what it reports.
// Counts are observations, not billing calculations or guaranteed vendor totals.
//
//	response, err := model.Generate(ctx, request)
//	if err != nil {
//		return err
//	}
//	fmt.Println(response.Usage.InputTokens, response.Usage.OutputTokens)
type Usage struct {
	// InputTokens counts the tokens sent to the model.
	InputTokens int `json:"input_tokens,omitzero"`

	// OutputTokens counts the tokens the model produced.
	OutputTokens int `json:"output_tokens,omitzero"`

	// CacheReadTokens counts input tokens served from the vendor's prompt
	// cache.
	CacheReadTokens int `json:"cache_read_tokens,omitzero"`

	// CacheWriteTokens counts input tokens written to the vendor's prompt
	// cache.
	CacheWriteTokens int `json:"cache_write_tokens,omitzero"`

	// ReasoningTokens counts output tokens spent on reasoning.
	ReasoningTokens int `json:"reasoning_tokens,omitzero"`
}

// Response is the result of one model turn.
// Raw stop reasons are not errors from Generate. Run interprets refusal,
// truncation, tool calls, and structured-output validation for an agent.
//
//	response, err := model.Generate(ctx, request)
//	if err != nil {
//		return err
//	}
//	fmt.Println(response.StopReason, response.Usage.OutputTokens)
type Response struct {
	// ID is the vendor's response ID.
	ID string `json:"id,omitzero"`

	// Model is the model that answered, as resolved by the vendor.
	Model string `json:"model,omitzero"`

	// Message is the assistant message.
	Message Message `json:"message"`

	// StopReason is why the turn ended.
	StopReason StopReason `json:"stop_reason,omitzero"`

	// Usage counts the tokens of the turn.
	Usage Usage `json:"usage,omitzero"`
}

// Purpose tells an embedding model what the vectors are for, for vendors that
// embed queries and documents differently.
// Empty input selects PurposeUnspecified; other values are passed to the driver.
//
// Example: _ = llm.EmbedRequest{Purpose: llm.PurposeQuery}
type Purpose string

const (
	// PurposeUnspecified leaves the choice to the vendor. Empty input
	// selects the same behavior.
	//
	// Example: _ = llm.EmbedRequest{Purpose: llm.PurposeUnspecified}
	PurposeUnspecified Purpose = "unspecified"

	// PurposeQuery embeds search queries.
	//
	// Example: _ = llm.EmbedRequest{Purpose: llm.PurposeQuery}
	PurposeQuery Purpose = "query"

	// PurposeDocument embeds documents to be searched.
	//
	// Example: _ = llm.EmbedRequest{Purpose: llm.PurposeDocument}
	PurposeDocument Purpose = "document"
)

// EmbedRequest asks for the embeddings of texts.
// Driver adapters receive completed facade defaults. Inputs remain caller-owned
// for the duration of the call and must not be mutated by the driver.
//
//	embeddings, err := driver.Embed(ctx, llm.EmbedRequest{
//		Model: "text-embedding-3-small", Inputs: []string{"first", "second"},
//		Purpose: llm.PurposeDocument,
//	})
//	if err != nil {
//		return err
//	}
//	_ = embeddings.Vectors
type EmbedRequest struct {
	// Model is the embedding model ID. Empty uses the facade's model.
	Model string

	// Inputs are the texts to embed. It must not be empty.
	Inputs []string

	// Dimensions is the vector size. Zero uses the vendor's default.
	Dimensions int

	// Purpose is what the vectors are for.
	Purpose Purpose
}

// Embeddings is the result of an [EmbedRequest].
// The facade checks vector count against input count; vendor usage can omit
// token categories, which then remain zero.
//
//	embeddings, err := driver.Embed(ctx, request)
//	if err != nil {
//		return err
//	}
//	fmt.Println(len(embeddings.Vectors), embeddings.Usage.InputTokens)
type Embeddings struct {
	// Vectors holds one vector per input, in input order.
	Vectors [][]float32

	// Usage counts the tokens of all batches.
	Usage Usage
}
