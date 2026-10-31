package temporalworkflow_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/workflowtest"
	"arkoren.dev/driver/temporal"
	"arkoren.dev/driver/temporal/internal/itest"
	"arkoren.dev/driver/temporal/temporalworkflow"
)

// worker starts a host with a worker for registry on its own task queue and
// returns the runner.
func worker(t *testing.T, queue string, registry *workflow.Registry) *temporalworkflow.Runner {
	t.Helper()

	host := temporal.New(func(host *temporal.Builder) {
		host.HostPort(itest.HostPort(t))
	}, func(host *temporal.Builder) {
		host.TaskQueue(queue)
	})

	runner, err := temporalworkflow.NewRunner(host, registry)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = host.Stop(context.Background())
	})

	return runner
}

// clientOnly returns a driver on a host without a worker.
func clientOnly(t *testing.T, queue string) workflow.Driver {
	t.Helper()

	host := temporal.New(func(host *temporal.Builder) {
		host.HostPort(itest.HostPort(t))
	}, func(host *temporal.Builder) {
		host.TaskQueue(queue)
	}, func(host *temporal.Builder) {
		host.NoWorker(true)
	})

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = host.Stop(context.Background())
	})

	driver, err := temporalworkflow.NewDriver(host)

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// runID returns an id that does not collide with earlier runs, because the
// server keeps its history.
func runID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// TestIntegration runs the workflow contract against the native engine using
// the shared integration setup.
func TestIntegration(t *testing.T) {
	workflowtest.Run(t, func(t *testing.T) workflow.Driver {
		return worker(t, "conformance", workflowtest.Registry(t)).Driver()
	}, workflowtest.WithoutImmediateStepRetries())
}

// TestIntegrationUncappedRetryPolicy verifies the server accepts the requested
// maximum and performs the configured finite retry sequence. Native duration
// limits still apply; protocol tests inspect the value written to the command.
func TestIntegrationUncappedRetryPolicy(t *testing.T) {
	var attempts atomic.Int32
	step := workflow.NewStep("uncapped-step", func(context.Context, string) (string, error) {
		if attempts.Add(1) == 1 {
			return "", errors.New("retry once")
		}

		return "ok", nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(2)
		step.Backoff(time.Millisecond, 2, 0)
	})
	definition := workflow.New("uncapped", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	api := workflow.NewClient(worker(t, runID("uncapped"), registry).Driver())

	run, err := api.Start(t.Context(), definition, runID("uncapped"), "input")

	if err != nil {
		t.Fatal(err)
	}

	result, err := run.Result(t.Context())

	if err != nil || result != "ok" {
		t.Fatalf("Result = %q, %v; want ok", result, err)
	}

	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d; want two", attempts.Load())
	}
}

// TestIntegrationUnlimitedStepUsesWorkflowBound verifies the server accepts an
// unlimited attempt backed only by the enclosing workflow's execution budget.
// Protocol coverage proves no hidden one-minute start-to-close timeout is sent.
func TestIntegrationUnlimitedStepUsesWorkflowBound(t *testing.T) {
	step := workflow.NewStep("unlimited-step", func(_ context.Context, in string) (string, error) {
		return in, nil
	}, func(step *workflow.StepBuilder) {
		step.Timeout(0)
	})
	definition := workflow.New("unlimited", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(2 * time.Hour)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	api := workflow.NewClient(worker(t, runID("unlimited"), registry).Driver())

	run, err := api.Start(t.Context(), definition, runID("unlimited"), "input")

	if err != nil {
		t.Fatal(err)
	}

	result, err := run.Result(t.Context())

	if err != nil || result != "input" {
		t.Fatalf("Result = %q, %v; want input", result, err)
	}
}

// TestIntegrationRunnerRejectsNoWorkerHost verifies that the real client-only
// host cannot be used to construct a worker runner.
func TestIntegrationRunnerRejectsNoWorkerHost(t *testing.T) {
	host := temporal.New(func(host *temporal.Builder) {
		host.NoWorker(true)
	})

	registry, err := workflow.Handlers()

	if err != nil {
		t.Fatal(err)
	}

	if _, err := temporalworkflow.NewRunner(host, registry); err == nil {
		t.Fatal("NewRunner on a host without worker succeeded")
	}
}

// TestIntegrationStopWithExpiredContext verifies that worker resources are
// still released when Stop receives an already canceled context.
func TestIntegrationStopWithExpiredContext(t *testing.T) {
	host := temporal.New(func(host *temporal.Builder) {
		host.HostPort(itest.HostPort(t))
	}, func(host *temporal.Builder) {
		host.TaskQueue("stop")
	})

	registry, err := workflow.Handlers()

	if err != nil {
		t.Fatal(err)
	}

	if _, err := temporalworkflow.NewRunner(host, registry); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	expired, cancel := context.WithCancel(context.Background())
	cancel()

	_ = host.Stop(expired)

	if err := host.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestIntegrationClientOnly verifies that a separate client process can submit,
// resolve, and cancel executions handled by the worker process.
func TestIntegrationClientOnly(t *testing.T) {
	echo := workflow.New("echo", func(c *workflow.Context, in string) (string, error) {
		return in + "|" + c.Name(), nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(echo)

	if err != nil {
		t.Fatal(err)
	}

	worker(t, "client-only", registry)

	api := workflow.NewClient(clientOnly(t, "client-only"))
	id := runID("echo")

	run, err := api.Start(t.Context(), echo, id, "hi")

	if err != nil {
		t.Fatal(err)
	}

	got, err := run.Result(t.Context())

	if err != nil || got != "hi|echo" {
		t.Fatalf("Result = %q, %v", got, err)
	}

	if _, err := api.Start(t.Context(), echo, id, "again"); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("restart error = %v; want ErrAlreadyStarted", err)
	}

	if err := run.Cancel(t.Context()); err != nil {
		t.Fatalf("Cancel of a finished run: %v", err)
	}
}

// TestIntegrationSpawnAndMaxDuration verifies that detached child execution and
// workflow duration limits retain their contract behavior against the real
// engine.
func TestIntegrationSpawnAndMaxDuration(t *testing.T) {
	leaf := workflow.New("leaf", func(_ *workflow.Context, in string) (string, error) {
		return in + "!", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	root := workflow.New("root", func(c *workflow.Context, in string) (string, error) {
		run, err := c.Spawn(leaf, c.ID()+"-leaf", in)

		if err != nil {
			return "", err
		}

		return run, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	slow := workflow.New("slow", func(c *workflow.Context, _ string) (string, error) {
		return "", c.Sleep(time.Hour)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Second)
	})

	registry, err := workflow.Handlers(leaf, root, slow)

	if err != nil {
		t.Fatal(err)
	}

	worker(t, "spawn", registry)

	api := workflow.NewClient(clientOnly(t, "spawn"))

	run, err := api.Start(t.Context(), root, runID("root"), "x")

	if err != nil {
		t.Fatal(err)
	}

	spawned, err := run.Result(t.Context())

	if err != nil {
		t.Fatal(err)
	}

	if got, err := api.Run(leaf, spawned).Result(t.Context()); err != nil || got != "x!" {
		t.Fatalf("leaf Result = %q, %v", got, err)
	}

	// No registry: the runner enforces MaxDuration inside the workflow.
	timed, err := api.Start(t.Context(), slow, runID("slow"), "")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := timed.Result(t.Context()); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("Result error = %v; want ErrTimeout", err)
	}
}

// serverVersion reads the actual server version through the public native SDK
// service. It deliberately does not infer the running server from an image tag
// or CLI module manifest; missing version evidence fails the integration test.
func serverVersion(t *testing.T, connection client.Client) string {
	t.Helper()

	response, err := connection.WorkflowService().GetSystemInfo(t.Context(), &workflowservice.GetSystemInfoRequest{})

	if err != nil {
		t.Fatal(err)
	}

	version := response.GetServerVersion()

	if version == "" {
		t.Fatal("Temporal returned no native server version")
	}

	return version
}

// scheduledRetryPolicy reads the server-persisted activity policy through native
// history, rather than assuming a server version populates newer poll metadata.
// This fixture has one activity; a missing policy or native read failure is an
// integration failure, not permission to fall back to unbounded retry defaults.
func scheduledRetryPolicy(t *testing.T, connection client.Client, id string) *commonpb.RetryPolicy {
	t.Helper()

	events := connection.GetWorkflowHistory(t.Context(), id, "", false,
		enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)

	for events.HasNext() {
		event, err := events.Next()

		if err != nil {
			t.Fatal(err)
		}

		attributes := event.GetActivityTaskScheduledEventAttributes()

		if attributes == nil {
			continue
		}

		if attributes.RetryPolicy == nil {
			t.Fatal("server-persisted activity has no explicit retry policy")
		}

		return attributes.RetryPolicy
	}

	t.Fatal("server history has no scheduled activity")

	return nil
}

// TestIntegrationServerVersionAndRetryMaximum observes both the disposable
// server's version and its effective activity retry bound. Max zero must avoid
// the implicit 100-times-initial maximum, but this test does not invent a
// universal backend bound or claim that the server retains all Go durations.
func TestIntegrationServerVersionAndRetryMaximum(t *testing.T) {
	endpoint := itest.HostPort(t)
	initial := time.Millisecond
	step := workflow.NewStep("native-bound-step", func(_ context.Context, in string) (string, error) {
		return in, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(2)
		step.Backoff(initial, 2, 0)
	})
	definition := workflow.New("native-bound", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	host := temporal.New(func(host *temporal.Builder) {
		host.HostPort(endpoint)
		host.TaskQueue(runID("native-bound"))
	})

	runner, err := temporalworkflow.NewRunner(host, registry)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	version := serverVersion(t, host.Client())
	api := workflow.NewClient(runner.Driver())

	run, err := api.Start(t.Context(), definition, runID("native-bound"), "input")

	if err != nil {
		t.Fatal(err)
	}

	result, err := run.Result(t.Context())

	if err != nil || result != "input" {
		t.Fatalf("Result = %q, %v; want input", result, err)
	}

	policy := scheduledRetryPolicy(t, host.Client(), run.ID())

	if policy.MaximumAttempts != 2 {
		t.Fatalf("server-persisted maximum attempts = %d; want two finite attempts", policy.MaximumAttempts)
	}

	maximum := policy.MaximumInterval.AsDuration()

	if maximum <= 100*initial {
		t.Fatalf("effective native maximum = %s; inherited the implicit 100x cap", maximum)
	}

	t.Logf("native server=%s requested_maximum=%s effective_maximum=%s", version,
		time.Duration(math.MaxInt64), maximum)
}

// ownershipEngine owns a real native worker and typed definitions for join
// observations. The child is always submitted independently before the parent.
type ownershipEngine struct {
	// api submits and observes the facade's typed handles.
	api *workflow.Client

	// native observes the join activity through the public server API.
	native client.Client

	// child waits for a signal and preserves the original caller's input.
	child *workflow.Workflow[string, string]

	// parent exercises explicit cancellation after the server starts its join.
	parent *workflow.Workflow[string, string]

	// timedParent exercises MaxDuration after that same native acknowledgement.
	timedParent *workflow.Workflow[string, string]

	// calls counts actual child entry work, never replayed workflow code.
	calls atomic.Int32
}

// newOwnershipEngine registers a signal-blocked child and two joining parents
// on an isolated task queue. Native worker cleanup remains owned by the test.
func newOwnershipEngine(t *testing.T) *ownershipEngine {
	t.Helper()

	endpoint := itest.HostPort(t)
	engine := &ownershipEngine{}
	entry := workflow.NewStep("ownership-entry", func(_ context.Context, input string) (string, error) {
		engine.calls.Add(1)

		return input, nil
	})
	engine.child = workflow.New("ownership-child", func(ctx *workflow.Context, input string) (string, error) {
		if _, err := ctx.Step(entry, input); err != nil {
			return "", err
		}

		value, err := ctx.Wait(workflow.NewSignal[string]("release"))

		return input + value, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	join := func(ctx *workflow.Context, id string) (string, error) {
		return ctx.Child(engine.child, id, "ignored")
	}
	engine.parent = workflow.New("ownership-parent", join, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	engine.timedParent = workflow.New("ownership-timed-parent", join, func(definition *workflow.Builder) {
		definition.MaxDuration(4 * time.Second)
	})
	registry, err := workflow.Handlers(entry, engine.child, engine.parent, engine.timedParent)

	if err != nil {
		t.Fatal(err)
	}

	host := temporal.New(func(host *temporal.Builder) {
		host.HostPort(endpoint)
		host.TaskQueue(runID("native-ownership"))
	})

	runner, err := temporalworkflow.NewRunner(host, registry)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	engine.native = host.Client()
	engine.api = workflow.NewClient(runner.Driver())

	return engine
}

// joinExisting submits an independent child before its joining parent, then
// requires native evidence of the started result-reading activity. Cancellation
// is not allowed to race ahead of the operation that this test intends to cover.
func (engine *ownershipEngine) joinExisting(t *testing.T, definition *workflow.Workflow[string, string]) (*workflow.Run[string], *workflow.Run[string]) {
	t.Helper()

	child, err := engine.api.Start(t.Context(), engine.child, runID("independent"), "original")

	if err != nil {
		t.Fatal(err)
	}

	parent, err := engine.api.Start(t.Context(), definition, runID("joining"), child.ID())

	if err != nil {
		t.Fatal(err)
	}

	engine.waitForJoin(t, parent.ID())

	return parent, child
}

// waitForJoin observes the native activity state, not a process-local readiness
// guess or a delay. A canceled observation, description failure, or parent that
// never reaches the join is a test failure rather than a successful ownership test.
func (engine *ownershipEngine) waitForJoin(t *testing.T, id string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	for ctx.Err() == nil {
		description, err := engine.native.DescribeWorkflowExecution(ctx, id, "")

		if err != nil {
			t.Fatal(err)
		}

		started := slices.ContainsFunc(description.PendingActivities, func(info *workflowpb.PendingActivityInfo) bool {
			return info.GetActivityType().GetName() == "arkoren-join" && info.GetState() == enumspb.PENDING_ACTIVITY_STATE_STARTED
		})

		if started {
			t.Logf("native join activity started: parent=%s", id)

			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("native join activity never started: %v", ctx.Err())
}

// finishIndependent proves that a fully joined run still waits after parent
// termination and can finish through the public signal API without duplicate work.
func (engine *ownershipEngine) finishIndependent(t *testing.T, child *workflow.Run[string]) {
	t.Helper()

	if status, err := child.Status(t.Context()); err != nil || status != workflow.Running {
		t.Fatalf("independent child Status = %v, %v; want running", status, err)
	}

	if err := engine.api.Signal(t.Context(), engine.child, child.ID(), workflow.NewSignal[string]("release"), "!"); err != nil {
		t.Fatal(err)
	}

	if output, err := child.Result(t.Context()); err != nil || output != "original!" {
		t.Fatalf("independent child Result = %q, %v; want original!", output, err)
	}

	if calls := engine.calls.Load(); calls != 1 {
		t.Fatalf("child entry work ran %d times; want one", calls)
	}
}

// TestIntegrationJoinedChildSurvivesParentCancellation cancels only after the
// server confirms the join activity started. The preexisting run must survive.
func TestIntegrationJoinedChildSurvivesParentCancellation(t *testing.T) {
	engine := newOwnershipEngine(t)

	parent, child := engine.joinExisting(t, engine.parent)

	if err := parent.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := parent.Result(t.Context()); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("parent Result = %v; want ErrCanceled", err)
	}

	engine.finishIndependent(t, child)
}

// TestIntegrationJoinedChildSurvivesParentTimeout establishes the same native
// join before MaxDuration ends the wait. Timeout must not acquire child ownership.
func TestIntegrationJoinedChildSurvivesParentTimeout(t *testing.T) {
	engine := newOwnershipEngine(t)

	parent, child := engine.joinExisting(t, engine.timedParent)

	if _, err := parent.Result(t.Context()); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("parent Result = %v; want ErrTimeout", err)
	}

	engine.finishIndependent(t, child)
}
