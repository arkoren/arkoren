package llm

import (
	"cmp"
	"context"
	"errors"
	"time"

	"arkoren.dev/core/event"
)

// ResponseGeneratedEvent is the payload of [ResponseGenerated].
// It carries bounded operation metadata, not response text or conversation
// history. Configure ModelBuilder.Events to publish standalone turn outcomes.
//
//	unsubscribe, err := bus.Listen(llm.ResponseGenerated, func(_ context.Context, notice llm.ResponseGeneratedEvent) error {
//		fmt.Println(notice.Model, notice.Usage.OutputTokens)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type ResponseGeneratedEvent struct {
	// Model is the model that answered, as resolved by the vendor.
	Model string

	// Usage counts the tokens of the turn.
	Usage Usage

	// StopReason is why the turn ended.
	StopReason StopReason

	// Duration is how long the call took.
	Duration time.Duration
}

// ResponseFailedEvent reports a failed standalone turn without retaining its
// error or content. [Model.Generate] and [Model.Stream] still return the original
// error to the caller; event listeners receive only a bounded classification.
//
// Example:
//
//	bus := event.New()
//	_, err := bus.Listen(llm.ResponseFailed, func(_ context.Context, payload llm.ResponseFailedEvent) error {
//		fmt.Println(payload.Model, payload.ErrorType)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
type ResponseFailedEvent struct {
	// Model is the requested model ID.
	Model string

	// ErrorType is a package sentinel name such as "llm.ErrRateLimited",
	// "context.Canceled", "context.DeadlineExceeded", or "unknown". Context
	// errors take precedence over package sentinels. No cause, message, or
	// arbitrary Go type name is retained. Emitted failures are nonempty;
	// the zero value means unclassified.
	//
	// Example: fmt.Println(payload.ErrorType)
	ErrorType string

	// Duration is how long the call took.
	Duration time.Duration
}

// EmbeddingsGeneratedEvent is the payload of [EmbeddingsGenerated].
// Count and Usage describe the observed request, never its text or vectors.
//
//	unsubscribe, err := bus.Listen(llm.EmbeddingsGenerated, func(_ context.Context, notice llm.EmbeddingsGeneratedEvent) error {
//		fmt.Println(notice.Model, notice.Count)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type EmbeddingsGeneratedEvent struct {
	// Model is the embedding model ID.
	Model string

	// Count is the number of texts embedded.
	Count int

	// Usage counts the tokens of all batches.
	Usage Usage

	// Duration is how long the call took.
	Duration time.Duration
}

// EmbeddingsFailedEvent reports a failed embedding call without retaining its
// error or input texts. [Embedder.Embed] returns the original error separately,
// so listeners can observe a failure without receiving provider diagnostics.
//
// Example:
//
//	bus := event.New()
//	_, err := bus.Listen(llm.EmbeddingsFailed, func(_ context.Context, payload llm.EmbeddingsFailedEvent) error {
//		fmt.Println(payload.Count, payload.ErrorType)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
type EmbeddingsFailedEvent struct {
	// Model is the embedding model ID.
	Model string

	// Count is the number of texts in the request.
	Count int

	// ErrorType uses the bounded classification of [ResponseFailedEvent.ErrorType],
	// never provider text or a retained cause. Emitted failures are nonempty;
	// the zero value means unclassified.
	//
	// Example: fmt.Println(payload.ErrorType)
	ErrorType string

	// Duration is how long the call took.
	Duration time.Duration
}

// AgentCompletedEvent is the payload of [AgentCompleted].
// Usage sums the model turns, and Steps counts turns rather than tool calls.
// The validated output and conversation remain only in the caller's Result.
//
//	unsubscribe, err := bus.Listen(llm.AgentCompleted, func(_ context.Context, notice llm.AgentCompletedEvent) error {
//		fmt.Println(notice.Agent, notice.Steps)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type AgentCompletedEvent struct {
	// Agent is the agent's name.
	Agent string

	// Model is the model ID the run used.
	Model string

	// Steps is the number of model turns taken.
	Steps int

	// Usage sums the tokens of all turns.
	Usage Usage

	// Duration is how long the run took.
	Duration time.Duration
}

// AgentFailedEvent reports a failed [Model.Run] or [Model.RunStream] without
// retaining provider, tool, policy, or schema diagnostics. The original cause
// remains in the returned error, not in this observation or policy feedback.
//
// Example:
//
//	bus := event.New()
//	_, err := bus.Listen(llm.AgentFailed, func(_ context.Context, payload llm.AgentFailedEvent) error {
//		fmt.Println(payload.Agent, payload.ErrorType)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
type AgentFailedEvent struct {
	// Agent is the agent's name.
	Agent string

	// Model is the model ID the run used.
	Model string

	// Steps is the number of model turns taken before the failure.
	Steps int

	// Usage sums the tokens of the turns taken.
	Usage Usage

	// ErrorType uses the bounded classification of [ResponseFailedEvent.ErrorType].
	// An execution policy failure is "llm.ErrToolAvailability", ahead of its
	// ErrPermanent wrapper, unless it also matches a context error. Emitted
	// failures are nonempty; the zero value means unclassified.
	//
	// Example: fmt.Println(payload.ErrorType)
	ErrorType string

	// Duration is how long the run took.
	Duration time.Duration
}

// ToolInvokedEvent is the payload of [ToolInvoked].
// It reports successful invocation, not an independent resource-authorization
// decision or durable completion of downstream application work.
//
//	unsubscribe, err := bus.Listen(llm.ToolInvoked, func(_ context.Context, notice llm.ToolInvokedEvent) error {
//		fmt.Println(notice.Agent, notice.Tool)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type ToolInvokedEvent struct {
	// Agent is the name of the running agent.
	Agent string

	// Tool is the tool's name.
	Tool string

	// Duration is how long the call took.
	Duration time.Duration
}

// ToolFailedEvent is the payload of [ToolFailed]. It carries no error,
// because tool errors can hold internal details; the error class is on the
// "execute_tool" span.
// Denied availability and invalid arguments can fail before the typed handler
// is invoked; this observation does not expose policy or validation messages.
//
//	unsubscribe, err := bus.Listen(llm.ToolFailed, func(_ context.Context, notice llm.ToolFailedEvent) error {
//		fmt.Println(notice.Agent, notice.Tool)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type ToolFailedEvent struct {
	// Agent is the name of the running agent.
	Agent string

	// Tool is the tool's name.
	Tool string

	// Duration is how long the call took.
	Duration time.Duration
}

var (
	// AgentCompleted ("llm.agent.completed") is published when
	// [Model.Run] or [Model.RunStream] finishes with a valid result.
	// Turns inside that run do not also publish ResponseGenerated.
	//
	//	unsubscribe, err := bus.Listen(llm.AgentCompleted, func(_ context.Context, notice llm.AgentCompletedEvent) error {
	//		fmt.Println(notice.Agent, notice.Steps)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	AgentCompleted = event.NewTopic[AgentCompletedEvent]("llm.agent.completed")

	// AgentFailed ("llm.agent.failed") is published when a run fails. A
	// stream the consumer stops early publishes nothing.
	// The payload carries bounded ErrorType, never the original error cause.
	//
	//	unsubscribe, err := bus.Listen(llm.AgentFailed, func(_ context.Context, notice llm.AgentFailedEvent) error {
	//		fmt.Println(notice.Agent, notice.ErrorType)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	AgentFailed = event.NewTopic[AgentFailedEvent]("llm.agent.failed")

	// ToolInvoked ("llm.tool.invoked") is published when a tool function
	// returns without error.
	// Tool content and application resource identifiers are not included.
	//
	//	unsubscribe, err := bus.Listen(llm.ToolInvoked, func(_ context.Context, notice llm.ToolInvokedEvent) error {
	//		fmt.Println(notice.Tool, notice.Duration)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	ToolInvoked = event.NewTopic[ToolInvokedEvent]("llm.tool.invoked")

	// ToolFailed ("llm.tool.failed") is published when a tool function
	// returns an error, or when the model's arguments do not validate.
	// Availability denials can also publish this outcome without handler execution.
	//
	//	unsubscribe, err := bus.Listen(llm.ToolFailed, func(_ context.Context, notice llm.ToolFailedEvent) error {
	//		fmt.Println(notice.Tool, notice.Duration)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	ToolFailed = event.NewTopic[ToolFailedEvent]("llm.tool.failed")

	// ResponseGenerated is published when [Model.Generate] returns a
	// response, or a [Model.Stream] finishes normally after its [Done] event.
	// A consumer break, including at Done, suppresses this standalone outcome.
	//
	//	unsubscribe, err := bus.Listen(llm.ResponseGenerated, func(_ context.Context, notice llm.ResponseGeneratedEvent) error {
	//		fmt.Println(notice.Model, notice.Usage.OutputTokens)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	ResponseGenerated = event.NewTopic[ResponseGeneratedEvent]("llm.response.generated")

	// ResponseFailed is published when a [Model.Generate] or [Model.Stream]
	// call fails. A stream the consumer stops early publishes nothing.
	// ErrorType is a bounded observation; inspect returned errors for recovery.
	//
	//	unsubscribe, err := bus.Listen(llm.ResponseFailed, func(_ context.Context, notice llm.ResponseFailedEvent) error {
	//		fmt.Println(notice.Model, notice.ErrorType)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	ResponseFailed = event.NewTopic[ResponseFailedEvent]("llm.response.failed")

	// EmbeddingsGenerated is published when [Embedder.Embed] succeeds.
	// No vector or input text is retained in the event.
	//
	//	unsubscribe, err := bus.Listen(llm.EmbeddingsGenerated, func(_ context.Context, notice llm.EmbeddingsGeneratedEvent) error {
	//		fmt.Println(notice.Model, notice.Count)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	EmbeddingsGenerated = event.NewTopic[EmbeddingsGeneratedEvent]("llm.embeddings.generated")

	// EmbeddingsFailed is published when [Embedder.Embed] fails.
	// It classifies the failure without exporting provider diagnostic text.
	//
	//	unsubscribe, err := bus.Listen(llm.EmbeddingsFailed, func(_ context.Context, notice llm.EmbeddingsFailedEvent) error {
	//		fmt.Println(notice.Model, notice.ErrorType)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	EmbeddingsFailed = event.NewTopic[EmbeddingsFailedEvent]("llm.embeddings.failed")
)

// emitResponse publishes the outcome of one model call.
func emitResponse(ctx context.Context, bus *event.Bus, model string, response Response, err error, duration time.Duration) {
	if err != nil {
		bus.Emit(ctx, ResponseFailed, ResponseFailedEvent{
			Model:     model,
			ErrorType: eventErrorType(err),
			Duration:  duration,
		})

		return
	}

	bus.Emit(ctx, ResponseGenerated, ResponseGeneratedEvent{
		Model:      cmp.Or(response.Model, model),
		Usage:      response.Usage,
		StopReason: response.StopReason,
		Duration:   duration,
	})
}

// eventErrorType bounds non-nil failure classifications to context identities and
// the package sentinel names, in that order. Sentinel precedence follows the
// shared package list, so policy failures outrank ErrPermanent. Unknown failures
// use a literal fallback; error text and arbitrary type names are never read.
func eventErrorType(err error) string {
	if errors.Is(err, context.Canceled) {
		return "context.Canceled"
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "context.DeadlineExceeded"
	}

	for _, sentinel := range errorSentinels {
		if errors.Is(err, sentinel.Err) {
			return sentinel.Name
		}
	}

	return "unknown"
}
