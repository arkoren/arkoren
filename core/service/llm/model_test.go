package llm_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestStreamCollectionDefersAndRepeatsIO ensures a collection is an execution
// recipe, not a cached response, even after lazy projections are constructed.
func TestStreamCollectionDefersAndRepeatsIO(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply("first")
	fake.Reply("second")

	stream := llm.New(fake, "m").Stream(t.Context(), userRequest("x")).Map(func(_ int, item llm.Event) (llm.Event, error) {
		return item, nil
	})

	if len(fake.Generated()) != 0 {
		t.Fatal("construction invoked driver")
	}

	first, err := stream.Items()

	if err != nil {
		t.Fatal(err)
	}

	second, err := stream.Items()

	if err != nil {
		t.Fatal(err)
	}

	if len(fake.Generated()) != 2 || reflect.DeepEqual(first, second) {
		t.Fatalf("expected two distinct turns: %v, %v", first, second)
	}
}

// TestStreamCollectionItemsPreservesTerminalPrefix exercises native
// materialization without losing the original provider failure or partial text.
func TestStreamCollectionItemsPreservesTerminalPrefix(t *testing.T) {
	t.Parallel()

	driver := newStub()
	driver.events = []llm.Event{llm.TextDelta{Text: "partial"}}

	driver.err = llm.ErrRateLimited

	items, err := llm.New(driver, "m").Stream(t.Context(), userRequest("x")).Items()

	if !errors.Is(err, llm.ErrRateLimited) || !reflect.DeepEqual(items, driver.events) {
		t.Fatalf("items = %#v, error = %v", items, err)
	}
}

// TestStreamCollectionFilterCannotHideMissingDone verifies that source
// completion validation still runs when a projection drops every event.
func TestStreamCollectionFilterCannotHideMissingDone(t *testing.T) {
	t.Parallel()

	driver := newStub()
	driver.events = []llm.Event{llm.TextDelta{Text: "partial"}}

	items, err := llm.New(driver, "m").Stream(t.Context(), userRequest("x")).Filter(func(_ int, _ llm.Event) (bool, error) {
		return false, nil
	}).Items()

	if !errors.Is(err, llm.ErrUnavailable) || len(items) != 0 {
		t.Fatalf("items = %#v, error = %v", items, err)
	}
}

// TestStreamCollectionFilterDoneStillCompletes separates the projected event
// view from source completion: hiding Done does not abandon a fully read turn.
func TestStreamCollectionFilterDoneStillCompletes(t *testing.T) {
	t.Parallel()

	driver := newStub()
	driver.events = doneEvent(driver)

	items, err := llm.New(driver, "m").Stream(t.Context(), userRequest("x")).Filter(func(_ int, item llm.Event) (bool, error) {
		_, done := item.(llm.Done)

		return !done, nil
	}).Items()

	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, error = %v", items, err)
	}
}

// TestStreamCollectionTakeCancelsWithoutOutcome ensures collection-driven
// early stop cancels the driver without inventing success or failure events.
func TestStreamCollectionTakeCancelsWithoutOutcome(t *testing.T) {
	t.Parallel()

	bus := event.New()
	outcomes := 0

	if _, err := bus.Listen(llm.ResponseGenerated, func(context.Context, llm.ResponseGeneratedEvent) error {
		outcomes++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Listen(llm.ResponseFailed, func(context.Context, llm.ResponseFailedEvent) error {
		outcomes++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	driver := newStub()
	driver.events = doneEvent(driver)
	model := llm.New(driver, "m", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	items, err := model.Stream(t.Context(), userRequest("x")).Take(1).Items()

	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, error = %v", items, err)
	}

	if !errors.Is(driver.streamCtx.Err(), context.Canceled) || outcomes != 0 {
		t.Fatalf("context = %v, outcomes = %d", driver.streamCtx.Err(), outcomes)
	}
}

// TestStreamCollectionIterFalseCancels exercises the explicit standard-iterator
// interoperability boundary without relying on the language's range break.
func TestStreamCollectionIterFalseCancels(t *testing.T) {
	t.Parallel()

	driver := newStub()
	driver.events = doneEvent(driver)
	stream := llm.New(driver, "m").Stream(t.Context(), userRequest("x"))
	stream.Iter()(func(_ llm.Event, err error) bool {
		if err != nil {
			t.Fatal(err)
		}

		return false
	})

	if !errors.Is(driver.streamCtx.Err(), context.Canceled) {
		t.Fatalf("driver context = %v", driver.streamCtx.Err())
	}
}

// TestEmptyRequestEnumsSelectDefaults checks normalization at the facade edge.
// The caller's original request remains unset while the driver sees canonical values.
func TestEmptyRequestEnumsSelectDefaults(t *testing.T) {
	t.Parallel()

	driver := llmtest.New()
	driver.Reply("hello")

	request := llm.Request{Messages: []llm.Message{llm.UserText("hello")}}

	if _, err := llm.New(driver, "chat").Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	generated := driver.Generated()

	if generated[0].Reasoning != llm.EffortDefault || generated[0].ToolChoice.Kind != llm.ToolChoiceAuto {
		t.Fatalf("driver defaults = %+v", generated[0])
	}

	if request.Reasoning != "" || request.ToolChoice.Kind != "" {
		t.Fatal("normalization mutated caller request")
	}
}

// TestUnknownReasoningRejected prevents arbitrary lexical values from bypassing
// validation that previously used numeric comparisons.
func TestUnknownReasoningRejected(t *testing.T) {
	t.Parallel()

	driver := llmtest.New()

	_, err := llm.New(driver, "chat").Generate(t.Context(), llm.Request{
		Messages: []llm.Message{llm.UserText("hello")}, Reasoning: "aardvark",
	})

	if !errors.Is(err, llm.ErrInvalidRequest) || len(driver.Generated()) != 0 {
		t.Fatalf("Generate error = %v, calls = %d", err, len(driver.Generated()))
	}
}

// TestUnknownToolChoiceRejected checks the closed vocabulary rather than string
// ordering; malformed input fails before any external driver call.
func TestUnknownToolChoiceRejected(t *testing.T) {
	t.Parallel()

	driver := llmtest.New()

	_, err := llm.New(driver, "chat").Generate(t.Context(), llm.Request{
		Messages: []llm.Message{llm.UserText("hello")}, ToolChoice: llm.ToolChoice{Kind: "aardvark"},
	})

	if !errors.Is(err, llm.ErrInvalidRequest) || len(driver.Generated()) != 0 {
		t.Fatalf("Generate error = %v, calls = %d", err, len(driver.Generated()))
	}
}

// secretPrompt must never reach telemetry or events.
const secretPrompt = "my password is hunter2"

// stubDriver is a hand-written Driver recording what it receives.
type stubDriver struct {
	// generated and embedded are the last requests received.
	generated llm.Request
	// embedded captures the last embedding request sent through the facade.
	embedded llm.EmbedRequest

	// streamCtx is the context of the last Stream call.
	streamCtx context.Context

	// response, err, events and vectors script the replies.
	response llm.Response
	// err is the configured failure returned by every driver operation.
	err error
	// events scripts the stream in order before any configured failure.
	events []llm.Event
	// vectors scripts the embedding result independently of the request.
	vectors [][]float32
}

// Generate captures the request and returns the scripted response and failure.
func (driver *stubDriver) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	driver.generated = request

	return driver.response, driver.err
}

// Stream captures the request and context, then yields the scripted events in order.
func (driver *stubDriver) Stream(ctx context.Context, request llm.Request) iter.Seq2[llm.Event, error] {
	driver.generated, driver.streamCtx = request, ctx

	return func(yield func(llm.Event, error) bool) {
		for _, events := range driver.events {
			if !yield(events, nil) {
				return
			}
		}

		if driver.err != nil {
			yield(nil, driver.err)
		}
	}
}

// wrapper is a driver that wraps another, for DriverAs.
type wrapper struct {
	// Driver supplies the operations while Unwrap exposes it to driver discovery.
	llm.Driver
}

// Unwrap exposes the embedded driver to the facade's driver discovery.
func (wrapped wrapper) Unwrap() llm.Driver {
	return wrapped.Driver
}

// newStub supplies a successful model turn and two embedding vectors for facade tests.
func newStub() *stubDriver {
	return &stubDriver{
		response: llm.Response{
			ID:    "resp_1",
			Model: "gpt-5.2-2026-01-01",
			Message: llm.Message{
				Role:  llm.RoleAssistant,
				Parts: []llm.Part{llm.Text{Text: "hi"}},
			},
			StopReason: llm.StopEndTurn,
			Usage: llm.Usage{
				InputTokens:     12,
				OutputTokens:    5,
				CacheReadTokens: 2,
			},
		},
		vectors: [][]float32{{1, 2}, {3, 4}},
	}
}

// userRequest creates one user turn without overriding the facade's model defaults.
func userRequest(text string) llm.Request {
	return llm.Request{Messages: []llm.Message{llm.UserText(text)}}
}

// doneEvent pairs a text delta with the driver's scripted terminal response.
func doneEvent(driver *stubDriver) []llm.Event {
	return []llm.Event{llm.TextDelta{Text: "h"}, llm.Done{Response: driver.response}}
}

// TestGenerateAppliesDefaults checks the completed request seen by the driver.
// Facade defaults must fill fields the caller leaves unset.
func TestGenerateAppliesDefaults(t *testing.T) {
	t.Parallel()

	driver := newStub()
	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
		model.MaxTokens(256)
	})

	if _, err := model.Generate(t.Context(), userRequest("hello")); err != nil {
		t.Fatal(err)
	}

	if driver.generated.Model != "gpt-5.2" || driver.generated.MaxTokens != 256 {
		t.Fatalf("driver got model %q and max tokens %d, want the facade defaults", driver.generated.Model, driver.generated.MaxTokens)
	}
}

// TestGenerateRequestOverridesDefaults keeps explicit per-call settings intact.
// Constructor defaults must not replace values already present in the request.
func TestGenerateRequestOverridesDefaults(t *testing.T) {
	t.Parallel()

	driver := newStub()
	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
		model.MaxTokens(256)
	})

	request := userRequest("hello")
	request.Model, request.MaxTokens = "gpt-5.2-mini", 64

	if _, err := model.Generate(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	if driver.generated.Model != "gpt-5.2-mini" || driver.generated.MaxTokens != 64 {
		t.Fatalf("driver got model %q and max tokens %d, want the request values", driver.generated.Model, driver.generated.MaxTokens)
	}
}

// TestGenerateRejectsNilContextAndDriverBeforeIO checks facade boundary guards.
// Invalid dependencies must fail without entering a driver operation.
func TestGenerateRejectsNilContextAndDriverBeforeIO(t *testing.T) {
	t.Parallel()

	driver := newStub()

	//nolint:staticcheck // a nil context is the case under test.
	if _, err := llm.New(driver, "m").Generate(nil, userRequest("x")); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("nil context error = %v, want ErrInvalidRequest", err)
	}

	if _, err := llm.New(nil, "m").Generate(t.Context(), userRequest("x")); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("nil driver error = %v, want ErrInvalidRequest", err)
	}

	if len(driver.generated.Messages) != 0 {
		t.Fatal("driver was called")
	}
}

// TestGenerateRejectsMalformedRequests exercises requests that cannot be sent.
// Each invalid shape must fail before the stub records any generation.
func TestGenerateRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	bad := map[string]llm.Request{
		"no messages": {},
		"empty parts": {Messages: []llm.Message{{Role: llm.RoleUser}}},
		"nil part": {Messages: []llm.Message{{
			Role:  llm.RoleUser,
			Parts: []llm.Part{nil},
		}}},
		"bad role": {Messages: []llm.Message{{
			Role:  "system",
			Parts: []llm.Part{llm.Text{Text: "x"}},
		}}},
		"negative max": {
			Messages:  []llm.Message{llm.UserText("x")},
			MaxTokens: -1,
		},
		"unlisted named tool": {
			Messages: []llm.Message{llm.UserText("x")},
			ToolChoice: llm.ToolChoice{
				Kind: llm.ToolChoiceNamed,
				Name: "missing",
			},
		},
		"unnamed tool": {
			Messages: []llm.Message{llm.UserText("x")},
			Tools:    []llm.ToolSpec{{}},
		},
	}

	for name, request := range bad {
		driver := newStub()

		if _, err := llm.New(driver, "m").Generate(t.Context(), request); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: error = %v, want ErrInvalidRequest", name, err)
		}

		if len(driver.generated.Messages) != 0 {
			t.Errorf("%s: driver was called", name)
		}
	}
}

// TestGenerateRejectsMissingModel covers omission in both facade and request.
// Generation cannot proceed without a completed model identifier.
func TestGenerateRejectsMissingModel(t *testing.T) {
	t.Parallel()

	if _, err := llm.New(newStub(), "").Generate(t.Context(), userRequest("x")); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

// TestGenerateKeepsDriverErrorInChain checks wrapped backend failures.
// Callers must still be able to match the driver's original sentinel.
func TestGenerateKeepsDriverErrorInChain(t *testing.T) {
	t.Parallel()

	driver := newStub()

	driver.err = errors.Join(llm.ErrRateLimited, errors.New("vendor 429"))

	if _, err := llm.New(driver, "m").Generate(t.Context(), userRequest("x")); !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

// TestDriverAsLooksThroughWrappers checks discovery through an Unwrap boundary.
// Both model and embedder facades must expose the underlying concrete driver.
func TestDriverAsLooksThroughWrappers(t *testing.T) {
	t.Parallel()

	driver := newStub()
	model := llm.New(wrapper{Driver: driver}, "m")

	if got, ok := model.DriverAs[*stubDriver](); !ok || got != driver {
		t.Fatal("DriverAs did not find the wrapped driver")
	}

	embedder := llm.NewEmbedder(wrapper{Driver: driver}, "m")

	if got, ok := embedder.DriverAs[*stubDriver](); !ok || got != driver {
		t.Fatal("Embedder.DriverAs did not find the wrapped driver")
	}
}

// TestGeneratePublishesGenerated checks the success event after a completed turn.
// Its metadata must describe the observed response rather than prompt content.
func TestGeneratePublishesGenerated(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got llm.ResponseGeneratedEvent

	if _, err := bus.Listen(llm.ResponseGenerated, func(_ context.Context, payload llm.ResponseGeneratedEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := llm.New(newStub(), "gpt-5.2", func(model *llm.ModelBuilder) {
		model.Events(bus)
	}).Generate(t.Context(), userRequest(secretPrompt)); err != nil {
		t.Fatal(err)
	}

	if got.Model != "gpt-5.2-2026-01-01" || got.Usage.InputTokens != 12 || got.StopReason != llm.StopEndTurn {
		t.Fatalf("generated = %+v", got)
	}
}

// TestGeneratePublishesFailed checks the failure event when generation fails.
// The event must retain the operation's model and failure classification.
func TestGeneratePublishesFailed(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got llm.ResponseFailedEvent

	if _, err := bus.Listen(llm.ResponseFailed, func(_ context.Context, payload llm.ResponseFailedEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	driver := newStub()

	driver.err = llm.ErrUnavailable

	if _, err := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
		model.Events(bus)
	}).Generate(t.Context(), userRequest("x")); err == nil {
		t.Fatal("want an error")
	}

	if got.Model != "gpt-5.2" || got.ErrorType != "llm.ErrUnavailable" {
		t.Fatalf("failed = %+v", got)
	}
}

// TestStreamYieldsEventsAndPublishesGenerated checks forwarding and final reporting.
// The Done event supplies the completed response used by the success event.
func TestStreamYieldsEventsAndPublishesGenerated(t *testing.T) {
	t.Parallel()

	bus := event.New()
	published := 0

	if _, err := bus.Listen(llm.ResponseGenerated, func(context.Context, llm.ResponseGeneratedEvent) error {
		published++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fake := telemetrytest.New()
	driver := newStub()
	driver.events = doneEvent(driver)
	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
		model.Events(bus)
		model.Telemetry(fake.Telemetry(t))
		model.MaxTokens(9)
	})

	var got []llm.Event

	for events, err := range model.Stream(t.Context(), userRequest("x")) {
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, events)
	}

	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}

	if done, ok := got[1].(llm.Done); !ok || done.Response.ID != "resp_1" {
		t.Fatalf("last event = %#v, want Done", got[1])
	}

	if published != 1 || driver.generated.MaxTokens != 9 {
		t.Fatalf("published = %d, max tokens = %d", published, driver.generated.MaxTokens)
	}

	fake.AssertSpan(t, "chat gpt-5.2", telemetry.Int("gen_ai.usage.output_tokens", 5))
}

// TestStreamBreakCancelsDriverContext covers a caller abandoning iteration.
// The stream's derived context must be canceled when the consumer stops.
func TestStreamBreakCancelsDriverContext(t *testing.T) {
	t.Parallel()

	published := false
	bus := event.New()

	if _, err := bus.Listen(llm.ResponseGenerated, func(context.Context, llm.ResponseGeneratedEvent) error {
		published = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	driver := newStub()
	driver.events = []llm.Event{llm.TextDelta{Text: "a"}, llm.TextDelta{Text: "b"}, llm.Done{}}
	model := llm.New(driver, "m", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	for range model.Stream(t.Context(), userRequest("x")) {
		break
	}

	if driver.streamCtx.Err() == nil {
		t.Fatal("driver context was not canceled after break")
	}

	if published {
		t.Fatal("an abandoned stream published an event")
	}
}

// TestStreamYieldsDriverError checks that iteration surfaces a backend failure.
// Matching the yielded error must preserve the driver's sentinel.
func TestStreamYieldsDriverError(t *testing.T) {
	t.Parallel()

	driver := newStub()
	driver.events = []llm.Event{llm.TextDelta{Text: "a"}}

	driver.err = llm.ErrRateLimited

	var last error

	for _, err := range llm.New(driver, "m").Stream(t.Context(), userRequest("x")) {
		last = err
	}

	if !errors.Is(last, llm.ErrRateLimited) {
		t.Fatalf("last error = %v, want ErrRateLimited", last)
	}
}

// TestStreamWithoutDoneFails covers a backend stream missing its terminal response.
// Exhausting partial events alone must not count as successful generation.
func TestStreamWithoutDoneFails(t *testing.T) {
	t.Parallel()

	driver := newStub()
	driver.events = []llm.Event{llm.TextDelta{Text: "a"}}

	var last error

	for _, err := range llm.New(driver, "m").Stream(t.Context(), userRequest("x")) {
		last = err
	}

	if !errors.Is(last, llm.ErrUnavailable) {
		t.Fatalf("last error = %v, want ErrUnavailable", last)
	}
}

// TestStreamRejectsMalformedRequestBeforeIO checks validation at iteration time.
// An invalid request must not reach the streaming driver.
func TestStreamRejectsMalformedRequestBeforeIO(t *testing.T) {
	t.Parallel()

	driver := newStub()

	var got error

	for _, err := range llm.New(driver, "m").Stream(t.Context(), llm.Request{}) {
		got = err
	}

	if !errors.Is(got, llm.ErrInvalidRequest) || driver.streamCtx != nil {
		t.Fatalf("error = %v, driver called = %v", got, driver.streamCtx != nil)
	}
}
