package workflow

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/collection"
)

// WorkflowHandler runs one workflow definition on bytes.
// Registration snapshots the declaration; adapters must supply a replay-safe
// Runtime and preserve failures using EncodeError at process boundaries.
//
//	handler, ok := registry.Workflow("onboard")
//	if !ok {
//		return workflow.ErrNotFound
//	}
//	output, err := handler.Handle(runtime, input)
//	if err != nil {
//		return err
//	}
//	_ = output
type WorkflowHandler struct { //nolint:revive // preserve the public raw record paired with StepHandler
	// Name is the workflow name.
	Name string

	// Options are the settings of the definition.
	Options WorkflowOptions

	// Handle decodes in, runs the function with a [Context] over rt, and
	// returns the encoded output. A failure is returned as the error of the
	// function; use [EncodeError] to carry it.
	Handle func(rt Runtime, in []byte) ([]byte, error)
}

// StepHandler runs one step definition on bytes.
// Call Handle only for actual attempts, not for replayed recorded results.
// Its context carries cancellation and worker trace-parent extraction.
//
//	handler, ok := registry.Step("charge")
//	if !ok {
//		return workflow.ErrNotFound
//	}
//	ctx = registry.NewTraceContext(ctx, carrier)
//	output, err := handler.Handle(ctx, input)
//	if err != nil {
//		return err
//	}
//	_ = output
type StepHandler struct {
	// Name is the step name.
	Name string

	// Options are the default settings of the step.
	Options StepOptions

	// Handle decodes in, runs the function, and returns the encoded output.
	// An error that satisfies [ErrPermanent] must not be retried.
	// Registered handlers observe each actual invocation with the registry's
	// telemetry service. Drivers must not invoke this callback for replayed
	// results; extract the carrier with Registry.NewTraceContext beforehand.
	//
	//	ctx = registry.NewTraceContext(ctx, options.Trace)
	//	out, err := handler.Handle(ctx, input)
	//	if err != nil {
	//		return err
	//	}
	//	_ = out
	Handle func(ctx context.Context, in []byte) ([]byte, error)
}

// Registry holds the handlers of validated definitions for a driver to
// register. Use [NewRegistry] to select worker telemetry or a custom codec;
// [Handlers] uses JSON and follows the dynamic process telemetry default.
// Published handlers snapshot settings; service implementations stay shared.
//
//	registry, err := workflow.NewRegistry(definitions, func(worker *workflow.RegistryBuilder) {
//		worker.Telemetry(observations)
//	})
//	if err != nil {
//		return err
//	}
//	_ = registry.Steps()
type Registry struct {
	// telemetry belongs to this worker registration, never to a client.
	telemetry *telemetry.Telemetry
	// codec is captured by the handlers when definitions are registered.
	codec Codec

	// workflows maps validated names to snapshotted workflow handlers.
	workflows map[string]WorkflowHandler

	// steps maps validated names to snapshotted step handlers.
	steps map[string]StepHandler
}

// Handlers validates defs and returns their handlers, encoded with [JSON].
// It returns the [Workflow.Err] or [Step.Err] of a definition, so an invalid
// name satisfies [ErrInvalidName] and a nil function [ErrNilFunc], then
// [ErrDuplicateName] when two definitions share a name and
// [ErrMissingMaxDuration] for a workflow without [Builder.MaxDuration].
// Each handler captures the name, function, and settings independently of the
// published pointer; subsequent assignment through that pointer does not change
// the registry. Captured function closures retain their own ownership rules.
//
// Example:
//
//	registry, err := workflow.Handlers(Onboard, SendWelcome)
//	if err != nil {
//		return err
//	}
//	_ = registry.Workflows()
func Handlers(defs ...Definition) (*Registry, error) {
	return HandlersWith(JSON{}, defs...)
}

// HandlersWith is [Handlers] with a custom [Codec]. Use the same codec for
// the [Client].
// Nil selects JSON. Codec implementations stay shared, while registrations
// snapshot the validated name, function, and policy of each definition.
//
//	registry, err := workflow.HandlersWith(workflow.JSON{}, Onboard, SendWelcome)
//	if err != nil {
//		return err
//	}
//	_ = registry.Workflows()
func HandlersWith(codec Codec, defs ...Definition) (*Registry, error) {
	return newRegistry(&RegistryBuilder{codec: codec}, defs)
}

// newRegistry snapshots validated handlers with the completed worker settings.
// It performs no telemetry I/O and never invokes orchestration functions.
func newRegistry(settings *RegistryBuilder, defs []Definition) (*Registry, error) {
	codec := settings.codec

	if codec == nil {
		codec = JSON{}
	}

	registry := &Registry{
		telemetry: settings.telemetry,
		codec:     codec,
		workflows: map[string]WorkflowHandler{},
		steps:     map[string]StepHandler{},
	}

	for _, def := range defs {
		if def == nil {
			return nil, fmt.Errorf("register definition: %w", ErrNilFunc)
		}

		if err := def.register(registry); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

// taken reports whether name is registered as a workflow or a step.
func (registry *Registry) taken(name string) bool {
	_, workflow := registry.workflows[name]
	_, step := registry.steps[name]

	return workflow || step
}

// Workflows returns the workflow handlers sorted by name.
// The returned slice is independent of the registry; changing its records
// cannot replace registered handlers. Handler functions remain shared.
// This eager snapshot is not a live query; collection operations do not clone
// functions, their captures, or services used by those functions.
//
//	short := registry.Workflows().Filter(func(_ int, handler workflow.WorkflowHandler) bool {
//		return handler.Options.MaxDuration <= time.Hour
//	})
//	for _, handler := range short {
//		fmt.Println(handler.Name, handler.Options.MaxDuration)
//	}
func (registry *Registry) Workflows() collection.Slice[WorkflowHandler] {
	return collection.NewSlice(slices.Collect(func(yield func(WorkflowHandler) bool) {
		for _, name := range slices.Sorted(maps.Keys(registry.workflows)) {
			if !yield(registry.workflows[name]) {
				return
			}
		}
	}))
}

// Steps returns the step handlers sorted by name.
// The slice and records are copies; the registered typed functions are shared.
// This eager snapshot is not a live query; wrapping it adds collection
// operations without acquiring ownership of functions or their captures.
//
//	retried := registry.Steps().Filter(func(_ int, handler workflow.StepHandler) bool {
//		return handler.Options.Attempts > 1
//	})
//	for _, handler := range retried {
//		fmt.Println(handler.Name, handler.Options.Attempts)
//	}
func (registry *Registry) Steps() collection.Slice[StepHandler] {
	return collection.NewSlice(slices.Collect(func(yield func(StepHandler) bool) {
		for _, name := range slices.Sorted(maps.Keys(registry.steps)) {
			if !yield(registry.steps[name]) {
				return
			}
		}
	}))
}

// Workflow returns the handler of the workflow called name.
// The boolean is false for an absent name. Editing the returned record cannot
// replace the registry's snapshot or its scalar policy.
//
//	handler, ok := registry.Workflow("onboard")
//	if !ok {
//		return workflow.ErrNotFound
//	}
//	_ = handler.Options.MaxDuration
func (registry *Registry) Workflow(name string) (WorkflowHandler, bool) {
	handler, ok := registry.workflows[name]

	return handler, ok
}

// Step returns the handler of the step called name.
// The boolean is false for an absent name. Drivers use this lookup for actual
// attempts, with per-call derived policy supplied separately by Runtime.Step.
//
//	handler, ok := registry.Step("charge")
//	if !ok {
//		return workflow.ErrNotFound
//	}
//	_ = handler.Options.Timeout
func (registry *Registry) Step(name string) (StepHandler, bool) {
	handler, ok := registry.steps[name]

	return handler, ok
}

// RegistryBuilder configures worker registration independently of client pods.
// Its final state is copied before handlers are published; services stay shared.
//
//	registry, err := workflow.NewRegistry(definitions, func(worker *workflow.RegistryBuilder) {
//		worker.Telemetry(observations)
//	})
//	if err != nil {
//		return err
//	}
//	_ = registry.Steps()
type RegistryBuilder struct {
	// codec must agree with the client; nil selects JSON.
	codec Codec
	// telemetry observes actual attempts, not orchestration/replay.
	telemetry *telemetry.Telemetry
}

// RegistryBuilderFunc configures one fresh worker-registration draft. Callbacks
// run once in order, including for invalid definitions; nil callbacks panic.
//
//	var configure workflow.RegistryBuilderFunc = func(worker *workflow.RegistryBuilder) {
//		worker.Codec(workflow.JSON{})
//	}
//	registry, err := workflow.NewRegistry(definitions, configure)
//	if err != nil {
//		return err
//	}
//	_ = registry
type RegistryBuilderFunc = func(*RegistryBuilder)

// Codec replaces the worker payload codec; nil selects JSON. The codec is
// shared, not cloned, and must match the client. It returns the same draft.
//
//	registry, err := workflow.NewRegistry(definitions, func(worker *workflow.RegistryBuilder) {
//		worker.Codec(workflow.JSON{})
//	})
//	if err != nil {
//		return err
//	}
//	_ = registry.Steps()
func (worker *RegistryBuilder) Codec(value Codec) *RegistryBuilder {
	worker.codec = value

	return worker
}

// Telemetry selects worker attempt observations and carrier extraction. Nil
// follows the current process default at each attempt, independently of the
// client's choice. It does not observe orchestration or terminal run outcomes.
//
//	registry, err := workflow.NewRegistry(definitions, func(worker *workflow.RegistryBuilder) {
//		worker.Telemetry(observations)
//	})
//	if err != nil {
//		return err
//	}
//	_ = registry.Steps()
func (worker *RegistryBuilder) Telemetry(value *telemetry.Telemetry) *RegistryBuilder {
	worker.telemetry = value

	return worker
}

// NewRegistry validates definitions and publishes an immutable worker registry.
// It has the same validation and handler-snapshot semantics as HandlersWith.
// Configure worker telemetry here even when clients run in a different pod.
// Without callbacks JSON and the dynamic process telemetry default apply.
//
//	registry, err := workflow.NewRegistry([]workflow.Definition{Onboard, SendWelcome},
//		func(worker *workflow.RegistryBuilder) {
//			worker.Telemetry(observations)
//		})
//	if err != nil {
//		return err
//	}
//	_ = registry.Workflows()
func NewRegistry(definitions []Definition, configure ...RegistryBuilderFunc) (*Registry, error) {
	draft := &RegistryBuilder{}

	for _, configure := range configure {
		configure(draft)
	}

	return newRegistry(draft, definitions)
}
