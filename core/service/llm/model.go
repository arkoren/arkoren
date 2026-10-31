package llm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/llm/internal/genai"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/collection"
)

// Model runs model turns against a [Driver] with a default model ID,
// validation, telemetry and events. It holds no conversation state and is
// safe for concurrent use when its driver is.
// The driver and instrumentation services remain shared; Model owns no driver
// lifecycle and performs no automatic request retries.
//
//	model := llm.New(driver, "gpt-5.2")
//	response, err := model.Generate(ctx, llm.Request{Messages: []llm.Message{llm.UserText("Hello.")}})
//	if err != nil {
//		return err
//	}
//	_ = response.Message
type Model struct {
	// driver performs the turns.
	driver Driver

	// model is the model ID used when a request names none.
	model string

	// config holds the defaults, telemetry and events.
	config Config

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[genai.Instruments]
}

// New returns a Model that sends requests to driver and uses model, such as
// "gpt-5.2", when [Request.Model] is empty. It does no I/O and cannot fail; a
// nil driver or empty model is reported as [ErrInvalidRequest] by each call.
//
// Callbacks run directly once in order on one fresh draft. Nil entries panic;
// expanded nil/empty slices omit edits. Retained drafts cannot change the model.
//
//	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
//		model.MaxTokens(4096)
//	})
func New(driver Driver, model string, configure ...ModelBuilderFunc) *Model {
	draft := &ModelBuilder{}

	for _, configure := range configure {
		configure(draft)
	}

	return &Model{
		driver:      driver,
		model:       model,
		config:      draft.config,
		instruments: newInstruments(),
	}
}

// Driver returns the underlying Driver.
// Direct calls bypass facade defaults, validation, events, and telemetry.
//
//	backend := model.Driver()
//	_ = backend // Its lifecycle still belongs to the composition owner.
func (model *Model) Driver() Driver {
	return model.driver
}

// DriverAs returns the first driver of type T in the chain of the underlying
// driver. A driver that wraps another exposes it with an Unwrap() Driver
// method, so wrappers do not hide the concrete driver.
// Lookup does no I/O and transfers no lifecycle ownership.
//
//	fake, ok := model.DriverAs[*llmtest.Fake]()
//	if ok {
//		_ = fake.Generated()
//	}
func (model *Model) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](model.Driver())
}

// Config returns an independent reporting record. Shared telemetry and event
// services are not cloned; assigning fields cannot reconfigure the model.
//
//	config := llm.New(driver, "gpt-5.2").Config()
//	fmt.Println(config.MaxTokens)
func (model *Model) Config() Config {
	return model.config
}

// prepare fills the defaults of request and checks it, so a malformed request
// fails before any I/O. Every failure wraps [ErrInvalidRequest]. request is
// returned by value; the caller's slices are shared, not copied.
func (model *Model) prepare(request Request) (Request, error) {
	request.Model = cmp.Or(request.Model, model.model)
	request.MaxTokens = cmp.Or(request.MaxTokens, model.config.MaxTokens)
	request.Reasoning = cmp.Or(request.Reasoning, EffortDefault)
	request.ToolChoice.Kind = cmp.Or(request.ToolChoice.Kind, ToolChoiceAuto)

	switch {
	case model.driver == nil:
		return request, fmt.Errorf("%w: driver cannot be nil", ErrInvalidRequest)
	case request.Model == "":
		return request, fmt.Errorf("%w: model cannot be empty", ErrInvalidRequest)
	case len(request.Messages) == 0:
		return request, fmt.Errorf("%w: at least one message is required", ErrInvalidRequest)
	case request.MaxTokens < 0:
		return request, fmt.Errorf("%w: max tokens cannot be negative", ErrInvalidRequest)
	case !slices.Contains([]Effort{EffortDefault, EffortOff, EffortLow, EffortMedium, EffortHigh}, request.Reasoning):
		return request, fmt.Errorf("%w: unknown reasoning effort %q", ErrInvalidRequest, request.Reasoning)
	}

	if err := validateMessages(request.Messages); err != nil {
		return request, err
	}

	return request, validateToolChoice(request.ToolChoice, request.Tools)
}

// validateMessages rejects unsupported roles, empty turns, and nil parts before
// a request reaches a driver. It observes but does not mutate caller slices.
func validateMessages(messages []Message) error {
	for i, message := range messages {
		if message.Role != RoleUser && message.Role != RoleAssistant {
			return fmt.Errorf("%w: message %d has role %q", ErrInvalidRequest, i, message.Role)
		}

		if len(message.Parts) == 0 {
			return fmt.Errorf("%w: message %d has no parts", ErrInvalidRequest, i)
		}

		if index := slices.Index(message.Parts, nil); index >= 0 {
			return fmt.Errorf("%w: message %d part %d is nil", ErrInvalidRequest, i, index)
		}
	}

	return nil
}

// validateToolChoice checks the closed choice vocabulary and named-tool
// references. All errors wrap ErrInvalidRequest; no tool schema is evaluated.
func validateToolChoice(choice ToolChoice, tools []ToolSpec) error {
	if !slices.Contains([]ToolChoiceKind{ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired, ToolChoiceNamed}, choice.Kind) {
		return fmt.Errorf("%w: unknown tool choice %q", ErrInvalidRequest, choice.Kind)
	}

	named := false

	for _, tool := range tools {
		if tool.Name == "" {
			return fmt.Errorf("%w: tool name cannot be empty", ErrInvalidRequest)
		}

		named = named || tool.Name == choice.Name
	}

	if choice.Kind == ToolChoiceNamed && !named {
		return fmt.Errorf("%w: tool choice %q is not in the request tools", ErrInvalidRequest, choice.Name)
	}

	return nil
}

// Generate performs one model turn. It fills [Request.Model] and
// [Request.MaxTokens] from the facade when empty, validates the request, and
// returns the driver's response. The response carries a [StopReason] such as
// [StopToolUse] or [StopMaxTokens]; none of them is an error.
//
// A nil ctx, nil driver or malformed request fails with [ErrInvalidRequest]
// before any I/O. Other errors come from the driver and map onto the package
// sentinels. When [ModelBuilder.Events] is set, Generate publishes [ResponseGenerated]
// or [ResponseFailed]. A nil ctx publishes and records nothing.
//
//	response, err := model.Generate(ctx, llm.Request{
//		Messages: []llm.Message{llm.UserText("Say hi.")},
//	})
func (model *Model) Generate(ctx context.Context, request Request) (Response, error) {
	return model.generate(ctx, request, model.config.Events)
}

// generate is [Model.Generate] publishing its outcome on bus. [Model.Run]
// passes a nil bus, because the steps of a run report through the agent
// events instead.
func (model *Model) generate(ctx context.Context, request Request, bus *event.Bus) (Response, error) {
	if ctx == nil {
		return Response{}, fmt.Errorf("%w: context cannot be nil", ErrInvalidRequest)
	}

	request, err := model.prepare(request)
	ctx, op := genai.Begin(ctx, model.config.Telemetry, model.instruments, telemetry.Client,
		"chat", request.Model, requestAttrs(model.config, request.Model)...)
	completed := false
	defer endInterrupted(ctx, op, &completed)
	start := time.Now()

	var response Response

	if err == nil {
		response, err = model.driver.Generate(ctx, request)
	}

	completed = true
	model.finish(ctx, op, bus, request.Model, response, err, time.Since(start))

	if err != nil {
		return Response{}, err
	}

	return response, nil
}

// Stream performs one model turn and yields its events as the vendor
// produces them: [TextDelta], [ReasoningDelta], [ToolCallStarted],
// [ToolCallDelta], and last [Done] with the complete [Response]. It applies
// the same defaults and validation as [Model.Generate]. A failure is yielded
// as the second value, after which the sequence ends; a nil ctx is yielded as
// [ErrInvalidRequest] without telemetry.
//
// Stopping at a successful event, including Done, cancels the driver context
// and publishes no response outcome. The span ends without error. A driver
// failure is finalized before its error is yielded. A stream that ends without
// [Done] or an error is a driver bug and fails with [ErrUnavailable].
// [ResponseGenerated] or [ResponseFailed] is published once the stream ends.
//
// The returned collection defers validation, I/O and telemetry until traversal.
// Each traversal starts another turn and can incur another provider charge; it
// does not replay saved events. Filter and Map preserve source Done validation,
// even when they hide Done from the consumer. Reaching a Take limit stops early
// rather than completing the response. Items returns a prefix plus any terminal
// error, not a completed response on failure. Materialize only finite streams;
// JSON encoding materializes events rather than streaming the network response.
// Use Iter to pass the collection to standard iterator consumers.
//
//	for event, err := range model.Stream(ctx, request) {
//		if err != nil {
//			return err
//		}
//		if delta, ok := event.(llm.TextDelta); ok {
//			fmt.Print(delta.Text)
//		}
//	}
func (model *Model) Stream(ctx context.Context, request Request) collection.TryLazySlice[Event] {
	return collection.TryLazySlice[Event](model.stream(ctx, request, model.config.Events))
}

// stream is [Model.Stream] publishing its outcome on bus; see
// [Model.generate].
func (model *Model) stream(ctx context.Context, request Request, bus *event.Bus) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if ctx == nil {
			yield(nil, fmt.Errorf("%w: context cannot be nil", ErrInvalidRequest))

			return
		}

		request, err := model.prepare(request)
		ctx, op := genai.Begin(ctx, model.config.Telemetry, model.instruments, telemetry.Client,
			"chat", request.Model, requestAttrs(model.config, request.Model)...)
		completed := false
		defer endInterrupted(ctx, op, &completed)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		start := time.Now()

		var response Response

		if err == nil {
			response, err = model.streamTurn(ctx, request, yield)
		}

		completed = true

		if errors.Is(err, errStopped) {
			op.End(ctx, nil)

			return
		}

		model.finish(ctx, op, bus, request.Model, response, err, time.Since(start))

		if err != nil {
			yield(nil, err)
		}
	}
}

// streamTurn forwards one driver's events and obtains its final response.
// A consumer stop is distinct from an invalid driver stream, even at Done;
// returning from range joins the driver's cooperative iterator cleanup.
func (model *Model) streamTurn(ctx context.Context, request Request, yield func(Event, error) bool) (Response, error) {
	var response Response
	done := false

	for event, err := range model.driver.Stream(ctx, request) {
		if err != nil {
			return Response{}, err
		}

		if last, ok := event.(Done); ok {
			response, done = last.Response, true
		}

		if !yield(event, nil) {
			return Response{}, errStopped
		}
	}

	if !done {
		return Response{}, fmt.Errorf("%w: stream ended without a done event", ErrUnavailable)
	}

	return response, nil
}

// finish ends the telemetry of one call and publishes its event on bus, which
// may be nil. response is ignored when err is not nil.
func (model *Model) finish(ctx context.Context, op *genai.Op, bus *event.Bus, requested string, response Response, err error, duration time.Duration) {
	if err == nil {
		recordResponse(op, response)
	}

	op.End(ctx, err, errorSentinels...)
	emitResponse(ctx, bus, cmp.Or(requested, model.model), response, err, duration)
}
