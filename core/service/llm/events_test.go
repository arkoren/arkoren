package llm_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/foundation/validate"
)

// privateFailureText stands in for credentials or content echoed by a provider,
// tool, or policy. It must remain available to callers but absent from events.
const privateFailureText = "private-failure-credential-and-content"

// observeFailure subscribes through the public bus API and retains the latest
// payload for synchronous assertions. A missing event leaves its zero value.
func observeFailure[T any](t *testing.T, bus *event.Bus, topic event.Topic[T]) *T {
	t.Helper()

	var observed T

	stop, err := bus.Listen(topic, func(_ context.Context, payload T) error {
		observed = payload

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	return &observed
}

// assertPrivateEvent checks both ordinary diagnostic formatting and JSON, since
// an error interface can leak text when formatted even if JSON hides its fields.
// JSON uses nanoseconds for durations; event records have no declared wire format.
func assertPrivateEvent(t *testing.T, payload any) {
	t.Helper()

	if formatted := fmt.Sprintf("%+v", payload); strings.Contains(formatted, privateFailureText) {
		t.Fatalf("failure event contains private error data: %s", formatted)
	}

	encoded, err := json.Marshal(payload, json.WithMarshalers(json.MarshalFunc(func(duration time.Duration) ([]byte, error) {
		return json.Marshal(int64(duration))
	})))

	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(encoded), privateFailureText) {
		t.Fatalf("failure event contains private error data: %s", encoded)
	}
}

// TestGenerateFailureEventExcludesVendorCause retains mapped error identity for
// the caller without making the provider's potentially sensitive text observable.
func TestGenerateFailureEventExcludesVendorCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.ResponseFailed)
	cause := errors.New(privateFailureText)
	fake := llmtest.New()
	fake.FailNext(errors.Join(llm.ErrRateLimited, cause))

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	_, err := model.Generate(t.Context(), llm.Request{Messages: []llm.Message{llm.UserText("hello")}})

	if !errors.Is(err, cause) || !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("error = %v, want provider cause and rate-limit identity", err)
	}

	if observed.Model != "model" || observed.ErrorType != "llm.ErrRateLimited" {
		t.Fatalf("failure event = %+v, want requested model and bounded rate-limit class", observed)
	}

	assertPrivateEvent(t, observed)
}

// TestStreamFailureEventExcludesNativeError checks lazy failures retain a typed
// transport cause, including its private URL, only in the returned stream error.
func TestStreamFailureEventExcludesNativeError(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.ResponseFailed)
	cause := &url.Error{
		Op:  "POST",
		URL: "https://provider.invalid/?api_key=" + privateFailureText,
		Err: errors.New(privateFailureText),
	}
	fake := llmtest.New()
	fake.FailNext(cause)

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	var failure error

	for _, err := range model.Stream(t.Context(), llm.Request{Messages: []llm.Message{llm.UserText("hello")}}) {
		failure = err
	}

	if native, ok := errors.AsType[*url.Error](failure); !ok || native != cause {
		t.Fatalf("error = %v, want original typed transport cause", failure)
	}

	if observed.ErrorType != "unknown" {
		t.Fatalf("error type = %q, want bounded unknown instead of a native type", observed.ErrorType)
	}

	assertPrivateEvent(t, observed)
}

// TestEmbedFailureEventExcludesVendorCause checks embedding errors do not expose
// input or provider diagnostics while callers retain the original error chain.
func TestEmbedFailureEventExcludesVendorCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.EmbeddingsFailed)
	cause := errors.New(privateFailureText)
	fake := llmtest.New()
	fake.FailNext(errors.Join(llm.ErrUnavailable, cause))

	embedder := llm.NewEmbedder(fake, "embedding-model", func(embedder *llm.EmbedderBuilder) {
		embedder.Events(bus)
	})

	_, err := embedder.Embed(t.Context(), privateFailureText)

	if !errors.Is(err, cause) || !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("error = %v, want provider cause and unavailable identity", err)
	}

	if observed.Model != "embedding-model" || observed.Count != 1 || observed.ErrorType != "llm.ErrUnavailable" {
		t.Fatalf("failure event = %+v, want request metadata and bounded unavailable class", observed)
	}

	assertPrivateEvent(t, observed)
}

// TestAgentFailureEventExcludesPermanentToolCause keeps fatal tool diagnostics
// out of both the event payload and model conversation, without losing the cause.
func TestAgentFailureEventExcludesPermanentToolCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.AgentFailed)
	cause := errors.New(privateFailureText)
	tool := llm.NewTool("lookup", "Find an order.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			return "", llm.Permanent(cause)
		})

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
	})

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{}`)

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	result, err := model.Run(t.Context(), agent, []llm.Message{llm.UserText("hello")})

	if !errors.Is(err, cause) || !errors.Is(err, llm.ErrPermanent) {
		t.Fatalf("error = %v, want permanent tool failure and original cause", err)
	}

	if len(result.Messages) != 2 || len(fake.Generated()) != 1 {
		t.Fatal("permanent failure added model feedback or requested another turn")
	}

	if observed.ErrorType != "llm.ErrPermanent" || observed.Steps != 1 {
		t.Fatalf("failure event = %+v, want one step and bounded permanent class", observed)
	}

	assertPrivateEvent(t, observed)
}

// TestAgentStreamFailureEventExcludesAvailabilityCause checks an execution policy
// failure remains fatal and model-safe, even when its cause wraps a ToolError.
func TestAgentStreamFailureEventExcludesAvailabilityCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.AgentFailed)
	cause := fmt.Errorf("policy credentials: %w", llm.ToolError(privateFailureText))
	var checks, handlers atomic.Int32
	tool := llm.NewTool("lookup", "Find an order.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			handlers.Add(1)

			return "unexpected execution", nil
		}, func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) {
				if checks.Add(1) == 1 {
					return true, nil
				}

				return false, cause
			})
		})

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
	})

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `invalid`)

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	err := availabilityStream(t.Context(), model, agent)

	if !errors.Is(err, cause) || !errors.Is(err, llm.ErrToolAvailability) {
		t.Fatalf("error = %v, want original availability cause", err)
	}

	if !errors.Is(err, llm.ErrPermanent) || handlers.Load() != 0 {
		t.Fatalf("error = %v, handlers = %d, want fatal policy error before handler", err, handlers.Load())
	}

	if len(fake.Generated()) != 1 || checks.Load() != 2 {
		t.Fatal("execution policy failure retried or skipped policy evaluation")
	}

	if observed.ErrorType != "llm.ErrToolAvailability" {
		t.Fatalf("error type = %q, want availability class before permanent wrapper", observed.ErrorType)
	}

	assertPrivateEvent(t, observed)
}

// TestAgentFailureEventExcludesVendorCause checks the agent-level observation
// classifies provider errors without publishing a standalone response event.
func TestAgentFailureEventExcludesVendorCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.AgentFailed)
	response := observeFailure(t, bus, llm.ResponseFailed)
	cause := errors.New(privateFailureText)
	fake := llmtest.New()
	fake.FailNext(errors.Join(llm.ErrContextLength, cause))

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	_, err := model.Run(t.Context(), llm.NewAgent[string]("support"), []llm.Message{llm.UserText("hello")})

	if !errors.Is(err, cause) || !errors.Is(err, llm.ErrContextLength) {
		t.Fatalf("error = %v, want provider cause and context-length identity", err)
	}

	if observed.ErrorType != "llm.ErrContextLength" || response.ErrorType != "" {
		t.Fatalf("agent = %+v, response = %+v, want only agent failure", observed, response)
	}

	assertPrivateEvent(t, observed)
}

// TestAgentFailureEventExcludesOperationalSchemaCause extends the existing
// output-feedback boundary to events. Operational diagnostics are neither
// correction requests nor event data; the caller still receives their cause.
func TestAgentFailureEventExcludesOperationalSchemaCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.AgentFailed)
	cause := errors.New(privateFailureText)
	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return cause
		})
	})

	agent := llm.NewAgent[string]("summary", func(agent *llm.AgentBuilder[string]) {
		agent.Output(schema)
	})

	fake := llmtest.New()
	fake.Reply(`"draft"`)

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	result, err := model.Run(t.Context(), agent, []llm.Message{llm.UserText("hello")})

	if !errors.Is(err, cause) || errors.Is(err, llm.ErrInvalidOutput) {
		t.Fatalf("error = %v, want operational cause without invalid-output identity", err)
	}

	if len(fake.Generated()) != 1 || len(result.Messages) != 2 {
		t.Fatal("operational schema error added feedback or requested another turn")
	}

	if observed.ErrorType != "unknown" {
		t.Fatalf("error type = %q, want bounded unknown for operational cause", observed.ErrorType)
	}

	assertPrivateEvent(t, observed)
}

// TestFailureEventClassifiesCanceledCause checks cancellation takes precedence
// over a joined package sentinel, without retaining any private diagnostic text.
func TestFailureEventClassifiesCanceledCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.ResponseFailed)
	cause := errors.New(privateFailureText)
	fake := llmtest.New()
	fake.FailNext(errors.Join(llm.ErrRateLimited, context.Canceled, cause))

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	_, err := model.Generate(t.Context(), llm.Request{Messages: []llm.Message{llm.UserText("hello")}})

	if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want original cause and cancellation", err)
	}

	if observed.ErrorType != "context.Canceled" {
		t.Fatalf("error type = %q, want cancellation before package class", observed.ErrorType)
	}

	assertPrivateEvent(t, observed)
}

// TestFailureEventClassifiesDeadlineCause checks deadline failures use their
// fixed context identity even when the provider also marks itself unavailable.
func TestFailureEventClassifiesDeadlineCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeFailure(t, bus, llm.ResponseFailed)
	cause := errors.New(privateFailureText)
	fake := llmtest.New()
	fake.FailNext(errors.Join(llm.ErrUnavailable, context.DeadlineExceeded, cause))

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	_, err := model.Generate(t.Context(), llm.Request{Messages: []llm.Message{llm.UserText("hello")}})

	if !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want original cause and deadline identity", err)
	}

	if observed.ErrorType != "context.DeadlineExceeded" {
		t.Fatalf("error type = %q, want deadline before package class", observed.ErrorType)
	}

	assertPrivateEvent(t, observed)
}
