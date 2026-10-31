// Package execution owns Hatchet native waits, step joins and attempt execution.
package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/hatchet/hatchetworkflow/internal/protocol"
)

// Environment supplies one run's native execution capabilities and identities.
type Environment struct {
	// Raw is the actual SDK durable context, not a context-only wrapper.
	Raw hatchet.DurableContext
	// Context bounds native result and marker observations.
	Context context.Context
	// Registry supplies immutable step policy and attempt trace extraction.
	Registry *workflow.Registry
	// Task resolves a registered standalone task without importing Runner.
	Task func(string) (*hatchet.StandaloneTask, bool)
	// FirstValue verifies the marker's physical owner before returning bytes.
	FirstValue func(context.Context, string, string, string) ([]byte, error)
	// SignalKey returns the complete SDK-namespaced native event key.
	SignalKey func(string) string
	// Key is the verified versioned logical execution key.
	Key string
	// Trace is forwarded data, never extracted during orchestration replay.
	Trace map[string]string
	// Err reports the owning runtime's cancellation or deadline cause.
	Err func() error
	// Remaining reads the next replay-stable wait budget from its durable memo.
	Remaining func() (time.Duration, error)
}

// minSleep is the shortest sleep condition Hatchet accepts.
const minSleep = time.Millisecond

// Select blocks until a physical-execution signal, timer, or MaxDuration fires.
// Signals use the same verified scope as senders and reject legacy/foreign
// event or marker owners with workflow.ErrNotFound. All retained events in that
// physical scope are eligible, including signals accepted before a worker began.
// The fixed historical boundary is replay-stable and does not change the
// separately memoized first-worker timeout origin.
func (execution Environment) Select(cases []workflow.RawCase) (int, []byte, error) {
	if err := execution.Err(); err != nil {
		return -1, nil, err
	}

	remaining, err := execution.Remaining()

	if err != nil {
		return -1, nil, err
	}

	if remaining <= 0 {
		return -1, nil, workflow.ErrTimeout
	}

	keys, condition := execution.waitConditions(cases, remaining)

	result, err := execution.Raw.WaitFor(condition)

	if err != nil {
		if cause := execution.Err(); cause != nil {
			return -1, nil, cause
		}

		return -1, nil, fmt.Errorf("wait: %w", err)
	}

	return execution.selectedCase(cases, keys, result)
}

// waitConditions builds native predicates in declaration order, deduplicating
// identical keys before appending the run limit. The fixed historical boundary,
// minimum sleeps, and predicate order are part of the retained replay protocol.
func (execution Environment) waitConditions(cases []workflow.RawCase, remaining time.Duration) ([]string, hatchet.Condition) {
	executionID := execution.Raw.WorkflowRunId()
	scope := protocol.SignalScope(execution.Key, executionID)

	keys := make([]string, len(cases))
	seen := map[string]bool{}

	var conditions []hatchet.Condition

	add := func(key string, condition hatchet.Condition) {
		if !seen[key] {
			seen[key] = true

			conditions = append(conditions, condition)
		}
	}

	for index, option := range cases {
		if option.Signal != "" {
			keys[index] = execution.SignalKey(option.Signal)
			// Physical isolation makes all retained history safe to consider;
			// worker-relative timestamps would discard older queued signals.
			add(keys[index], hatchet.UserEventCondition(keys[index], "",
				hatchet.WithEventScope(scope), hatchet.WithConsiderEventsSince(time.Unix(0, 0).UTC())))

			continue
		}

		after := max(option.After, minSleep)
		keys[index] = sleepKey(after)
		add(keys[index], hatchet.SleepCondition(after))
	}

	add(sleepKey(max(remaining, minSleep)), hatchet.SleepCondition(max(remaining, minSleep)))

	condition := conditions[0]

	if len(conditions) > 1 {
		condition = hatchet.OrCondition(conditions...)
	}

	return keys, condition
}

// selectedCase preserves declaration-order precedence when several native keys
// fire together. An unmatched fired key is the run limit; no fired keys is an
// engine error. Signal payloads undergo physical-owner verification separately.
func (execution Environment) selectedCase(cases []workflow.RawCase, keys []string, result *hatchet.WaitResult) (int, []byte, error) {
	fired := map[string]bool{}

	for _, key := range result.Keys() {
		fired[key] = true
	}

	for index, option := range cases {
		if !fired[keys[index]] {
			continue
		}

		if option.Signal == "" {
			return index, nil, nil
		}

		first, err := execution.signalValue(result, keys[index], option.Signal)

		if err != nil {
			return -1, nil, err
		}

		return index, first, nil
	}

	// Any other fired key is the limit.
	if len(fired) > 0 {
		return -1, nil, workflow.ErrTimeout
	}

	return -1, nil, fmt.Errorf("wait: no condition fired (keys %v)", result.Keys())
}

// signalValue verifies the native event's protocol and physical owner before
// consulting the first-value marker. Parsing failures retain precedence over
// ownership failures, which in turn precede any marker lookup or transport error.
func (execution Environment) signalValue(result *hatchet.WaitResult, key, signal string) ([]byte, error) {
	var payload protocol.Wire

	if err := result.Unmarshal(key, &payload); err != nil {
		return nil, fmt.Errorf("decode signal %s: %w", signal, err)
	}

	executionID := execution.Raw.WorkflowRunId()

	if payload.SignalVersion != protocol.SignalVersion || payload.ExecutionID != executionID {
		return nil, fmt.Errorf("signal event execution mismatch: %w", workflow.ErrNotFound)
	}

	scope := protocol.SignalScope(execution.Key, executionID)

	return execution.FirstValue(execution.Context, payload.Marker,
		protocol.SignalMarkerKey(scope, signal), executionID)
}

// Step submits and joins the registered native step with its existing trace and
// timeout/attempt envelope. A completed zero-delay retry policy carries an
// additive flag only when registration is positive, so unaffected replay inputs
// remain byte-compatible. An expired parent deadline outranks the native result.
func (execution Environment) Step(name string, in []byte, o workflow.StepOptions) ([]byte, error) {
	if err := execution.Err(); err != nil {
		return nil, err
	}

	task, ok := execution.Task(name)

	if !ok {
		return nil, fmt.Errorf("step %s: %w: not registered", name, workflow.ErrNotFound)
	}

	input := protocol.Wire{
		Trace:    execution.Trace,
		Data:     string(in),
		Timeout:  int64(o.Timeout),
		Attempts: o.Attempts,
	}

	registered, _ := execution.Registry.Step(name)
	immediate := o.Initial == 0 && o.Attempts > 1
	input.RetryWithoutDelay = immediate && registered.Options.Initial != 0

	result, err := task.Run(execution.Raw, input)

	if cause := execution.Err(); cause != nil {
		return nil, cause
	}

	if err != nil {
		return nil, childFailure(err)
	}

	var out protocol.Wire

	if err := result.Into(&out); err != nil {
		return nil, fmt.Errorf("decode step %s output: %w", name, err)
	}

	return []byte(out.Data), nil
}

// childFailure turns the error of a child run into a facade error.
func childFailure(err error) error {
	if _, ok := errors.AsType[*workflow.Error](err); ok {
		return err
	}

	return protocol.Failure(err.Error())
}

// sleepKey returns the key under which Hatchet reports a sleep condition.
func sleepKey(d time.Duration) string {
	return "sleep:" + d.String()
}
