package workflow

import (
	"context"
	"fmt"
	"time"

	"arkoren.dev/core/internal/name"
	"arkoren.dev/core/service/workflow/internal/execution"
)

// Definition is a workflow or step that a [Driver] can run. [*Workflow] and
// [*Step] implement it; pass them to [Handlers].
// The interface is sealed so registry validation and typed decoding remain
// owned by this package rather than arbitrary caller implementations.
//
//	definitions := []workflow.Definition{Onboard, SendWelcome}
//	registry, err := workflow.NewRegistry(definitions)
//	if err != nil {
//		return err
//	}
//	_ = registry.Workflows()
type Definition interface {
	// definition limits implementations to workflow-owned declarations.
	definition()
	// register installs the declaration's handler in registry.
	register(registry *Registry) error
}

// Workflow is a named, typed, durable function together with the run settings
// that are application code rather than transport data. Create it with [New],
// configure it through [Builder], pass it to [Handlers] on the worker, and
// start it with [Client.Start].
//
// A Workflow is an immutable published definition: callbacks edit a draft, and
// the first error found is kept and reported by [Workflow.Err]. [Handlers]
// rejects a workflow whose Err is not nil. Always create workflows with [New];
// the zero value has no name and no function.
//
// Example:
//
//	var Onboard = workflow.New("onboard", onboard, func(definition *workflow.Builder) {
//		definition.MaxDuration(24 * time.Hour)
//		definition.Retention(7 * 24 * time.Hour)
//		definition.Description("Welcomes a new user and waits for confirmation.")
//	})
type Workflow[In, Out any] struct {
	// name is the stable identifier of the workflow.
	name string

	// fn is the workflow body.
	fn func(*Context, In) (Out, error)

	// options are the run settings.
	options WorkflowOptions

	// err is the first error found while building the workflow.
	err error
}

// New returns the workflow called identity that runs fn. The name must satisfy
// the shared identifier rule of 1-64 lowercase letters, digits, or hyphens
// with no hyphen at either end, and fn must not be nil. Violations are kept
// on the value and reported by [Workflow.Err] and [Handlers] as
// [ErrInvalidName] or [ErrNilFunc]. fn must use the
// [Context] for time, randomness, and side effects, so a driver can replay it.
// Set [Builder.MaxDuration] before registering: it is required.
// Callbacks run directly once, synchronously in order on one fresh, nonnil
// draft, even when name or fn is invalid. Nil entries and callback panics
// propagate; expanded nil or empty slices supply no edits. Publication copies
// the completed settings; retained drafts cannot change the result.
//
// Example:
//
//	onboard := workflow.New("onboard", onboardUser, func(definition *workflow.Builder) {
//		definition.MaxDuration(24 * time.Hour)
//	})
//	registry, err := workflow.Handlers(onboard)
//	if err != nil {
//		return err
//	}
//	_ = registry
func New[In, Out any](identity string, fn func(*Context, In) (Out, error), configure ...BuilderFunc) *Workflow[In, Out] {
	def := &Workflow[In, Out]{
		name: identity,
		fn:   fn,
	}

	if err := name.Identifier(identity); err != nil {
		def.err = fmt.Errorf("workflow name %q: %w: %w", identity, ErrInvalidName, err)
	} else if fn == nil {
		def.err = fmt.Errorf("workflow %s: %w", identity, ErrNilFunc)
	}

	definition := &Builder{
		name: identity,
		err:  def.err,
	}

	for _, configure := range configure {
		configure(definition)
	}

	def.options = definition.options

	def.err = definition.err

	return def
}

// Builder configures one workflow declaration before [New] publishes
// its immutable snapshot. Retaining this draft does not permit later changes
// to the published workflow. Settings preserve the first declaration error.
//
// Example:
//
//	workflow.New("onboard", onboard, func(definition *workflow.Builder) {
//		definition.MaxDuration(24 * time.Hour)
//	})
type Builder struct {
	// name identifies the declaration in validation errors.
	name string
	// options holds the draft's scalar settings, copied at publication.
	options WorkflowOptions
	// err retains the first construction or setting error.
	err error
}

// MaxDuration bounds the whole run. It is required: [Handlers] fails for a
// workflow without it. When it expires the run fails with [ErrTimeout]. A
// value of zero or less is an error.
// It returns the same draft and preserves any earlier declaration error.
//
// Example:
//
//	workflow.New("onboard", onboard, func(definition *workflow.Builder) {
//		definition.MaxDuration(24 * time.Hour)
//	})
func (definition *Builder) MaxDuration(duration time.Duration) *Builder {
	if definition.err != nil {
		return definition
	}

	if duration <= 0 {
		definition.err = fmt.Errorf("workflow %s: max duration %s: %w", definition.name, duration, ErrInvalidOptions)

		return definition
	}

	definition.options.MaxDuration = duration

	return definition
}

// Retention sets how long the engine keeps a finished run. Zero uses the
// engine default and a negative value is an error.
// It returns the same draft and preserves the first declaration error.
//
// Example:
//
//	workflow.New("onboard", onboard, func(definition *workflow.Builder) {
//		definition.MaxDuration(time.Hour)
//		definition.Retention(7 * 24 * time.Hour)
//	})
func (definition *Builder) Retention(duration time.Duration) *Builder {
	if definition.err != nil {
		return definition
	}

	if duration < 0 {
		definition.err = fmt.Errorf("workflow %s: negative retention %s: %w", definition.name, duration, ErrInvalidOptions)

		return definition
	}

	definition.options.Retention = duration

	return definition
}

// Description sets free text that documents the workflow, replacing any earlier
// description. Empty text clears it. It returns the same draft without clearing
// an earlier validation error.
//
// Example:
//
//	workflow.New("onboard", onboard, func(definition *workflow.Builder) {
//		definition.MaxDuration(time.Hour)
//		definition.Description("Welcomes a new user.")
//	})
func (definition *Builder) Description(text string) *Builder {
	definition.options.Description = text

	return definition
}

// Name returns the stable identifier supplied to [New]. Settings cannot change
// it; registration and client operations use this name across processes.
//
// Example:
//
//	name := workflow.New("onboard", onboard).Name()
//	fmt.Println(name) // onboard
func (workflow *Workflow[In, Out]) Name() string {
	return workflow.name
}

// Err returns the first declaration error, or nil for valid construction.
// Missing MaxDuration is checked only by [Handlers], not by Err.
// A zero Workflow reports ErrInvalidName before registration-only checks.
//
// Example:
//
//	definition := workflow.New("Bad", onboard)
//	if errors.Is(definition.Err(), workflow.ErrInvalidName) {
//		return definition.Err()
//	}
func (workflow *Workflow[In, Out]) Err() error {
	if workflow.err == nil && workflow.name == "" {
		return fmt.Errorf("workflow was not built with New: %w", ErrInvalidName)
	}

	return workflow.err
}

// definition seals the set of registry declarations to workflows and steps.
func (workflow *Workflow[In, Out]) definition() {
}

// register validates and snapshots the name, policy, and typed function into
// registry. The handler does not retain the published pointer, so assigning a
// different definition through that pointer cannot change registered execution.
func (workflow *Workflow[In, Out]) register(registry *Registry) error {
	if workflow == nil {
		return fmt.Errorf("register workflow: %w", ErrNilFunc)
	}

	if err := workflow.Err(); err != nil {
		return fmt.Errorf("register workflow: %w", err)
	}

	if workflow.options.MaxDuration <= 0 {
		return fmt.Errorf("register workflow %s: %w", workflow.name, ErrMissingMaxDuration)
	}

	if registry.taken(workflow.name) {
		return fmt.Errorf("register workflow %s: %w", workflow.name, ErrDuplicateName)
	}

	codec := registry.codec
	identity, fn := workflow.name, workflow.fn

	registry.workflows[identity] = WorkflowHandler{
		Name:    identity,
		Options: workflow.options,
		Handle: func(rt Runtime, raw []byte) (out []byte, err error) {
			// Runtime carries no context.Context, so the panic log cannot be correlated.
			defer execution.Protect(context.Background(), rt.Logger(), execution.Repanicker(rt), nil, &err)

			var input In

			if err := DecodeValue(codec, raw, &input); err != nil {
				return nil, Permanent(fmt.Errorf("workflow %s: %w", identity, err))
			}

			result, err := fn(newContext(rt, codec), input)

			if err != nil {
				return nil, err
			}

			return EncodeValue(codec, result)
		},
	}

	return nil
}

// BuilderFunc edits the fresh workflow declaration draft used by [New].
// Callbacks run synchronously once in order, even for invalid declarations;
// supplied nil callbacks panic. Set MaxDuration before registering the result.
//
// Example:
//
//	policy := workflow.BuilderFunc(func(definition *workflow.Builder) {
//		definition.MaxDuration(time.Minute)
//	})
//	onboard := workflow.New("onboard", onboardUser, policy)
//	if err := onboard.Err(); err != nil {
//		return err
//	}
type BuilderFunc = func(*Builder)
