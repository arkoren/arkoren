package llm_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/foundation/validate"
)

// TestRunPreservesInitiatingAvailabilityFailure distinguishes a fatal policy
// failure from an earlier sibling's permanent response to its cancellation.
func TestRunPreservesInitiatingAvailabilityFailure(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	slow := llm.NewTool("slow", "Wait for cancellation.", validate.Infer[struct{}](),
		func(ctx context.Context, _ struct{}) (string, error) {
			close(started)
			<-ctx.Done()

			return "", llm.Permanent(ctx.Err())
		})

	cause := errors.New("availability backend failed")
	var checks atomic.Int32
	guarded := llm.NewTool("guarded", "Check capability.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			return "must not run", nil
		}, func(action *llm.ToolBuilder) {
			action.Available(func(context.Context) (bool, error) {
				if checks.Add(1) == 1 {
					return true, nil
				}

				<-started

				return false, cause
			})
		})

	fake := llmtest.New()
	fake.ReplyParts(
		llm.ToolCall{ID: "slow", Name: "slow", Arguments: []byte(`{}`)},
		llm.ToolCall{ID: "guarded", Name: "guarded", Arguments: []byte(`{}`)},
	)

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(slow, guarded)
	})

	_, err := llm.New(fake, "model").Run(t.Context(), agent, []llm.Message{llm.UserText("Help.")})

	if !errors.Is(err, llm.ErrToolAvailability) || !errors.Is(err, cause) {
		t.Fatalf("initiating policy failure was replaced: %v", err)
	}

	if !errors.Is(err, llm.ErrPermanent) {
		t.Fatalf("policy failure lost its permanent classification: %v", err)
	}
}

// TestRunStreamDoesNotAnnounceUnadmittedTools ensures a queued sibling is not
// reported as started after the first admitted call permanently ends the turn.
func TestRunStreamDoesNotAnnounceUnadmittedTools(t *testing.T) {
	t.Parallel()

	cause := errors.New("stop this turn")
	tool := llm.NewTool("stop", "Stop the turn.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			return "", llm.Permanent(cause)
		})

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
		agent.ToolConcurrency(1)
	})

	fake := llmtest.New()
	fake.ReplyParts(
		llm.ToolCall{ID: "first", Name: "stop", Arguments: []byte(`{}`)},
		llm.ToolCall{ID: "queued", Name: "stop", Arguments: []byte(`{}`)},
	)
	var started []string
	var failure error

	for event, err := range llm.New(fake, "model").RunStream(t.Context(), agent, []llm.Message{llm.UserText("Help.")}) {
		if err != nil {
			failure = err
		}

		if event, ok := event.(llm.ToolStarted); ok {
			started = append(started, event.ID)
		}
	}

	if !errors.Is(failure, cause) {
		t.Fatalf("permanent failure lost: %v", failure)
	}

	if len(started) != 1 || started[0] != "first" {
		t.Fatalf("unadmitted call announced as started: %v", started)
	}
}

// TestRunStreamStopWinsConcurrentFatalFailure keeps the iterator contract when
// an admitted sibling fails while the consumer is stopping on another start.
func TestRunStreamStopWinsConcurrentFatalFailure(t *testing.T) {
	t.Parallel()

	allowed := make(chan struct{})
	contexts := make(chan context.Context, 1)
	tool := llm.NewTool("stop", "Stop this turn.", validate.Infer[struct{}](),
		func(ctx context.Context, _ struct{}) (string, error) {
			contexts <- ctx
			<-allowed

			return "", llm.Permanent(errors.New("fatal failure"))
		})

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
	})

	fake := llmtest.New()
	fake.ReplyParts(
		llm.ToolCall{ID: "first", Name: "stop", Arguments: []byte(`{}`)},
		llm.ToolCall{ID: "queued", Name: "stop", Arguments: []byte(`{}`)},
	)

	for item, err := range llm.New(fake, "model").RunStream(t.Context(), agent, []llm.Message{llm.UserText("Help.")}) {
		if err != nil {
			t.Fatal(err)
		}

		if started, ok := item.(llm.ToolStarted); ok && started.ID == "queued" {
			close(allowed)

			ctx := <-contexts
			<-ctx.Done()

			break
		}
	}
}

// TestRunPermanentCancelsSiblingsAndFails covers fatal parallel tool execution.
// A permanent failure must cancel sibling work and stop further model turns.
func TestRunPermanentCancelsSiblingsAndFails(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	canceled := make(chan struct{})

	slow := llm.NewTool("slow", "x", orderSchema, func(ctx context.Context, _ orderArgs) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)

		return "", ctx.Err()
	})

	fatal := llm.NewTool("fatal", "x", orderSchema, func(context.Context, orderArgs) (string, error) {
		<-started

		return "", llm.Permanent(errors.New("revoked"))
	})

	fake := llmtest.New()
	fake.ReplyParts(call("1", "slow", `{"number":"A-42"}`), call("2", "fatal", `{"number":"A-42"}`))

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(slow, fatal)
	}), userHistory())

	if !errors.Is(err, llm.ErrPermanent) || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("error = %v, want ErrPermanent", err)
	}

	<-canceled

	if result.Steps != 1 || len(fake.Generated()) != 1 {
		t.Fatalf("steps = %d, requests = %d, want the run to stop", result.Steps, len(fake.Generated()))
	}
}

// TestRunToolsOfOneTurnRunConcurrently checks the default tool execution policy.
// Independent tool calls must be able to enter their handlers together.
func TestRunToolsOfOneTurnRunConcurrently(t *testing.T) {
	t.Parallel()

	var arrived atomic.Int32

	both := make(chan struct{})

	rendezvous := llm.NewTool("meet", "x", orderSchema, func(_ context.Context, in orderArgs) (string, error) {
		if arrived.Add(1) == 2 {
			close(both)
		}

		<-both

		return in.Number, nil
	})

	fake := llmtest.New()
	fake.ReplyParts(call("1", "meet", `{"number":"aaa"}`), call("2", "meet", `{"number":"bbb"}`))
	fake.Reply("done")

	if _, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(rendezvous)
	}), userHistory()); err != nil {
		t.Fatal(err)
	}
}

// TestRunToolConcurrencyOneRunsCallsOneAtATime checks an explicit serial limit.
// The second handler must not enter while the first handler remains active.
func TestRunToolConcurrencyOneRunsCallsOneAtATime(t *testing.T) {
	t.Parallel()

	var inFlight, maxInFlight atomic.Int32

	var order []string

	serial := llm.NewTool("serial", "x", orderSchema, func(_ context.Context, in orderArgs) (string, error) {
		now := inFlight.Add(1)
		defer inFlight.Add(-1)

		maxInFlight.Store(max(maxInFlight.Load(), now))

		order = append(order, in.Number)

		return in.Number, nil
	})

	fake := llmtest.New()
	fake.ReplyParts(call("1", "serial", `{"number":"aaa"}`), call("2", "serial", `{"number":"bbb"}`), call("3", "serial", `{"number":"ccc"}`))
	fake.Reply("done")

	agent := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(serial)
		agent.ToolConcurrency(1)
	})

	if _, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory()); err != nil {
		t.Fatal(err)
	}

	if maxInFlight.Load() != 1 || !reflect.DeepEqual(order, []string{"aaa", "bbb", "ccc"}) {
		t.Fatalf("max in flight = %d, order = %v", maxInFlight.Load(), order)
	}
}

// TestRunResultsKeepCallOrder checks publication after out-of-order completion.
// Tool results must retain the order of the model's original calls.
func TestRunResultsKeepCallOrder(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	first := llm.NewTool("first", "x", orderSchema, func(context.Context, orderArgs) (string, error) {
		<-release

		return "one", nil
	})

	second := llm.NewTool("second", "x", orderSchema, func(context.Context, orderArgs) (string, error) {
		defer close(release)

		return "two", nil
	})

	fake := llmtest.New()
	fake.ReplyParts(call("1", "first", `{"number":"aaa"}`), call("2", "second", `{"number":"bbb"}`))
	fake.Reply("done")

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(first, second)
	}), userHistory())

	if err != nil {
		t.Fatal(err)
	}

	results := toolResults(t, result.Messages[:3])

	if len(results) != 2 || results[0].CallID != "1" || results[0].Content != "one" || results[1].CallID != "2" || results[1].Content != "two" {
		t.Fatalf("results = %+v, want the order of the calls", results)
	}
}
