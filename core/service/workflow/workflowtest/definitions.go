package workflowtest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/core/service/workflow"
)

// Order is the input of the typed test.
// It exercises object decoding independently of the workflow's result type.
//
// Example:
//
//	order := workflowtest.Order{
//		Item: "tea",
//		Qty:  4,
//	}
type Order struct {
	// Item is copied into the receipt summary by the typed conformance flow.
	//
	// Example:
	//
	//	order := workflowtest.Order{
	//		Item: "tea",
	//	}
	//	item := order.Item
	Item string

	// Qty is doubled to produce the receipt's total during the typed test.
	//
	// Example:
	//
	//	order := workflowtest.Order{
	//		Qty: 4,
	//	}
	//	quantity := order.Qty
	Qty int
}

// Receipt is the output of the typed test.
// It verifies that the driver returns an object distinct from the input shape.
//
// Example:
//
//	receipt := workflowtest.Receipt{
//		Summary: "tea",
//		Total:   8,
//	}
type Receipt struct {
	// Summary echoes the input's Item to verify typed result decoding.
	//
	// Example:
	//
	//	receipt := workflowtest.Receipt{
	//		Summary: "tea",
	//	}
	//	summary := receipt.Summary
	Summary string

	// Total is twice the input quantity in the suite's typed workflow.
	//
	// Example:
	//
	//	receipt := workflowtest.Receipt{
	//		Total: 8,
	//	}
	//	total := receipt.Total
	Total int
}

// maxDuration bounds ordinary conformance workflows while allowing the short
// sleeps and retries needed to exercise engine behavior.
const maxDuration = 10 * time.Second

var (
	// typedFlow converts an Order to a Receipt to test distinct typed payloads.
	typedFlow = workflow.New("conf-typed", func(_ *workflow.Context, in Order) (Receipt, error) {
		return Receipt{
			Summary: in.Item,
			Total:   in.Qty * 2,
		}, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// duplicateReady keeps duplicateFlow active until the test releases it.
	duplicateReady = workflow.NewSignal[string]("go")

	// duplicateFlow makes concurrent duplicate submissions observable.
	duplicateFlow = workflow.New("conf-duplicate", func(c *workflow.Context, in string) (string, error) {
		value, err := c.Wait(duplicateReady)

		return in + value, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// retryCalls and permanentCalls count step calls by the run id that is the
	// input of the step.
	retryCalls sync.Map
	// retryStep succeeds on its third call for each unique run input.
	retryStep = workflow.NewStep("conf-flaky", func(_ context.Context, in string) (string, error) {
		if counter(&retryCalls, in).Add(1) < 3 {
			return "", errors.New("flaky")
		}

		return in + "-ok", nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(time.Millisecond, 1, time.Millisecond)
	})
	// retryFlow delegates to retryStep so the engine must apply its retry policy.
	retryFlow = workflow.New("conf-retry", func(c *workflow.Context, in string) (string, error) {
		return c.Step(retryStep, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// permanentCalls counts fatal-step executions by unique run input.
	permanentCalls sync.Map

	// permanentStep fails permanently even though its policy allows retries.
	permanentStep = workflow.NewStep("conf-fatal", func(_ context.Context, in string) (string, error) {
		counter(&permanentCalls, in).Add(1)

		return "", workflow.Permanent(workflow.NewError("conf-fatal", "do not retry"))
	}, func(step *workflow.StepBuilder) {
		step.Retry(5)
		step.Backoff(time.Millisecond, 1, time.Millisecond)
	})
	// permanentFlow exposes the fatal step result at the workflow boundary.
	permanentFlow = workflow.New("conf-permanent", func(c *workflow.Context, in string) (string, error) {
		return c.Step(permanentStep, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// slowStep cooperates with cancellation but outlasts its attempt timeout.
	slowStep = workflow.NewStep("conf-slow", func(ctx context.Context, _ string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "late", nil
		}
	}, func(step *workflow.StepBuilder) {
		step.Timeout(50 * time.Millisecond)
	})
	// stepTimeoutFlow separates an attempt timeout from the whole-run bound.
	stepTimeoutFlow = workflow.New("conf-step-timeout", func(c *workflow.Context, in string) (string, error) {
		return c.Step(slowStep, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// childFlow multiplies typed input so parent/child result flow is observable.
	childFlow = workflow.New("conf-child", func(_ *workflow.Context, in int) (int, error) {
		return in * 10, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})
	// parentFlow waits for childFlow and transforms its returned value.
	parentFlow = workflow.New("conf-parent", func(c *workflow.Context, in int) (int, error) {
		got, err := c.Child(childFlow, c.ID()+"-child", in)

		return got + 1, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// sleepFlow compares runtime time before and after a durable sleep.
	sleepFlow = workflow.New("conf-sleep", func(c *workflow.Context, millis int) (bool, error) {
		d := time.Duration(millis) * time.Millisecond
		started := c.Now()

		if err := c.Sleep(d); err != nil {
			return false, err
		}

		return c.Now().Sub(started) >= d, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// earlySignal is delivered while waitBeforeFlow is still sleeping.
	earlySignal = workflow.NewSignal[string]("early")

	// waitBeforeFlow tests buffering of a signal received before Wait begins.
	waitBeforeFlow = workflow.New("conf-wait-before", func(c *workflow.Context, _ string) (string, error) {
		if err := c.Sleep(100 * time.Millisecond); err != nil {
			return "", err
		}

		return c.Wait(earlySignal)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// lateSignal carries a typed object to a workflow already waiting for it.
	lateSignal = workflow.NewSignal[Order]("late")

	// waitAfterFlow tests wakeup and decoding of a signal arriving after Wait.
	waitAfterFlow = workflow.New("conf-wait-after", func(c *workflow.Context, _ string) (Order, error) {
		return c.Wait(lateSignal)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// neverSignal intentionally receives no value in timeout/cancellation tests.
	neverSignal = workflow.NewSignal[string]("never")

	// waitTimeoutFlow converts the expected wait timeout into a stable result.
	waitTimeoutFlow = workflow.New("conf-wait-timeout", func(c *workflow.Context, _ string) (string, error) {
		if _, err := c.Wait(neverSignal, workflow.Timeout(50*time.Millisecond)); errors.Is(err, workflow.ErrTimeout) {
			return "timed out", nil
		}

		return "unexpected", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// valueSignal is sent repeatedly to test first-value signal resolution.
	valueSignal = workflow.NewSignal[string]("value")

	// resolveOnceFlow must receive the first value from both consecutive waits.
	resolveOnceFlow = workflow.New("conf-resolve-once", func(c *workflow.Context, _ string) (string, error) {
		first, err := c.Wait(valueSignal)

		if err != nil {
			return "", err
		}

		second, err := c.Wait(valueSignal)

		return first + "," + second, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// pickSignal competes with a timer in the two Select scenarios.
	pickSignal = workflow.NewSignal[string]("pick")

	// selectSignalFlow gives the test enough time to make the signal win.
	selectSignalFlow = selectWorkflow("conf-select-signal", 5*time.Second)

	// selectTimerFlow uses a short timer with no competing signal delivery.
	selectTimerFlow = selectWorkflow("conf-select-timer", 50*time.Millisecond)

	// cancelFlow stays blocked so explicit cancellation must release its wait.
	cancelFlow = workflow.New("conf-cancel", func(c *workflow.Context, _ string) (string, error) {
		return c.Wait(neverSignal)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// unknownSignal provides a valid identity for unknown-run signal assertions.
	unknownSignal = workflow.NewSignal[string]("any")

	// unknownFlow provides a registered definition without starting the target ID.
	unknownFlow = workflow.New("conf-unknown", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// errorKind compares custom failures by kind rather than their message.
	errorKind = workflow.NewError("conf-custom", "")

	// errorKindFlow returns a custom kind that must survive the engine boundary.
	errorKindFlow = workflow.New("conf-error-kind", func(*workflow.Context, string) (string, error) {
		return "", workflow.NewError("conf-custom", "boom")
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// panicFlow checks application panic conversion into a failed run.
	panicFlow = workflow.New("conf-panic", func(*workflow.Context, string) (string, error) {
		panic("conf-boom")
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// spawnFlow spawns a duplicateFlow run twice under one id and returns it.
	spawnFlow = workflow.New("conf-spawn", func(c *workflow.Context, in string) (string, error) {
		id := c.ID() + "-spawned"

		first, err := c.Spawn(duplicateFlow, id, in)

		if err != nil {
			return "", err
		}

		second, err := c.Spawn(duplicateFlow, id, in)

		if err != nil || second != first {
			return "", errors.Join(err, errors.New("second Spawn changed the id"))
		}

		return first, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// childFailFlow returns a permanent custom failure for parent propagation.
	childFailFlow = workflow.New("conf-child-fail", func(*workflow.Context, string) (string, error) {
		return "", workflow.Permanent(workflow.NewError("conf-custom", "child boom"))
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})
	// childFailParentFlow exposes the failed child's kind and permanent marker.
	childFailParentFlow = workflow.New("conf-child-fail-parent", func(c *workflow.Context, _ string) (string, error) {
		return c.Child(childFailFlow, c.ID()+"-child", "")
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// joinFlow runs the duplicateFlow run whose id is the input as a child.
	joinFlow = workflow.New("conf-join", func(c *workflow.Context, in string) (string, error) {
		return c.Child(duplicateFlow, in, "ignored")
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// selfJoinFlow is initialized after declaration so its body can refer to it.
	selfJoinFlow *workflow.Workflow[string, string]

	// ctxErrSeen records by run id first the string "started", then what
	// Context.Err returned after cancel.
	ctxErrSeen sync.Map
	// ctxErrFlow records the cancellation error observed through durable calls.
	ctxErrFlow = workflow.New("conf-ctx-err", func(c *workflow.Context, _ string) (string, error) {
		ctxErrSeen.Store(c.ID(), "started")

		// Wait durably; blocking on Done is not replay-safe on every engine.
		for c.Err() == nil {
			if err := c.Sleep(10 * time.Millisecond); err != nil {
				break
			}
		}

		ctxErrSeen.Store(c.ID(), c.Err())

		return "", c.Err()
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// delayFlow returns immediately once the driver's start delay has elapsed.
	delayFlow = workflow.New("conf-delay", func(*workflow.Context, string) (string, error) {
		return "ran", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// unregisteredFlow is deliberately missing from Definitions.
	unregisteredFlow = workflow.New("conf-unregistered", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// maxDurationFlow stays blocked beyond its short whole-run timeout.
	maxDurationFlow = workflow.New("conf-max-duration", func(c *workflow.Context, _ string) (string, error) {
		return c.Wait(neverSignal)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(100 * time.Millisecond)
	})
)

// init publishes the self-referencing definition after its pointer variable
// exists. The body runs only when a driver starts the conformance workflow.
func init() {
	selfJoinFlow = workflow.New("conf-self-join", func(c *workflow.Context, _ string) (string, error) {
		if _, err := c.Child(selfJoinFlow, c.ID(), ""); !errors.Is(err, workflow.ErrInvalidID) {
			return "", errors.New("self Child was not rejected")
		}

		return "rejected", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})
}

// selectWorkflow races the signal against a timer and reports the winner.
func selectWorkflow(name string, after time.Duration) *workflow.Workflow[string, string] {
	return workflow.New(name, func(c *workflow.Context, _ string) (string, error) {
		var winner string

		err := c.Select(
			workflow.On(pickSignal, func(value string) {
				winner = "signal:" + value
			}),
			workflow.After(after, func() {
				winner = "timer"
			}),
		)

		return winner, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})
}

// counter returns the shared atomic call counter for a unique test run input.
// Only *atomic.Int32 values are stored in these maps, so the assertion is safe.
func counter(m *sync.Map, key string) *atomic.Int32 {
	value, _ := m.LoadOrStore(key, new(atomic.Int32))

	return value.(*atomic.Int32) //nolint:forcetypeassert // only counters are stored
}

// Definitions returns every workflow and step the suite runs. Register all of
// them in the driver that is passed to [Run].
// The returned slice is independent; definitions remain immutable shared values.
// Include them even when a suite option requires rejection of a particular policy.
//
//	definitions := workflowtest.Definitions()
//	registry, err := workflow.NewRegistry(definitions)
//	if err != nil {
//		return err
//	}
//	_ = registry.Steps()
func Definitions() []workflow.Definition {
	return []workflow.Definition{
		typedFlow, duplicateFlow, retryStep, retryFlow, permanentStep, permanentFlow,
		immediateEntry, immediateStep, immediateFlow, immediateOnceStep, immediateOnceFlow,
		slowStep, stepTimeoutFlow, childFlow, parentFlow, sleepFlow, waitBeforeFlow,
		waitAfterFlow, waitTimeoutFlow, resolveOnceFlow, selectSignalFlow,
		selectTimerFlow, cancelFlow, unknownFlow, errorKindFlow, maxDurationFlow,
		panicFlow, spawnFlow, childFailFlow, childFailParentFlow, joinFlow, selfJoinFlow,
		ctxErrFlow, delayFlow, traceStep, traceFlow, traceChildFlow, traceSpawnFlow,
		ownershipEntry, ownershipChild, ownershipParent, ownershipTimeoutParent,
	}
}

// Registry returns the registry of [Definitions] together with extra, for
// newDriver to give to the driver under test. It fails the test when the
// definitions are invalid.
// Worker registration uses an independently started recording telemetry service
// so serialized start/child/spawn propagation can be checked. Cleanup stops it.
//
//	registry := workflowtest.Registry(t, applicationFlow, applicationStep)
//	driver := memoryworkflow.New(registry)
//	t.Cleanup(func() {
//		if err := driver.Stop(context.Background()); err != nil {
//			t.Error(err)
//		}
//	})
func Registry(t testing.TB, extra ...workflow.Definition) *workflow.Registry {
	t.Helper()

	service := telemetrytest.New().Telemetry(t)

	registry, err := workflow.NewRegistry(append(Definitions(), extra...), func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(service)
	})

	if err != nil {
		t.Fatalf("workflowtest: Handlers: %v", err)
	}

	return registry
}
