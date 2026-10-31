package llm_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/foundation/validate"
)

// availabilityRun exercises the nonstreaming API with the same conversation as
// availabilityStream. Assertions inspect the fake's recorded requests.
func availabilityRun(ctx context.Context, model *llm.Model, agent llm.Agent[string]) error {
	_, err := model.Run(ctx, agent, userHistory())

	return err
}

// availabilityStream consumes the entire streaming API so failures cannot hide
// behind an unconsumed lazy sequence.
func availabilityStream(ctx context.Context, model *llm.Model, agent llm.Agent[string]) error {
	for _, err := range model.RunStream(ctx, agent, userHistory()) {
		if err != nil {
			return err
		}
	}

	return nil
}

// checkAvailabilityFiltering follows a guessed hidden call and an allowed call
// through three model turns. Discovery runs only once, execution rechecks once,
// and denial precedes decoding even malformed arguments.
func checkAvailabilityFiltering(t *testing.T, run func(context.Context, *llm.Model, llm.Agent[string]) error) {
	t.Helper()

	var allowedChecks, deniedChecks, handlers atomic.Int32
	allowed := llm.NewTool("allowed", "Allowed.", orderSchema,
		func(context.Context, orderArgs) (string, error) { handlers.Add(1); return "found", nil },
		func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) { allowedChecks.Add(1); return true, nil })
		})

	denied := llm.NewTool("denied", "Denied.", orderSchema,
		func(context.Context, orderArgs) (string, error) { panic("denied handler") },
		func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) { deniedChecks.Add(1); return false, nil })
		})

	fake := llmtest.New()
	fake.ReplyToolCall("denied", `invalid`)
	fake.ReplyToolCall("allowed", `{"number":"A-42"}`)
	fake.Reply("done")

	agent := llm.NewAgent[string]("agent", func(agent *llm.AgentBuilder[string]) { agent.Tools(allowed, denied) })

	if err := run(t.Context(), llm.New(fake, "model"), agent); err != nil {
		t.Fatal(err)
	}

	requests := fake.Generated()

	if len(requests) != 3 {
		t.Fatalf("requests = %d", len(requests))
	}

	for _, request := range requests {
		if len(request.Tools) != 1 || request.Tools[0].Name != "allowed" {
			t.Fatalf("advertised tools = %+v", request.Tools)
		}
	}

	results := toolResults(t, requests[1].Messages)

	if len(results) != 1 {
		t.Fatalf("denied results = %+v", results)
	}

	if results[0] != (llm.ToolResult{CallID: "call-1", Content: "tool unavailable", IsError: true}) {
		t.Fatalf("denied result = %+v", results)
	}

	if [3]int32{allowedChecks.Load(), deniedChecks.Load(), handlers.Load()} != [3]int32{2, 2, 1} {
		t.Fatalf("checks = %d, %d, handlers = %d", allowedChecks.Load(), deniedChecks.Load(), handlers.Load())
	}
}

// TestRunAvailabilityFiltersOnceAndGuardsHiddenNames covers Run discovery and
// execution without confusing hidden tools with unknown tool names.
func TestRunAvailabilityFiltersOnceAndGuardsHiddenNames(t *testing.T) {
	t.Parallel()

	checkAvailabilityFiltering(t, availabilityRun)
}

// TestRunStreamAvailabilityFiltersOnceAndGuardsHiddenNames gives streaming the
// same filtering, execution ordering and exact callback-count guarantees.
func TestRunStreamAvailabilityFiltersOnceAndGuardsHiddenNames(t *testing.T) {
	t.Parallel()

	checkAvailabilityFiltering(t, availabilityStream)
}

// checkAvailabilityDiscoveryFailure proves a wrapped ToolError from policy is
// fatal before model I/O, preserving identity but not producing a tool result.
func checkAvailabilityDiscoveryFailure(t *testing.T, run func(context.Context, *llm.Model, llm.Agent[string]) error) {
	t.Helper()

	cause := fmt.Errorf("permissions backend: %w", llm.ToolError("secret"))
	var checks atomic.Int32
	tool := llm.NewTool("guarded", "Guarded.", orderSchema,
		func(context.Context, orderArgs) (string, error) { panic("handler") },
		func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) { checks.Add(1); return false, cause })
		})

	fake := llmtest.New()
	fake.Reply("must not be requested")

	agent := llm.NewAgent[string]("agent", func(agent *llm.AgentBuilder[string]) { agent.Tools(tool) })

	err := run(t.Context(), llm.New(fake, "model"), agent)

	if !errors.Is(err, llm.ErrToolAvailability) || !errors.Is(err, cause) {
		t.Fatalf("error = %v", err)
	}

	if errors.Is(err, llm.ErrPermanent) || len(fake.Generated()) != 0 || checks.Load() != 1 {
		t.Fatalf("error = %v, requests = %d, checks = %d", err, len(fake.Generated()), checks.Load())
	}
}

// TestRunAvailabilityDiscoveryErrorPreventsIO checks fail-closed discovery in
// the nonstreaming API, including model-safe errors wrapped by policy failures.
func TestRunAvailabilityDiscoveryErrorPreventsIO(t *testing.T) {
	t.Parallel()

	checkAvailabilityDiscoveryFailure(t, availabilityRun)
}

// TestRunStreamAvailabilityDiscoveryErrorPreventsIO checks the lazy streaming
// API fails before a driver is asked to produce any events.
func TestRunStreamAvailabilityDiscoveryErrorPreventsIO(t *testing.T) {
	t.Parallel()

	checkAvailabilityDiscoveryFailure(t, availabilityStream)
}

// checkAvailabilityExecutionFailure starts a sibling, then fails execution
// policy. The run cancels that sibling and never sends policy text to a model.
func checkAvailabilityExecutionFailure(t *testing.T, run func(context.Context, *llm.Model, llm.Agent[string]) error) {
	t.Helper()

	started, canceled := make(chan struct{}), make(chan struct{})
	cause := fmt.Errorf("permissions backend: %w", llm.ToolError("secret"))
	var checks atomic.Int32
	slow := llm.NewTool("slow", "Slow.", orderSchema,
		func(ctx context.Context, _ orderArgs) (string, error) {
			close(started)
			<-ctx.Done()
			close(canceled)

			return "", ctx.Err()
		})

	fatal := llm.NewTool("fatal", "Fatal.", orderSchema,
		func(context.Context, orderArgs) (string, error) { panic("handler") },
		func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) {
				if checks.Add(1) == 1 {
					return true, nil
				}

				<-started

				return false, cause
			})
		})

	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			call("slow", "slow", `{"number":"A-42"}`), call("fatal", "fatal", `invalid`),
		}}, StopReason: llm.StopToolUse,
	})
	fake.Reply("must not be requested")

	agent := llm.NewAgent[string]("agent", func(agent *llm.AgentBuilder[string]) { agent.Tools(slow, fatal) })

	err := run(t.Context(), llm.New(fake, "model"), agent)

	if !errors.Is(err, llm.ErrToolAvailability) || !errors.Is(err, cause) {
		t.Fatalf("error = %v", err)
	}

	if !errors.Is(err, llm.ErrPermanent) || len(fake.Generated()) != 1 || checks.Load() != 2 {
		t.Fatalf("error = %v, requests = %d, checks = %d", err, len(fake.Generated()), checks.Load())
	}

	select {
	case <-canceled:
	default:
		t.Fatal("sibling was not canceled")
	}
}

// TestRunAvailabilityExecutionErrorCancelsSiblings checks existing permanent
// failure machinery also handles fatal capability-policy errors.
func TestRunAvailabilityExecutionErrorCancelsSiblings(t *testing.T) {
	t.Parallel()

	checkAvailabilityExecutionFailure(t, availabilityRun)
}

// TestRunStreamAvailabilityExecutionErrorCancelsSiblings ensures streaming
// stops after a policy failure and exposes no model-facing tool result.
func TestRunStreamAvailabilityExecutionErrorCancelsSiblings(t *testing.T) {
	t.Parallel()

	checkAvailabilityExecutionFailure(t, availabilityStream)
}

// checkAvailabilityRevocation queues two calls behind one concurrency slot.
// The first handler revokes capability before the second policy is rechecked.
func checkAvailabilityRevocation(t *testing.T, run func(context.Context, *llm.Model, llm.Agent[string]) error) {
	t.Helper()

	var revoked atomic.Bool
	var checks, handlers atomic.Int32
	tool := llm.NewTool("guarded", "Guarded.", orderSchema,
		func(context.Context, orderArgs) (string, error) {
			handlers.Add(1)
			revoked.Store(true)

			return "revoked", nil
		}, func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) { checks.Add(1); return !revoked.Load(), nil })
		})

	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			call("first", "guarded", `{"number":"A-42"}`), call("second", "guarded", `invalid`),
		}}, StopReason: llm.StopToolUse,
	})
	fake.Reply("done")

	agent := llm.NewAgent[string]("agent", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
		agent.ToolConcurrency(1)
	})

	if err := run(t.Context(), llm.New(fake, "model"), agent); err != nil {
		t.Fatal(err)
	}

	requests := fake.Generated()
	results := toolResults(t, requests[1].Messages)

	if len(results) != 2 || !results[1].IsError || results[1].Content != "tool unavailable" {
		t.Fatalf("results = %+v", results)
	}

	if checks.Load() != 3 || handlers.Load() != 1 {
		t.Fatalf("checks = %d, handlers = %d", checks.Load(), handlers.Load())
	}
}

// TestRunAvailabilityRechecksAfterConcurrencySlot verifies queued calls cannot
// use the policy decision made before another handler revoked access.
func TestRunAvailabilityRechecksAfterConcurrencySlot(t *testing.T) {
	t.Parallel()

	checkAvailabilityRevocation(t, availabilityRun)
}

// TestRunStreamAvailabilityRechecksAfterConcurrencySlot applies the same
// revocation ordering to streaming runs.
func TestRunStreamAvailabilityRechecksAfterConcurrencySlot(t *testing.T) {
	t.Parallel()

	checkAvailabilityRevocation(t, availabilityStream)
}

// checkAvailabilityDefaults verifies that policy evaluation never parses input.
// The execution path normalizes once and gives its handler the defaulted value.
func checkAvailabilityDefaults(t *testing.T, run func(context.Context, *llm.Model, llm.Agent[string]) error) {
	t.Helper()

	var normalizations, handlers atomic.Int32
	schema := validate.Struct(func(object *validate.StructBuilder[orderArgs]) {
		object.Field("Number", func(number *validate.StringBuilder[string]) {
			number.Normalize(func(value string) string {
				normalizations.Add(1)

				return value
			})
			number.Default("A-42")
		})
	})

	tool := llm.NewTool("guarded", "Guarded.", schema,
		func(_ context.Context, in orderArgs) (string, error) {
			handlers.Add(1)

			return in.Number, nil
		}, func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) {
				return true, nil
			})
		})
	normalizations.Store(0)

	fake := llmtest.New()
	fake.ReplyToolCall("guarded", `{}`)
	fake.Reply("done")

	agent := llm.NewAgent[string]("agent", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
	})

	if err := run(t.Context(), llm.New(fake, "model"), agent); err != nil {
		t.Fatal(err)
	}

	results := toolResults(t, fake.Generated()[1].Messages)

	if len(results) != 1 || results[0].Content != "A-42" {
		t.Fatalf("results = %+v", results)
	}

	if normalizations.Load() != 1 || handlers.Load() != 1 {
		t.Fatalf("normalizations = %d, handlers = %d", normalizations.Load(), handlers.Load())
	}
}

// TestRunAvailabilityPreservesDefaultsOnce checks allowed Run calls retain the
// existing schema default behavior without any extra normalization.
func TestRunAvailabilityPreservesDefaultsOnce(t *testing.T) {
	t.Parallel()

	checkAvailabilityDefaults(t, availabilityRun)
}

// TestRunStreamAvailabilityPreservesDefaultsOnce checks streaming calls retain
// the same once-only normalization and default application as Run.
func TestRunStreamAvailabilityPreservesDefaultsOnce(t *testing.T) {
	t.Parallel()

	checkAvailabilityDefaults(t, availabilityStream)
}

// principalKey isolates the test's request-scoped permission without changing
// the library's context or introducing shared principal state.
type principalKey struct{}

// TestRunAvailabilityConcurrentPrincipalsNeverShareDecisions runs one immutable
// agent concurrently for an allowed and denied principal. Both request contexts
// reach discovery and execution, including the guessed hidden tool call.
func TestRunAvailabilityConcurrentPrincipalsNeverShareDecisions(t *testing.T) {
	t.Parallel()

	var checks, handlers atomic.Int32
	tool := llm.NewTool("guarded", "Guarded.", validate.Infer[struct{}](),
		func(ctx context.Context, _ struct{}) (string, error) {
			if ctx.Value(principalKey{}) != "allowed" {
				panic("wrong principal")
			}

			handlers.Add(1)

			return "ok", nil
		}, func(action *llm.ToolBuilder) {
			action.Available(func(ctx context.Context) (bool, error) {
				checks.Add(1)

				return ctx.Value(principalKey{}) == "allowed", nil
			})
		})

	agent := llm.NewAgent[string]("agent", func(agent *llm.AgentBuilder[string]) { agent.Tools(tool) })

	var wg sync.WaitGroup

	for _, principal := range []string{"allowed", "denied"} {
		wg.Go(func() {
			fake := llmtest.New()
			fake.ReplyToolCall("guarded", `{}`)
			fake.Reply("done")

			ctx := context.WithValue(t.Context(), principalKey{}, principal)

			if err := availabilityRun(ctx, llm.New(fake, "model"), agent); err != nil {
				t.Error(err)

				return
			}

			advertised := len(fake.Generated()[0].Tools) == 1

			if advertised != (principal == "allowed") {
				t.Errorf("%s advertised = %v", principal, advertised)
			}
		})
	}

	wg.Wait()

	if checks.Load() != 4 || handlers.Load() != 1 {
		t.Fatalf("checks = %d, handlers = %d", checks.Load(), handlers.Load())
	}
}

// checkAvailabilityParentCancellation checks cancellation inside a
// successful execution policy ends the run with context.Canceled, not a policy
// failure or a permanent failure, and never invokes the handler.
func checkAvailabilityParentCancellation(t *testing.T, run func(context.Context, *llm.Model, llm.Agent[string]) error) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	var checks atomic.Int32
	tool := llm.NewTool("guarded", "Guarded.", orderSchema,
		func(context.Context, orderArgs) (string, error) { panic("handler") },
		func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) {
				if checks.Add(1) == 2 {
					cancel()
				}

				return true, nil
			})
		})

	fake := llmtest.New()
	fake.ReplyToolCall("guarded", `invalid`)

	agent := llm.NewAgent[string]("agent", func(agent *llm.AgentBuilder[string]) { agent.Tools(tool) })

	err := run(ctx, llm.New(fake, "model"), agent)

	if !errors.Is(err, context.Canceled) || errors.Is(err, llm.ErrPermanent) {
		t.Fatalf("error = %v", err)
	}

	if checks.Load() != 2 || len(fake.Generated()) != 1 {
		t.Fatalf("checks = %d, requests = %d", checks.Load(), len(fake.Generated()))
	}
}

// TestRunAvailabilityPreservesParentCancellation verifies Run retains context
// cancellation identity rather than classifying it as a policy failure.
func TestRunAvailabilityPreservesParentCancellation(t *testing.T) {
	t.Parallel()

	checkAvailabilityParentCancellation(t, availabilityRun)
}

// TestRunStreamAvailabilityPreservesParentCancellation verifies streaming ends
// with the parent's cancellation error without executing a canceled tool.
func TestRunStreamAvailabilityPreservesParentCancellation(t *testing.T) {
	t.Parallel()

	checkAvailabilityParentCancellation(t, availabilityStream)
}

// TestRunStreamCollectionDefersAndRepeatsIO preserves the same deferred,
// repeatable execution boundary for the agent loop rather than just one turn.
func TestRunStreamCollectionDefersAndRepeatsIO(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply("first")
	fake.Reply("second")

	stream := llm.New(fake, "m").RunStream(t.Context(), llm.NewAgent[string]("a"), userHistory())

	if len(fake.Generated()) != 0 {
		t.Fatal("construction invoked driver")
	}

	if _, err := stream.Items(); err != nil {
		t.Fatal(err)
	}

	if _, err := stream.Items(); err != nil {
		t.Fatal(err)
	}

	if len(fake.Generated()) != 2 {
		t.Fatalf("got %d calls, want 2", len(fake.Generated()))
	}
}

// TestRunStreamCollectionTakeCancelsWithoutOutcome checks that a native Take
// stops the agent as well as its active turn, with no invented run outcome.
func TestRunStreamCollectionTakeCancelsWithoutOutcome(t *testing.T) {
	t.Parallel()

	bus := event.New()
	outcomes := 0

	if _, err := bus.Listen(llm.AgentCompleted, func(context.Context, llm.AgentCompletedEvent) error {
		outcomes++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Listen(llm.AgentFailed, func(context.Context, llm.AgentFailedEvent) error {
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

	items, err := model.RunStream(t.Context(), llm.NewAgent[string]("a"), userHistory()).Take(1).Items()

	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, error = %v", items, err)
	}

	if !errors.Is(driver.streamCtx.Err(), context.Canceled) || outcomes != 0 {
		t.Fatalf("context = %v, outcomes = %d", driver.streamCtx.Err(), outcomes)
	}
}

// TestRunStreamCollectionProjectionPreservesMissingDone proves mapping the
// event view cannot turn a malformed driver stream into a completed agent run.
func TestRunStreamCollectionProjectionPreservesMissingDone(t *testing.T) {
	t.Parallel()

	driver := newStub()
	driver.events = []llm.Event{llm.TextDelta{Text: "partial"}}

	items, err := llm.New(driver, "m").RunStream(t.Context(), llm.NewAgent[string]("a"), userHistory()).Map(func(_ int, item llm.Event) (string, error) {
		return item.(llm.TextDelta).Text, nil
	}).Items()

	if !errors.Is(err, llm.ErrUnavailable) || !reflect.DeepEqual(items, []string{"partial"}) {
		t.Fatalf("items = %#v, error = %v", items, err)
	}
}

// TestRunStreamCollectionTakeJoinsAdmittedTools stops on the second tool start
// only after the first handler is running, then checks its cooperative exit was
// joined before materialization returned. No wall-clock scheduling is assumed.
func TestRunStreamCollectionTakeJoinsAdmittedTools(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	exited := make(chan struct{})
	tool := llm.NewTool("wait", "Wait for cancellation.", validate.Infer[struct{}](),
		func(ctx context.Context, _ struct{}) (string, error) {
			close(entered)
			<-ctx.Done()
			close(exited)

			return "", ctx.Err()
		})

	agent := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
		agent.ToolConcurrency(2)
	})

	fake := llmtest.New()
	fake.ReplyParts(
		llm.ToolCall{ID: "first", Name: "wait", Arguments: []byte(`{}`)},
		llm.ToolCall{ID: "second", Name: "wait", Arguments: []byte(`{}`)},
	)

	starts := llm.New(fake, "m").RunStream(t.Context(), agent, userHistory()).Filter(func(_ int, item llm.Event) (bool, error) {
		started, ok := item.(llm.ToolStarted)

		if ok && started.ID == "second" {
			<-entered
		}

		return ok, nil
	})

	if _, err := starts.Take(2).Items(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-exited:
	default:
		t.Fatal("stream returned before the admitted handler exited")
	}
}

// orderArgs is the argument type of the test tools.
type orderArgs struct {
	// Number is the order identifier validated before invoking the tool.
	Number string `json:"number"`
}

// orderSchema rejects missing or short order identifiers before tool execution.
var orderSchema = validate.Struct(func(object *validate.StructBuilder[orderArgs]) {
	object.Field("Number", func(number *validate.StringBuilder[string]) {
		number.Required()
		number.Min(3)
	})
})

// ticket is the structured output of the test agents.
type ticket struct {
	// Priority is the constrained value the agent must return as structured output.
	Priority string `json:"priority"`
}

// ticketSchema limits structured replies to the low and high priorities.
var ticketSchema = validate.Struct(func(object *validate.StructBuilder[ticket]) {
	object.Field("Priority", func(priority *validate.StringBuilder[string]) {
		priority.Required()
		priority.OneOf("low", "high")
	})
})

// echoTool returns a tool that answers with its order number.
func echoTool(name string) llm.Tool {
	return llm.NewTool(name, "Find an order.", orderSchema, func(_ context.Context, in orderArgs) (string, error) {
		return "order " + in.Number, nil
	})
}

// userHistory is a one-message conversation.
func userHistory() []llm.Message {
	return []llm.Message{llm.UserText("where is my order?")}
}

// toolResults returns the ToolResult parts of the last message.
func toolResults(t *testing.T, messages []llm.Message) []llm.ToolResult {
	t.Helper()

	var results []llm.ToolResult

	for _, part := range messages[len(messages)-1].Parts {
		if result, ok := part.(llm.ToolResult); ok {
			results = append(results, result)
		}
	}

	return results
}

// call builds a model-issued tool call from the literal JSON used by each test.
func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{
		ID:        id,
		Name:      name,
		Arguments: []byte(args),
	}
}

// TestRunLoopsUntilEndTurn follows a tool request through the final model reply.
// The agent must keep turning until the model ends the conversation.
func TestRunLoopsUntilEndTurn(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{"number":"A-42"}`)
	fake.Reply("It ships today.")

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Instructions("Be brief.")
		agent.Tools(echoTool("lookup"))
	})

	result, err := llm.New(fake, "gpt-5.2").Run(t.Context(), agent, userHistory())

	if err != nil {
		t.Fatal(err)
	}

	if result.Output != "It ships today." || result.Text != result.Output || result.Steps != 2 || result.StopReason != llm.StopEndTurn {
		t.Fatalf("result = %+v", result)
	}

	if len(result.Messages) != 4 {
		t.Fatalf("messages = %d, want history + call + result + reply", len(result.Messages))
	}

	results := toolResults(t, result.Messages[:3])

	if len(results) != 1 || results[0].Content != "order A-42" || results[0].IsError || results[0].CallID != "call-1" {
		t.Fatalf("tool results = %+v", results)
	}

	requests := fake.Generated()

	if requests[0].Instructions != "Be brief." || len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "lookup" || len(requests[0].Tools[0].InputSchema) == 0 {
		t.Fatalf("first request = %+v", requests[0])
	}
}

// TestRunDoesNotModifyHistory checks caller-owned conversation storage.
// Appending agent turns must leave the supplied history unchanged.
func TestRunDoesNotModifyHistory(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply("hi")

	history := userHistory()

	if _, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a"), history); err != nil {
		t.Fatal(err)
	}

	if len(history) != 1 {
		t.Fatalf("history has %d messages", len(history))
	}
}

// TestRunSumsUsageAcrossSteps checks aggregate token accounting.
// The final result must include usage from every model turn in the run.
func TestRunSumsUsageAcrossSteps(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.Part{call("c1", "lookup", `{"number":"A-42"}`)},
		},
		StopReason: llm.StopToolUse,
		Usage: llm.Usage{
			InputTokens:  10,
			OutputTokens: 2,
		},
	})
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.Part{llm.Text{Text: "ok"}},
		},
		StopReason: llm.StopEndTurn,
		Usage: llm.Usage{
			InputTokens:  15,
			OutputTokens: 3,
		},
	})

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
	}), userHistory())

	if err != nil {
		t.Fatal(err)
	}

	if result.Usage.InputTokens != 25 || result.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

// TestRunStopsAtStepLimit prevents an agent from making unbounded model calls.
// Reaching the configured limit must return ErrMaxSteps with the partial result.
func TestRunStopsAtStepLimit(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()

	for range 3 {
		fake.ReplyToolCall("lookup", `{"number":"A-42"}`)
	}

	agent := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
		agent.MaxSteps(2)
	})

	result, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory())

	if !errors.Is(err, llm.ErrMaxSteps) {
		t.Fatalf("error = %v, want ErrMaxSteps", err)
	}

	if result.Steps != 2 {
		t.Fatalf("steps = %d, want 2", result.Steps)
	}

	last := toolResults(t, result.Messages)

	if len(last) != 1 || !last[0].IsError {
		t.Fatalf("last message = %+v, want an error result so the history stays valid", result.Messages[len(result.Messages)-1])
	}
}

// TestRunStepLimitPrecedence checks overrides at model, agent, and call boundaries.
// The most specific configured limit must govern the run.
func TestRunStepLimitPrecedence(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()

	for range 10 {
		fake.ReplyToolCall("lookup", `{"number":"A-42"}`)
	}

	agent := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
		agent.MaxSteps(3)
	})

	model := llm.New(fake, "m", func(model *llm.ModelBuilder) {
		model.MaxSteps(5)
	})

	result, err := model.Run(t.Context(), agent, userHistory(), llm.MaxSteps(1))

	if !errors.Is(err, llm.ErrMaxSteps) || result.Steps != 1 {
		t.Fatalf("run option: steps = %d, error = %v", result.Steps, err)
	}

	result, err = model.Run(t.Context(), agent, userHistory())

	if !errors.Is(err, llm.ErrMaxSteps) || result.Steps != 3 {
		t.Fatalf("agent: steps = %d, error = %v", result.Steps, err)
	}

	modelLimited := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
		agent.MaxSteps(0)
	})

	result, err = model.Run(t.Context(), modelLimited, userHistory())

	if !errors.Is(err, llm.ErrMaxSteps) || result.Steps != 5 {
		t.Fatalf("config: steps = %d, error = %v", result.Steps, err)
	}
}

// TestRunTruncatedAndRefusedReturnResult checks the truncated terminal response.
// The truncation error must not discard the response already obtained from the model.
func TestRunTruncatedAndRefusedReturnResult(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.Part{llm.Text{Text: "partial"}},
		},
		StopReason: llm.StopMaxTokens,
	})

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a"), userHistory())

	if !errors.Is(err, llm.ErrTruncated) {
		t.Fatalf("%s: error = %v, want %v", llm.StopMaxTokens, err, llm.ErrTruncated)
	}

	partialResult := result.Text == "partial" && result.Steps == 1
	completeHistory := result.StopReason == llm.StopMaxTokens && len(result.Messages) == 2

	if !partialResult || !completeHistory {
		t.Fatalf("%s: result = %+v", llm.StopMaxTokens, result)
	}
}

// TestRunRefusalReturnsResult keeps the model's partial response on refusal.
// Callers must receive ErrRefused together with the completed turn's history.
func TestRunRefusalReturnsResult(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.Part{llm.Text{Text: "partial"}},
		},
		StopReason: llm.StopRefusal,
	})

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a"), userHistory())

	if !errors.Is(err, llm.ErrRefused) {
		t.Fatalf("%s: error = %v, want %v", llm.StopRefusal, err, llm.ErrRefused)
	}

	partialResult := result.Text == "partial" && result.Steps == 1
	completeHistory := result.StopReason == llm.StopRefusal && len(result.Messages) == 2

	if !partialResult || !completeHistory {
		t.Fatalf("%s: result = %+v", llm.StopRefusal, result)
	}
}

// TestRunContentFilterReturnsResult keeps partial output blocked by content filtering.
// Filtering must report ErrRefused without losing the completed turn's history.
func TestRunContentFilterReturnsResult(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.Part{llm.Text{Text: "partial"}},
		},
		StopReason: llm.StopContentFilter,
	})

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a"), userHistory())

	if !errors.Is(err, llm.ErrRefused) {
		t.Fatalf("%s: error = %v, want %v", llm.StopContentFilter, err, llm.ErrRefused)
	}

	partialResult := result.Text == "partial" && result.Steps == 1
	completeHistory := result.StopReason == llm.StopContentFilter && len(result.Messages) == 2

	if !partialResult || !completeHistory {
		t.Fatalf("%s: result = %+v", llm.StopContentFilter, result)
	}
}

// TestRunInvalidArgumentsGoBackToModel covers tool argument validation failures.
// The model must receive a tool result explaining why execution was rejected.
func TestRunInvalidArgumentsGoBackToModel(t *testing.T) {
	t.Parallel()

	var called atomic.Bool

	tool := llm.NewTool("lookup", "Find.", orderSchema, func(context.Context, orderArgs) (string, error) {
		called.Store(true)

		return "", nil
	})

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{"number":"x"}`)
	fake.ReplyToolCall("lookup", `{"number":`)
	fake.Reply("sorry")

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
	}), userHistory())

	if err != nil {
		t.Fatal(err)
	}

	if called.Load() {
		t.Fatal("tool ran with invalid arguments")
	}

	invalid := toolResults(t, result.Messages[:3])

	if len(invalid) != 1 || !invalid[0].IsError || !strings.Contains(invalid[0].Content, "/number") {
		t.Fatalf("validation result = %+v, want the field message", invalid)
	}

	malformed := toolResults(t, result.Messages[:5])

	if len(malformed) != 1 || !malformed[0].IsError || !strings.Contains(malformed[0].Content, "invalid arguments") {
		t.Fatalf("decoding result = %+v", malformed)
	}
}

// TestRunUnknownToolGoesBackToModel covers calls to an unregistered tool.
// The agent must return a failed tool result so the model can recover.
func TestRunUnknownToolGoesBackToModel(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyToolCall("missing", `{}`)
	fake.Reply("recovered")

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
	}), userHistory())

	if err != nil || result.Output != "recovered" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}

	if results := toolResults(t, result.Messages[:3]); len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %+v", results)
	}
}

// TestRunFailureAfterUnknownToolMatchesErrUnknownTool checks chained failures.
// A later model failure must preserve the earlier unknown-tool classification.
func TestRunFailureAfterUnknownToolMatchesErrUnknownTool(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyToolCall("missing", `{}`)
	fake.Reply("again")

	_, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.MaxSteps(1)
	}), userHistory())

	if !errors.Is(err, llm.ErrMaxSteps) || !errors.Is(err, llm.ErrUnknownTool) {
		t.Fatalf("error = %v, want ErrMaxSteps and ErrUnknownTool", err)
	}
}

// TestRunStructuredOutput checks the agent's final schema and typed result.
// A valid JSON reply must decode into the declared output type.
func TestRunStructuredOutput(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply(`{"priority":"high"}`)

	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
	})

	result, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory())

	if err != nil {
		t.Fatal(err)
	}

	if result.Output.Priority != "high" {
		t.Fatalf("output = %+v", result.Output)
	}

	if len(fake.Generated()[0].Output) == 0 {
		t.Fatal("the output schema was not sent")
	}
}

// TestRunStructuredOutputRetriesWithValidationErrors checks model correction.
// Invalid output must produce feedback before the next schema-constrained turn.
func TestRunStructuredOutputRetriesWithValidationErrors(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply(`{"priority":"urgent"}`)
	fake.Reply(`{"priority":"low"}`)

	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
	})

	result, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory())

	if err != nil {
		t.Fatal(err)
	}

	if result.Output.Priority != "low" || result.Steps != 2 {
		t.Fatalf("result = %+v", result)
	}

	retry, ok := fake.Generated()[1].Messages[2].Parts[0].(llm.Text)

	if !ok || !strings.Contains(retry.Text, "/priority") {
		t.Fatalf("retry message = %+v, want the validation errors", fake.Generated()[1].Messages[2])
	}
}

// TestRunStructuredOutputFailsWithErrInvalidOutput covers exhausted corrections.
// A final invalid reply must retain the output-validation sentinel.
func TestRunStructuredOutputFailsWithErrInvalidOutput(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply(`{"priority":"urgent"}`)
	fake.Reply(`not json`)

	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
	})

	result, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory())

	if !errors.Is(err, llm.ErrInvalidOutput) {
		t.Fatalf("error = %v, want ErrInvalidOutput", err)
	}

	if result.Steps != 2 {
		t.Fatalf("steps = %d, want the original turn and one retry", result.Steps)
	}
}

// TestRunStructuredOutputNegativeRetriesNeverRetries checks disabled correction.
// The first invalid reply must end the run without another model call.
func TestRunStructuredOutputNegativeRetriesNeverRetries(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply(`{"priority":"urgent"}`)

	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
		agent.OutputRetries(-1)
	})

	if _, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory()); !errors.Is(err, llm.ErrInvalidOutput) {
		t.Fatalf("error = %v, want ErrInvalidOutput", err)
	}
}

// TestRunReturnsDefinitionErrorBeforeIO checks the agent publication boundary.
// An invalid definition must fail before consuming a scripted model reply.
func TestRunReturnsDefinitionErrorBeforeIO(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()

	_, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("Bad Name"), userHistory())

	if !errors.Is(err, llm.ErrInvalidName) || len(fake.Generated()) != 0 {
		t.Fatalf("error = %v, requests = %d", err, len(fake.Generated()))
	}

	//nolint:staticcheck // a nil context is the case under test.
	if _, err := llm.New(fake, "m").Run(nil, llm.NewAgent[string]("a"), userHistory()); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("nil context: %v", err)
	}
}

// TestRunOptionsOverrideAgent checks settings applied to a single execution.
// They must override agent defaults without changing the published definition.
func TestRunOptionsOverrideAgent(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply("hi")

	agent := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Instructions("Base.")
		agent.MaxTokens(100)
	})

	_, err := llm.New(fake, "gpt-5.2").Run(t.Context(), agent, userHistory(),
		llm.Instructions("User: Ana"), llm.UseModel("gpt-5.2-mini"), llm.MaxTokens(7))

	if err != nil {
		t.Fatal(err)
	}

	request := fake.Generated()[0]

	if request.Model != "gpt-5.2-mini" || request.MaxTokens != 7 || request.Instructions != "Base.\n\nUser: Ana" {
		t.Fatalf("request = model %q, max tokens %d, instructions %q", request.Model, request.MaxTokens, request.Instructions)
	}
}

// TestRunStreamStructuredOutputRetries ensures callback-based output settings
// still validate streamed final replies and count a corrected reply as a step.
func TestRunStreamStructuredOutputRetries(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply(`{"priority":"urgent"}`)
	fake.Reply(`{"priority":"high"}`)

	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
		agent.OutputRetries(1)
	})

	steps := 0

	for events, err := range llm.New(fake, "m").RunStream(t.Context(), agent, userHistory()) {
		if err != nil {
			t.Fatal(err)
		}

		if _, ok := events.(llm.StepCompleted); ok {
			steps++
		}
	}

	if steps != 2 || len(fake.Generated()) != 2 {
		t.Fatalf("steps = %d, requests = %d", steps, len(fake.Generated()))
	}
}

// TestRunStreamEmitsLoopEventsInOrder checks model, tool, and step notifications.
// Consumers must observe the same causal order as the agent execution.
func TestRunStreamEmitsLoopEventsInOrder(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{"number":"A-42"}`)
	fake.Reply("done")

	var names []string

	for events, err := range llm.New(fake, "m").RunStream(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
	}), userHistory()) {
		if err != nil {
			t.Fatal(err)
		}

		switch events := events.(type) {
		case llm.ToolCallStarted:
			names = append(names, "call-started")
		case llm.Done:
			names = append(names, "done:"+string(events.Response.StopReason))
		case llm.ToolStarted:
			names = append(names, "invoked:"+events.Name)
		case llm.ToolFinished:
			names = append(names, "completed:"+events.Name)
		case llm.StepCompleted:
			names = append(names, "step")
		case llm.TextDelta:
			names = append(names, "text")
		}
	}

	want := []string{"call-started", "done:tool_use", "invoked:lookup", "completed:lookup", "step", "text", "done:end_turn", "step"}

	if !reflect.DeepEqual(names, want) {
		t.Fatalf("events = %v, want %v", names, want)
	}
}

// TestRunStreamYieldsFailureAndStopsOnBreak covers failing and abandoned streams.
// No further agent work should continue once the consumer ends iteration.
func TestRunStreamYieldsFailureAndStopsOnBreak(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.Part{llm.Text{Text: "x"}},
		},
		StopReason: llm.StopRefusal,
	})

	var got error

	for _, err := range llm.New(fake, "m").RunStream(t.Context(), llm.NewAgent[string]("a"), userHistory()) {
		got = err
	}

	if !errors.Is(got, llm.ErrRefused) {
		t.Fatalf("error = %v, want ErrRefused", got)
	}

	fake.Reset()
	fake.ReplyToolCall("lookup", `{"number":"A-42"}`)
	fake.Reply("never")

	for range llm.New(fake, "m").RunStream(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
	}), userHistory()) {
		break
	}

	if len(fake.Generated()) != 1 {
		t.Fatalf("requests = %d, want the run to stop after the break", len(fake.Generated()))
	}
}

// TestResultMessagesRoundTripAsJSON checks persisted agent conversation history.
// Tool calls and results must survive the same encoding as ordinary messages.
func TestResultMessagesRoundTripAsJSON(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{"number":"A-42"}`)
	fake.Reply("done")

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
	}), userHistory())

	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(result.Messages)

	if err != nil {
		t.Fatal(err)
	}

	var decoded []llm.Message

	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(decoded, result.Messages) {
		t.Fatalf("decoded = %+v, want %+v", decoded, result.Messages)
	}
}

// TestRunStreamEventsRoundTripAsJSON checks the wire form of an executed stream.
// Each emitted event must preserve its concrete type and payload through JSON.
func TestRunStreamEventsRoundTripAsJSON(t *testing.T) {
	t.Parallel()

	events := []llm.Event{
		llm.ToolStarted{
			ID:   "1",
			Name: "lookup",
		},
		llm.ToolFinished{
			ID:      "1",
			Name:    "lookup",
			IsError: true,
		},
		llm.StepCompleted{
			Step:       2,
			StopReason: llm.StopToolUse,
			Usage:      llm.Usage{InputTokens: 3},
		},
	}

	for _, events := range events {
		data, err := json.Marshal(events)

		if err != nil {
			t.Fatal(err)
		}

		decoded, err := llm.UnmarshalEvent(data)

		if err != nil || !reflect.DeepEqual(decoded, events) {
			t.Fatalf("round trip of %+v = %+v, %v", events, decoded, err)
		}
	}
}
