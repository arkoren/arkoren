// Package execution owns Restate attempts and suspension-safe runtime state.
package execution

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate/restateworkflow/internal/protocol"
)

// execution implements [workflow.Runtime] over one Restate workflow attempt.
// Local cancellation is synchronized with the shared Cancel handler; journal
// operations retain the native context and its replay semantics.
type execution struct {
	// ctx is the Restate context of the Run handler.
	ctx restatesdk.WorkflowContext

	// environment supplies registry and exact native service names.
	environment Environment

	// handler is the workflow being run.
	handler workflow.WorkflowHandler

	// id is the workflow key.
	id string

	// deadline is the durable timer of MaxDuration, created when the run
	// starts. Every blocking call also waits for it.
	deadline restatesdk.AfterFuture

	// done is closed when the run is canceled or timed out.
	done chan struct{}

	// mu guards the fields below.
	mu sync.Mutex

	// err is why done is closed.
	err error

	// sdkPanic is true once a panic of the SDK passed through a runtime call.
	sdkPanic bool

	// lastNow is the last value Now returned.
	lastNow time.Time
}

var _ workflow.Runtime = (*execution)(nil)

// newRuntime starts the MaxDuration timer and returns the runtime.
func newRuntime(ctx restatesdk.WorkflowContext, environment Environment, handler workflow.WorkflowHandler) *execution {
	return &execution{
		ctx:         ctx,
		environment: environment,
		handler:     handler,
		id:          restatesdk.Key(ctx),
		deadline:    restatesdk.After(ctx, handler.Options.MaxDuration),
		done:        make(chan struct{}),
	}
}

// ID returns the workflow key.
// The ingress decodes transport escaping before the key reaches this attempt,
// so application IDs retain their original bytes across replay and retries.
func (execution *execution) ID() string {
	return execution.id
}

// Name returns the workflow name.
// It comes from the immutable registered handler, not the native service prefix.
func (execution *execution) Name() string {
	return execution.handler.Name
}

// Logger returns the replay-aware logger of Restate.
// The SDK suppresses replayed records; it is not a new logger or telemetry span
// for each orchestration replay.
func (execution *execution) Logger() *slog.Logger {
	return execution.ctx.Log()
}

// Raw returns the [restatesdk.WorkflowContext].
// Native operations retain the SDK's deterministic execution restrictions;
// callers must not use this context concurrently from application goroutines.
func (execution *execution) Raw() any {
	return execution.ctx
}

// Done is closed once the run observed its cancellation or the MaxDuration
// timer.
// It is local to this attempt. Durable waits observe engine cancellation at
// native boundaries; a timer alone cannot interrupt arbitrary application code.
func (execution *execution) Done() <-chan struct{} {
	return execution.done
}

// Err returns why Done is closed, or nil.
// This reports the first observed stop reason, not an independent remote-status
// query or a wall-clock evaluation of the durable deadline.
func (execution *execution) Err() error {
	return execution.current()
}

// Repanic reports whether the panic v is an engine control panic, which the
// workflow handler must raise again. The Restate SDK suspends and aborts
// attempts with panics from inside its calls, so any panic that passed
// through a runtime call counts.
func (execution *execution) Repanic(any) bool {
	execution.mu.Lock()
	defer execution.mu.Unlock()

	return execution.sdkPanic
}

// current returns why the run stopped, or nil.
func (execution *execution) current() error {
	execution.mu.Lock()
	defer execution.mu.Unlock()

	return execution.err
}

// stop records why the run stopped and closes done. The first reason wins.
func (execution *execution) stop(err error) error {
	execution.mu.Lock()
	defer execution.mu.Unlock()

	if execution.err == nil {
		execution.err = err
		close(execution.done)
	}

	return execution.err
}

// guard runs op unless the run already stopped, and notes a panic of the SDK
// before it continues up to [execution.Repanic].
func (execution *execution) guard(op func() error) error {
	if err := execution.current(); err != nil {
		return err
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			execution.mu.Lock()
			execution.sdkPanic = true
			execution.mu.Unlock()

			panic(recovered)
		}
	}()

	return op()
}

// race waits for the first of futures to complete and returns its index. It
// also waits for the MaxDuration timer and for cancellation, which stop the
// run and return their error.
func (execution *execution) race(futures ...restatesdk.Future) (int, error) {
	all := append(append(make([]restatesdk.Future, 0, len(futures)+1), futures...), execution.deadline)

	first, err := restatesdk.WaitFirst(execution.ctx, all...)

	if err != nil {
		return -1, execution.stop(fmt.Errorf("invocation canceled: %w", workflow.ErrCanceled))
	}

	for index, future := range futures {
		if first == future {
			return index, nil
		}
	}

	if first == restatesdk.Future(execution.deadline) {
		return -1, execution.stop(fmt.Errorf("%w: max duration %s exceeded", workflow.ErrTimeout, execution.handler.Options.MaxDuration))
	}

	return -1, errors.New("restate: wait returned an unknown future")
}

// await converts the error of an awaited operation. Cancellation stops the
// run.
func (execution *execution) await(err restatesdk.TerminalError) error {
	if err.Code() == protocol.CodeCanceled {
		return execution.stop(fmt.Errorf("invocation canceled: %w", workflow.ErrCanceled))
	}

	return fromTerminal(err)
}

// fromTerminal converts a terminal error of an awaited Restate operation to a
// workflow error. It preserves the core envelope rather than exposing only the
// transport's numeric failure code.
func fromTerminal(err restatesdk.TerminalError) error {
	return protocol.FromFailure(int(err.Code()), err.Message())
}

// Now returns the time of the first execution. Each call is a durable Run
// action, so replays see the same values and each call adds one journal
// entry; workflows that call Now in a loop should keep the loop small.
func (execution *execution) Now() time.Time {
	var now time.Time

	err := execution.guard(func() error {
		value, err := restatesdk.Run(execution.ctx, func(restatesdk.RunContext) (time.Time, error) {
			return time.Now(), nil
		}, restatesdk.WithName("now"))

		if err != nil {
			return execution.await(err)
		}

		now = value

		return nil
	})

	if err != nil {
		execution.mu.Lock()
		defer execution.mu.Unlock()

		return execution.lastNow
	}

	execution.mu.Lock()
	execution.lastNow = now
	execution.mu.Unlock()

	return now
}

// UUID returns a deterministic identifier, the same on replays.
// It uses the native replay-safe generator; an already stopped attempt returns
// an empty string because it can no longer perform successful runtime work.
func (execution *execution) UUID() string {
	var id string

	_ = execution.guard(func() error { //nolint:errcheck // an aborted run has no use for an id
		id = restatesdk.UUID(execution.ctx).String()

		return nil
	})

	return id
}

// Sleep pauses the run with a durable timer.
// The workflow deadline and native cancellation can end the wait early. A timer
// adds a native journal operation rather than sleeping a process-local thread.
func (execution *execution) Sleep(d time.Duration) error {
	return execution.guard(func() error {
		_, err := execution.race(restatesdk.After(execution.ctx, d))

		return err
	})
}

// Wait waits for the promise of the signal, and for the timeout if set.
// Zero leaves this wait unbounded by a local timeout, but not by MaxDuration.
// Promise values survive suspension; their first-value semantics are native.
func (execution *execution) Wait(signal string, timeout time.Duration) ([]byte, error) {
	var out []byte

	err := execution.guard(func() error {
		promise := restatesdk.Promise[protocol.Payload](execution.ctx, protocol.SignalPromise+signal)
		futures := []restatesdk.Future{promise}

		if timeout > 0 {
			futures = append(futures, restatesdk.After(execution.ctx, timeout))
		}

		switch index, err := execution.race(futures...); {
		case err != nil:
			return err
		case index != 0:
			return fmt.Errorf("%w: no %s signal within %s", workflow.ErrTimeout, signal, timeout)
		}

		value, err := promise.Result()

		if err != nil {
			return execution.await(err)
		}

		out = value

		return nil
	})

	if err != nil {
		return nil, err
	}

	return out, nil
}

// Select waits for the first of the promises and timers of cases.
// Signal cases return bytes from their durable promise. Timer cases return nil
// bytes; a run stop returns index -1 and its cancellation/timeout failure.
func (execution *execution) Select(cases []workflow.RawCase) (int, []byte, error) {
	var (
		chosen int
		out    []byte
	)

	err := execution.guard(func() error {
		futures := make([]restatesdk.Future, len(cases))
		promises := make([]restatesdk.DurablePromise[protocol.Payload], len(cases))

		for index, option := range cases {
			if option.Signal == "" {
				futures[index] = restatesdk.After(execution.ctx, option.After)

				continue
			}

			promises[index] = restatesdk.Promise[protocol.Payload](execution.ctx, protocol.SignalPromise+option.Signal)
			futures[index] = promises[index]
		}

		// Keep the selection error scoped separately from the SDK terminal
		// error returned by the winning promise below.
		{
			index, err := execution.race(futures...)

			if err != nil {
				return err
			}

			chosen = index
		}

		if promises[chosen] == nil {
			return nil
		}

		value, err := promises[chosen].Result()

		if err != nil {
			return execution.await(err)
		}

		out = value

		return nil
	})

	if err != nil {
		return -1, nil, err
	}

	return chosen, out, nil
}

// Step runs a registered step as a durable Run action.
// Only native Run closures invoke the handler and extract trace propagation;
// replayed completion values skip that attempt boundary entirely.
func (execution *execution) Step(name string, in []byte, policy workflow.StepOptions) ([]byte, error) {
	handler, ok := execution.environment.Registry.Step(name)

	if !ok {
		return nil, fmt.Errorf("step %s is not registered: %w", name, workflow.ErrNotFound)
	}

	var out []byte

	err := execution.guard(func() error {
		future := restatesdk.RunAsync(execution.ctx, func(runCtx restatesdk.RunContext) (protocol.Payload, error) {
			ctx := execution.environment.Registry.NewTraceContext(runCtx, protocol.TraceCarrier(runCtx))

			return attempt(ctx, handler, in, policy)
		}, runOptions(name, policy)...)

		if _, err := execution.race(future); err != nil {
			return err
		}

		value, err := future.Result()

		if err != nil {
			return execution.await(err)
		}

		out = value

		return nil
	})

	if err != nil {
		return nil, err
	}

	return out, nil
}

// Child rejects before any send, attach, or cancellation command. The native
// durable send receipt contains an invocation ID but no created-versus-existing
// ownership decision. Canceling that ID could therefore stop another owner's run;
// omitting cancellation would instead violate newly created child lifetime.
// A separate status lookup cannot resolve this race. Existing run-state errors
// retain precedence, and this rejection adds no replay marker or control store.
//
//	_, err := execution.Child("fulfill", "order-42", input)
//	if errors.Is(err, workflow.ErrUnsupported) {
//		return nil, err // No child was submitted; choose a supported engine.
//	}
func (execution *execution) Child(string, string, []byte) ([]byte, error) {
	err := execution.guard(func() error {
		return fmt.Errorf("restate: atomic child cancellation ownership is unavailable: %w", workflow.ErrUnsupported)
	})

	return nil, err
}

// Spawn sends to the Run handler of the workflow without waiting. Restate
// deduplicates a send to a workflow id that exists, so a replay spawns once.
func (execution *execution) Spawn(name, id string, in []byte) error {
	return execution.guard(func() error {
		restatesdk.WorkflowSend(execution.ctx, execution.environment.Service(name), id, protocol.RunHandler).Send(protocol.Payload(in),
			restatesdk.WithHeaders(protocol.TraceHeaders(protocol.TraceCarrier(execution.ctx))))

		// A send is applied asynchronously. A short durable timer makes
		// the run visible to Status before Spawn returns.
		_, err := execution.race(restatesdk.After(execution.ctx, protocol.SpawnSettle))

		return err
	})
}
