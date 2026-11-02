package restateworkflow_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	restatesdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/workflowtest"
	"arkoren.dev/driver/restate"
	"arkoren.dev/driver/restate/internal/itest"
	"arkoren.dev/driver/restate/internal/protocoltest"
	"arkoren.dev/driver/restate/restateworkflow"
)

var (
	// journalCalls counts actual step executions independently of journal replay.
	journalCalls atomic.Int32
	// journalStep records one side effect so repeated calls can prove journal
	// reuse.
	journalStep = workflow.NewStep("edge-once", func(context.Context, string) (string, error) {
		journalCalls.Add(1)

		return "done", nil
	})
	// journalFlow calls a named step repeatedly to check durable result reuse.
	journalFlow = workflow.New("edge-journal", func(c *workflow.Context, in string) (string, error) {
		first, err := c.Step(journalStep, in)

		if err != nil {
			return "", err
		}

		started := c.Now()

		if err := c.Sleep(1500 * time.Millisecond); err != nil {
			return "", err
		}

		if c.Now().Sub(started) < 1500*time.Millisecond {
			return "", errors.New("slept too short")
		}

		id1, id2 := c.UUID(), c.UUID()

		if id1 == id2 || len(id1) != 36 {
			return "", errors.New("bad uuids")
		}

		return first, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	// exhaustCalls counts attempts made before the configured retry limit is
	// reached.
	exhaustCalls atomic.Int32
	// exhaustKind identifies the application failure expected after retry
	// exhaustion.
	exhaustKind = workflow.NewError("edge-kind", "")
	// exhaustStep fails with a retryable application error on every attempt.
	exhaustStep = workflow.NewStep("edge-fail", func(context.Context, string) (string, error) {
		exhaustCalls.Add(1)

		return "", workflow.NewError("edge-kind", "nope")
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(time.Millisecond, 1, time.Millisecond)
	})
	// exhaustFlow exposes the exhausted step failure through the workflow result
	// API.
	exhaustFlow = workflow.New("edge-exhaust", func(c *workflow.Context, in string) (string, error) {
		return c.Step(exhaustStep, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	// hangStep waits for its context to end so the step timeout path can be
	// observed.
	hangStep = workflow.NewStep("edge-hang-step", func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()

		return "", ctx.Err()
	}, func(step *workflow.StepBuilder) {
		step.Retry(2)
		step.Backoff(time.Millisecond, 1, time.Millisecond)
		step.Timeout(100 * time.Millisecond)
	})
	// hangFlow runs the blocked step with a bounded timeout.
	hangFlow = workflow.New("edge-hang", func(c *workflow.Context, in string) (string, error) {
		return c.Step(hangStep, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	// panicFlow panics deliberately to test conversion to a terminal workflow
	// failure.
	panicFlow = workflow.New("edge-panic", func(*workflow.Context, string) (string, error) {
		panic("kaboom")
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	// endedSignal names the signal used to check operations against a completed
	// run.
	endedSignal = workflow.NewSignal[string]("edge-sig")
	// endedFlow finishes immediately so late signals and cancellation can be
	// checked.
	endedFlow = workflow.New("edge-ended", func(*workflow.Context, string) (string, error) {
		return "ok", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	// delayFlow returns a fixed result after its submission delay has elapsed.
	delayFlow = workflow.New("edge-delay", func(*workflow.Context, string) (string, error) {
		return "ran", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	// delayCancelFlow provides a delayed run that can be canceled before its
	// handler begins.
	delayCancelFlow = workflow.New("edge-delay-cancel", func(*workflow.Context, string) (string, error) {
		return "ran", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
		definition.Retention(5 * time.Minute)
	})

	// spawned returns a transformed input after a short durable pause.
	spawned = workflow.New("edge-spawned", func(c *workflow.Context, in int) (int, error) {
		if err := c.Sleep(500 * time.Millisecond); err != nil {
			return 0, err
		}

		return in + 1, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	// spawner starts a detached child and returns its identifier for a later
	// result lookup.
	spawner = workflow.New("edge-spawner", func(c *workflow.Context, in int) (int, error) {
		id, err := c.Spawn(spawned, c.ID()+"-kid", in)

		if err != nil || id != c.ID()+"-kid" {
			return 0, fmt.Errorf("spawn id %q: %w", id, err)
		}

		return in, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	// metaFlow provides a registered workflow for submission-metadata assertions.
	metaFlow = workflow.New("edge-meta", func(*workflow.Context, string) (string, error) {
		return "ok", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	// unregisteredFlow is deliberately omitted from the registry to test missing-
	// definition rejection.
	unregisteredFlow = workflow.New("edge-unregistered", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
)

// edgeDefinitions returns the definitions the edge cases run, except the
// unregistered one.
func edgeDefinitions() []workflow.Definition {
	return []workflow.Definition{
		journalStep, journalFlow, exhaustStep, exhaustFlow, hangStep, hangFlow, panicFlow,
		endedFlow, delayFlow, delayCancelFlow, spawned, spawner, metaFlow,
	}
}

// edge is the state of one edge-case subtest.
type edge struct {
	// t reports failures for the currently running shared-host scenario.
	t *testing.T
	// ctx carries execution cancellation and the effective run deadline.
	ctx context.Context
	// client submits and resolves runs against the shared integration host.
	client *workflow.Client
	// id prefixes the logical run identifiers created by this scenario, keeping
	// its parent and child invocations separate from other shared-host checks.
	id string
}

// testEdges runs the edge cases on client, in parallel, as subtests: they share
// the one server and worker the caller started, which is too costly to start
// per case. Run ids carry token, so the server can keep its history between
// runs.
func testEdges(t *testing.T, client *workflow.Client, token string) {
	runEdge := func(index int, name string, run func(*edge)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			t.Cleanup(cancel)

			run(&edge{
				t:      t,
				ctx:    ctx,
				client: client,
				id:     token + "-edge" + string(rune('a'+index)),
			})
		})
	}

	runEdge(0, "steps are journaled across suspension", edgeJournal)
	runEdge(1, "exhausted retries keep the error", edgeExhaust)
	runEdge(2, "step timeout kind survives retries", edgeHang)
	runEdge(3, "panic fails the run", edgePanic)
	runEdge(4, "signal and cancel after the run ended", edgeEnded)
	runEdge(5, "delayed start", edgeDelay)
	runEdge(6, "cancel a delayed run", edgeDelayCancel)
	runEdge(7, "spawn outlives the parent", edgeSpawn)
	runEdge(8, "metadata and unknown workflow", edgeMeta)
}

// edgeJournal checks that a repeated named step reuses its journaled result
// rather than executing the side effect twice.
func edgeJournal(e *edge) {
	run, err := e.client.Start(e.ctx, journalFlow, e.id, "x")

	if err != nil {
		e.t.Fatal(err)
	}

	if got, err := run.Result(e.ctx); err != nil || got != "done" {
		e.t.Fatalf("Result = %q, %v", got, err)
	}

	if journalCalls.Load() != 1 {
		e.t.Fatalf("step ran %d times; want 1", journalCalls.Load())
	}
}

// edgeExhaust checks that retry exhaustion preserves the application error kind
// and executes exactly the configured number of attempts.
func edgeExhaust(e *edge) {
	run, err := e.client.Start(e.ctx, exhaustFlow, e.id, "x")

	if err != nil {
		e.t.Fatal(err)
	}

	if _, err := run.Result(e.ctx); !errors.Is(err, exhaustKind) || !strings.Contains(err.Error(), "nope") {
		e.t.Fatalf("Result error = %v; want kind edge-kind", err)
	}

	if exhaustCalls.Load() != 3 {
		e.t.Fatalf("step ran %d times; want 3", exhaustCalls.Load())
	}
}

// edgeHang checks that a blocked step ends with the workflow timeout sentinel
// rather than leaving result lookup blocked.
func edgeHang(e *edge) {
	run, err := e.client.Start(e.ctx, hangFlow, e.id, "x")

	if err != nil {
		e.t.Fatal(err)
	}

	if _, err := run.Result(e.ctx); !errors.Is(err, workflow.ErrTimeout) {
		e.t.Fatalf("Result error = %v; want ErrTimeout", err)
	}
}

// edgePanic checks that an application panic becomes a failed workflow with its
// message preserved.
func edgePanic(e *edge) {
	run, err := e.client.Start(e.ctx, panicFlow, e.id, "x")

	if err != nil {
		e.t.Fatal(err)
	}

	if _, err := run.Result(e.ctx); err == nil || !strings.Contains(err.Error(), "kaboom") {
		e.t.Fatalf("Result error = %v; want panic", err)
	}

	if status, _ := run.Status(e.ctx); status != workflow.Failed {
		e.t.Fatalf("Status = %v; want failed", status)
	}
}

// edgeEnded checks signal and cancellation behavior for completed and unknown
// executions using the shared integration host.
func edgeEnded(e *edge) {
	run, err := e.client.Start(e.ctx, endedFlow, e.id, "x")

	if err != nil {
		e.t.Fatal(err)
	}

	if _, err := run.Result(e.ctx); err != nil {
		e.t.Fatal(err)
	}

	if err := e.client.Signal(e.ctx, endedFlow, e.id, endedSignal, "late"); !errors.Is(err, workflow.ErrNotFound) {
		e.t.Fatalf("Signal error = %v; want ErrNotFound", err)
	}

	if err := run.Cancel(e.ctx); err != nil {
		e.t.Fatalf("Cancel of a finished run = %v", err)
	}

	if err := e.client.Run(endedFlow, e.id+"-missing").Cancel(e.ctx); !errors.Is(err, workflow.ErrNotFound) {
		e.t.Fatalf("Cancel of an unknown run = %v; want ErrNotFound", err)
	}
}

// edgeDelay checks that an accepted delayed run remains running before its
// handler returns the expected result.
func edgeDelay(e *edge) {
	run, err := e.client.Start(e.ctx, delayFlow, e.id, "x", workflow.Delay(time.Second))

	if err != nil {
		e.t.Fatal(err)
	}

	if status, err := run.Status(e.ctx); err != nil || status != workflow.Running {
		e.t.Fatalf("Status = %v, %v; want running", status, err)
	}

	if got, err := run.Result(e.ctx); err != nil || got != "ran" {
		e.t.Fatalf("Result = %q, %v", got, err)
	}
}

// edgeDelayCancel checks cancellation before a delayed handler starts and
// verifies the run cannot produce a normal result.
func edgeDelayCancel(e *edge) {
	run, err := e.client.Start(e.ctx, delayCancelFlow, e.id, "x", workflow.Delay(time.Hour))

	if err != nil {
		e.t.Fatal(err)
	}

	if err := run.Cancel(e.ctx); err != nil {
		e.t.Fatal(err)
	}

	// Retention is a persistent server feature, disabled by the legacy fixture.
	want := workflow.ErrNotFound

	if itest.PreflightRetention(e.t) {
		want = workflow.ErrCanceled
	}

	if _, err := run.Result(e.ctx); !errors.Is(err, want) {
		e.t.Fatalf("Result error = %v; want %v", err, want)
	}
}

// edgeSpawn checks that a detached child can be resolved by the identifier
// returned from its parent.
func edgeSpawn(e *edge) {
	run, err := e.client.Start(e.ctx, spawner, e.id, 41)

	if err != nil {
		e.t.Fatal(err)
	}

	if got, err := run.Result(e.ctx); err != nil || got != 41 {
		e.t.Fatalf("Result = %d, %v", got, err)
	}

	if got, err := e.client.Run(spawned, e.id+"-kid").Result(e.ctx); err != nil || got != 42 {
		e.t.Fatalf("child Result = %d, %v", got, err)
	}
}

// edgeMeta checks submission with metadata and rejection of a definition
// omitted from the shared registry.
func edgeMeta(e *edge) {
	run, err := e.client.Start(e.ctx, metaFlow, e.id, "x", workflow.Metadata(map[string]string{"team": "a"}))

	if err != nil {
		e.t.Fatal(err)
	}

	if _, err := run.Result(e.ctx); err != nil {
		e.t.Fatal(err)
	}

	if _, err := e.client.Start(e.ctx, unregisteredFlow, e.id, "x"); !errors.Is(err, workflow.ErrNotFound) {
		e.t.Fatalf("Start of an unregistered workflow = %v; want ErrNotFound", err)
	}
}

// endpointPort is the port of the endpoint of these tests. The server reaches
// it at host.docker.internal.
const endpointPort = "19080"

// TestIntegration serves the conformance and edge definitions from one
// endpoint and runs both suites against it. Ownership-entry observations are
// retained locally and printed only on conformance failure, before edge tests
// and invocation cleanup.
// The test is intentionally nonparallel while its SDK default logger is wrapped.
//
//	go test -race -run '^TestIntegration$' ./restateworkflow
func TestIntegration(t *testing.T) {
	trace := &ownershipTrace{started: time.Now()}
	captureOwnershipLogs(t, trace)
	registry := ownershipRegistry(t, trace)

	var runner *restateworkflow.Runner

	served, client := itest.Serve(t, endpointPort, func(host *restate.Host) {
		var err error

		runner, err = restateworkflow.NewRunner(host, registry)

		if err != nil {
			t.Fatal(err)
		}
	})

	for _, service := range runner.Services() {
		itest.WaitForService(t, served, service)
	}

	itest.KillInvocations(t, client, runner.Services()...)

	driver, err := restateworkflow.NewDriver(client)

	if err != nil {
		t.Fatal(err)
	}

	t.Run("conformance", func(t *testing.T) {
		registerOwnershipDiagnostics(t, trace, client.Query)
		workflowtest.Run(t, func(*testing.T) workflow.Driver {
			return driver
		}, workflowtest.WithoutChild())
	})
	t.Run("edges", func(t *testing.T) {
		testEdges(t, workflow.NewClient(driver), strconv.FormatInt(time.Now().UnixNano(), 36))
	})
}

// TestIntegrationStepRejectsLateSuccess checks the engine-retained result of a
// cooperative step that returns success after its own deadline. The HTTP-only
// regression runs without services; this test additionally covers real storage
// and ingress translation when the coordinator provisions integration services.
func TestIntegrationStepRejectsLateSuccess(t *testing.T) {
	step := workflow.NewStep("late-success", func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()

		return "too late", nil
	}, func(step *workflow.StepBuilder) {
		step.Timeout(time.Nanosecond)
	})
	definition := workflow.New("late-result", func(execution *workflow.Context, input string) (string, error) {
		return execution.Step(step, input)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
		definition.Retention(5 * time.Minute)
	})

	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	served, client := itest.Serve(t, "19083", func(host *restate.Host) {
		if _, err := restateworkflow.NewRunner(host, registry, func(runner *restateworkflow.RunnerBuilder) {
			runner.Prefix("timeout-regression")
		}); err != nil {
			t.Fatal(err)
		}
	})
	itest.WaitForService(t, served, "TimeoutRegressionWf_late-result")
	itest.KillInvocations(t, client, "TimeoutRegressionWf_late-result")

	driver, err := restateworkflow.NewDriver(client, func(driver *restateworkflow.DriverBuilder) {
		driver.Prefix("timeout-regression")
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)

	run, err := workflow.NewClient(driver).Start(ctx, definition, "late-"+strconv.FormatInt(time.Now().UnixNano(), 36), "input")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := run.Result(ctx); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("Result = %v, want ErrTimeout", err)
	}
}

// workflowEndpoint binds a typed step and an orchestration that calls it once.
// Tests drive the real Runner through HTTP; the helper supplies no runtime fake.
func workflowEndpoint(t *testing.T, step *workflow.Step[string, string]) *restate.Host {
	t.Helper()

	definition := workflow.New("probe", func(execution *workflow.Context, input string) (string, error) {
		return execution.Step(step, input)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(definition, step)

	if err != nil {
		t.Fatal(err)
	}

	host := restate.New(func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
		host.AllowInsecure(true)
	})

	if _, err := restateworkflow.NewRunner(host, registry); err != nil {
		t.Fatal(err)
	}

	protocoltest.Start(t, host)

	return host
}

// TestStepImmediateRetryDoesNotUseNativeDefault verifies the retry instruction
// emitted after a real failed attempt, not a private option count.
func TestStepImmediateRetryDoesNotUseNativeDefault(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("attempt", func(context.Context, string) (string, error) {
		return "", errors.New("retryable")
	}, func(step *workflow.StepBuilder) {
		step.Retry(2)
		step.Backoff(0, 2, 0)
	})

	host := workflowEndpoint(t, step)

	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenWf_probe/Run", Key: "run-42", Input: []byte(`{"v":"input"}`),
	})

	last := frames[len(frames)-1]
	delay, configured := last.Uint(8)

	if last.Type != 2 || !configured || delay != 0 {
		t.Fatalf("retry instruction type=%x delay=%dms configured=%v, want explicit immediate retry", last.Type, delay, configured)
	}
}

// TestStepUncappedBackoffDoesNotUseNativeCap checks a delay above the SDK's
// default two-second cap, without waiting for that delay in wall-clock time.
func TestStepUncappedBackoffDoesNotUseNativeCap(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("attempt", func(context.Context, string) (string, error) {
		return "", errors.New("retryable")
	}, func(step *workflow.StepBuilder) {
		step.Retry(4)
		step.Backoff(3*time.Second, 2, 0)
	})

	host := workflowEndpoint(t, step)

	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenWf_probe/Run", Key: "run-42", Input: []byte(`{"v":"input"}`),
		RetryCount: 1,
	})

	last := frames[len(frames)-1]
	delay, configured := last.Uint(8)

	if last.Type != 2 || !configured || delay != 3000 {
		t.Fatalf("retry instruction type=%x delay=%dms configured=%v, want first backoff 3000ms", last.Type, delay, configured)
	}
}

// TestStepLateSuccessIsTimeout covers a cooperative handler that observes its
// expired context but returns a value anyway. No handler goroutine is abandoned.
func TestStepLateSuccessIsTimeout(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("attempt", func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()

		return "late success", nil
	}, func(step *workflow.StepBuilder) {
		step.Timeout(time.Nanosecond)
	})

	host := workflowEndpoint(t, step)

	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenWf_probe/Run", Key: "run-42", Input: []byte(`{"v":"input"}`),
	})

	var result error

	for _, frame := range frames {
		if frame.Type == 0x0401 {
			result = workflow.DecodeError(protocoltest.Frame{Body: frame.Bytes(15)}.Bytes(2))
		}
	}

	if !errors.Is(result, workflow.ErrTimeout) {
		t.Fatalf("workflow output = %v, want ErrTimeout", result)
	}
}

// TestCompletedStepReplaySkipsAttempt checks the SDK's real replay boundary with
// a captured HTTP journal. Completed results must not repeat the handler side
// effect after retry-policy and timeout mapping changes.
func TestCompletedStepReplaySkipsAttempt(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	step := workflow.NewStep("attempt", func(context.Context, string) (string, error) {
		calls.Add(1)

		return "recorded", nil
	})

	host := workflowEndpoint(t, step)
	invocation := protocoltest.Invocation{
		Path: "/invoke/ArkorenWf_probe/Run", Key: "run-42", Input: []byte(`{"v":"input"}`),
	}

	first := protocoltest.Invoke(t, host, invocation)

	if first[len(first)-1].Type != 3 || calls.Load() != 1 {
		t.Fatal("initial invocation did not complete exactly one actual attempt")
	}

	replayed := protocoltest.Replay(t, host, invocation, first)

	if replayed[len(replayed)-1].Type != 3 || calls.Load() != 1 {
		t.Fatalf("replay repeated the handler or failed: attempts=%d", calls.Load())
	}
}

// TestChildRejectsBeforeNativeScheduling drives the bound Runner through the
// SDK's HTTP protocol. Unsupported ownership must not emit a call, send, attach,
// or cancellation command; the error must survive the ordinary output envelope.
func TestChildRejectsBeforeNativeScheduling(t *testing.T) {
	t.Parallel()

	child := workflow.New("child", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	parent := workflow.New("parent", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Child(child, "child-42", in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(parent, child)

	if err != nil {
		t.Fatal(err)
	}

	host := restate.New(func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
		host.AllowInsecure(true)
	})

	if _, err := restateworkflow.NewRunner(host, registry); err != nil {
		t.Fatal(err)
	}

	protocoltest.Start(t, host)

	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenWf_parent/Run", Key: "parent-42", Input: []byte(`{"v":"input"}`),
	})

	var result error

	for _, frame := range frames {
		switch frame.Type {
		case 0x040d, 0x040e, 0x0410, 0x0412:
			t.Fatalf("unsupported Child emitted native command %x", frame.Type)
		case 0x0401:
			result = workflow.DecodeError(protocoltest.Frame{Body: frame.Bytes(15)}.Bytes(2))
		}
	}

	if !errors.Is(result, workflow.ErrUnsupported) {
		t.Fatalf("workflow output = %v; want ErrUnsupported", result)
	}
}

// retentionService is the native name of the fixture's Arkoren workflow. Both
// native scoped submissions and raw Driver calls use the same bound handler.
const retentionService = "RetentionWf_retention-probe"

// nativeCanceledCode is Restate's invocation failure code for cancellation.
// It is not an ingress HTTP transport failure, despite sharing the value 409.
const nativeCanceledCode = 409

// retentionEndpoint binds a workflow whose executions are counted, with explicit
// positive retention. A separate native service can hold one scoped concurrency
// slot; releasing it is joined through its invocation handle by the queue test.
func retentionEndpoint(t *testing.T, blocker restatesdk.ServiceDefinition) (*restate.Host, *restateworkflow.Driver, *atomic.Int32) {
	t.Helper()

	var calls atomic.Int32
	definition := workflow.New("retention-probe", func(*workflow.Context, string) (string, error) {
		calls.Add(1)

		return "ran", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
		definition.Retention(5 * time.Minute)
	})

	registry, err := workflow.Handlers(definition)

	if err != nil {
		t.Fatal(err)
	}

	_, client := itest.Serve(t, "19085", func(host *restate.Host) {
		if blocker != nil {
			if err := host.Bind(blocker); err != nil {
				t.Fatal(err)
			}
		}

		if _, err := restateworkflow.NewRunner(host, registry, func(runner *restateworkflow.RunnerBuilder) {
			runner.Prefix("retention")
		}); err != nil {
			t.Fatal(err)
		}
	})
	itest.WaitForService(t, client, retentionService)

	driver, err := restateworkflow.NewDriver(client, func(driver *restateworkflow.DriverBuilder) {
		driver.Prefix("retention")
	})

	if err != nil {
		t.Fatal(err)
	}

	return client, driver, &calls
}

// TestIntegrationDelayedCancellationRetention checks the actual Driver lifecycle
// before the Run handler executes. The requested profile chooses the assertion;
// no observed failure is silently converted into a legacy-profile success.
func TestIntegrationDelayedCancellationRetention(t *testing.T) {
	_, driver, calls := retentionEndpoint(t, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	id := "delayed-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	if err := driver.Start(ctx, "retention-probe", id, []byte(`{"v":"input"}`), workflow.StartOptions{Delay: time.Hour}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Cancel(ctx, "retention-probe", id); err != nil {
		t.Fatal(err)
	}

	if itest.PreflightRetention(t) {
		requireRetainedCancellation(t, ctx, driver, id)
	} else {
		requireDiscardedCancellation(t, ctx, driver, id)
	}

	if count := calls.Load(); count != 0 {
		t.Fatalf("canceled application executed %d times", count)
	}
}

// requireRetainedCancellation checks result identity, terminal state, retained
// duplicate protection, and idempotent cancellation through the public Driver.
func requireRetainedCancellation(t *testing.T, ctx context.Context, driver *restateworkflow.Driver, id string) {
	t.Helper()

	if _, err := driver.Result(ctx, "retention-probe", id); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("Result = %v, want ErrCanceled", err)
	}

	if status, err := driver.Status(ctx, "retention-probe", id); err != nil || status != workflow.Canceled {
		t.Fatalf("Status = %v, %v, want Canceled", status, err)
	}

	err := driver.Start(ctx, "retention-probe", id, []byte(`{"v":"replacement"}`), workflow.StartOptions{Delay: time.Hour})

	if !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("duplicate Start = %v, want ErrAlreadyStarted", err)
	}

	if err := driver.Cancel(ctx, "retention-probe", id); err != nil {
		t.Fatalf("repeated Cancel = %v", err)
	}
}

// requireDiscardedCancellation characterizes the legacy native gap instead of
// weakening the retained-profile assertions. The replacement remains delayed
// and is canceled too, so the comparison never executes application code.
func requireDiscardedCancellation(t *testing.T, ctx context.Context, driver *restateworkflow.Driver, id string) {
	t.Helper()

	if _, err := driver.Result(ctx, "retention-probe", id); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("legacy Result = %v, want ErrNotFound", err)
	}

	if _, err := driver.Status(ctx, "retention-probe", id); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("legacy Status = %v, want ErrNotFound", err)
	}

	if err := driver.Cancel(ctx, "retention-probe", id); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("legacy repeated Cancel = %v, want ErrNotFound", err)
	}

	if err := driver.Start(ctx, "retention-probe", id, []byte(`{"v":"replacement"}`), workflow.StartOptions{Delay: time.Hour}); err != nil {
		t.Fatalf("legacy duplicate Start = %v, want accepted replacement", err)
	}

	if err := driver.Cancel(ctx, "retention-probe", id); err != nil {
		t.Fatal(err)
	}
}

// waitNativeState observes the server's persisted invocation status, rather than
// inferring queued work from a timeout or lack of handler output. The native ID
// is server-generated and contains no SQL delimiters. Polling is bounded.
func waitNativeState(t *testing.T, host *restate.Host, id, state string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	for {
		rows, err := host.Query(ctx, "SELECT status FROM sys_invocation_status WHERE id = '"+id+"'")

		if err != nil {
			t.Fatal(err)
		}

		if len(rows) == 1 && rows[0]["status"] == state {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("invocation %s did not reach %s: %v; rows=%v", id, state, ctx.Err(), rows)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// cancelNativePending verifies synchronous native preflight cancellation. HTTP
// 200 is the state-machine Done response, not an assumed completed 202 request.
func cancelNativePending(t *testing.T, ctx context.Context, host *restate.Host, id string) {
	t.Helper()

	response, err := host.Admin(ctx, http.MethodPatch, "/invocations/"+id+"/cancel", nil)

	if err != nil {
		t.Fatal(err)
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("pending native Cancel = HTTP %d, want 200", response.StatusCode)
	}
}

// TestIntegrationQueuedCancellationRetention uses a real scoped concurrency
// limit to keep a Workflow Run in Inboxed, with no application execution. The
// current Arkoren Driver has no scope setting; this case deliberately uses its
// bound Runner through the public native SDK/admin seams, without wire changes.
func TestIntegrationQueuedCancellationRetention(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	if os.Getenv("ARKOREN_RESTATE_VQUEUES") != "true" {
		t.Skip("queued Workflow fixture requires ARKOREN_RESTATE_VQUEUES=true on a fresh disposable cluster")
	}

	started := make(chan struct{})
	released, release := context.WithCancel(t.Context())
	blocker := restatesdk.NewService("RetentionBlocker").Handler("Hold", restatesdk.NewServiceHandler(
		func(ctx restatesdk.Context, _ restatesdk.Void) (restatesdk.Void, error) {
			close(started)

			select {
			case <-released.Done():
				return restatesdk.Void{}, nil
			case <-ctx.Done():
				return restatesdk.Void{}, ctx.Err()
			}
		}))

	client, _, calls := retentionEndpoint(t, blocker)
	t.Cleanup(release)
	itest.WaitForService(t, client, "RetentionBlocker")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	installQueueLimit(t, ctx, client)
	hold, err := ingress.ServiceSend[restatesdk.Void](client.Ingress(), "RetentionBlocker", "Hold", restatesdk.WithScope("retention-queue")).Send(ctx, restatesdk.Void{})

	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	id := "queued-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	request := ingress.WorkflowSend[map[string]string](client.Ingress(), retentionService, id, "Run", restatesdk.WithScope("retention-queue"))
	invocation, err := request.Send(ctx, map[string]string{"v": "input"})

	if err != nil {
		t.Fatal(err)
	}

	waitNativeState(t, client, invocation.Id(), "inboxed")
	cancelNativePending(t, ctx, client, invocation.Id())
	checkNativeCancellation(t, ctx, client, invocation.Id())
	checkQueuedDuplicate(t, ctx, client, request)
	release()

	if _, err := ingress.InvocationById[restatesdk.Void](client.Ingress(), hold.Id()).Attach(ctx); err != nil {
		t.Fatal(err)
	}

	if count := calls.Load(); count != 0 {
		t.Fatalf("queued canceled application executed %d times", count)
	}
}

// installQueueLimit configures only the isolated fixture scope. Its blocker and
// queued Run share one scope, so they compete for exactly one concurrency slot.
func installQueueLimit(t *testing.T, ctx context.Context, host *restate.Host) {
	t.Helper()

	response, err := host.Admin(ctx, http.MethodPut, "/limits/rules", []byte(`[{"pattern":"retention-queue","limits":{"concurrency":1}}]`))

	if err != nil {
		t.Fatal(err)
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("install queue limit = HTTP %d", response.StatusCode)
	}
}

// checkNativeCancellation checks both blocking result and nonblocking output,
// persisted terminal state, and repeated administrative cancellation. The
// requested fixture profile is explicit; failed retained results never fall back.
func checkNativeCancellation(t *testing.T, ctx context.Context, host *restate.Host, id string) {
	t.Helper()

	handle := ingress.InvocationById[restatesdk.Void](host.Ingress(), id)
	retained := itest.PreflightRetention(t)
	require := requireNativeMissing

	if retained {
		require = requireNativeCanceled
	}

	_, err := handle.Attach(ctx)
	require(t, err)

	_, err = handle.Output(ctx)
	require(t, err)

	if retained {
		waitNativeState(t, host, id, "completed")
	} else {
		rows, err := host.Query(ctx, "SELECT status FROM sys_invocation_status WHERE id = '"+id+"'")

		if err != nil {
			t.Fatal(err)
		}

		if len(rows) != 0 {
			t.Fatalf("legacy canceled invocation is still retained: %v", rows)
		}
	}

	response, err := host.Admin(ctx, http.MethodPatch, "/invocations/"+id+"/cancel", nil)

	if err != nil {
		t.Fatal(err)
	}

	defer response.Body.Close()

	want := http.StatusNotFound

	if retained {
		want = http.StatusConflict
	}

	if response.StatusCode != want {
		t.Fatalf("repeated native Cancel = HTTP %d, want %d", response.StatusCode, want)
	}
}

// requireNativeMissing checks the legacy profile's absent output explicitly.
// An unrelated ingress or transport failure does not satisfy this assertion.
func requireNativeMissing(t *testing.T, err error) {
	t.Helper()

	var missing *ingress.InvocationNotFoundError

	if !errors.As(err, &missing) {
		t.Fatalf("legacy native result = %v, want invocation not found", err)
	}
}

// requireNativeCanceled distinguishes a retained canceled invocation failure
// from an ingress error, a missing result, or an unrelated application failure.
func requireNativeCanceled(t *testing.T, err error) {
	t.Helper()

	var failure ingress.Error

	if !errors.As(err, &failure) || !failure.IsInvocationError() || failure.Code() != nativeCanceledCode {
		t.Fatalf("native result = %v, want invocation cancellation code 409", err)
	}
}

// checkQueuedDuplicate resubmits the same scoped Workflow identity. A retained
// cancellation must deduplicate; the legacy replacement is canceled immediately
// while the fixture still holds its only concurrency slot.
func checkQueuedDuplicate(t *testing.T, ctx context.Context, host *restate.Host, request ingress.SendRequester[map[string]string]) {
	t.Helper()

	response, err := request.Send(ctx, map[string]string{"v": "replacement"})

	if err != nil {
		t.Fatal(err)
	}

	retained := itest.PreflightRetention(t)
	want := "Accepted"

	if retained {
		want = "PreviouslyAccepted"
	}

	if response.Status() != want {
		t.Fatalf("duplicate native Start = %s, want %s", response.Status(), want)
	}

	if !retained {
		cancelNativePending(t, ctx, host, response.Id())
	}
}
