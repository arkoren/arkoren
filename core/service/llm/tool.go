package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"arkoren.dev/core/internal/name"
	"arkoren.dev/core/service/llm/internal/engine"
	"arkoren.dev/foundation/validate"
)

// genericToolFailure is what the model sees when a tool fails with an error
// that is not a [ToolError], so internal details such as SQL or host names
// never reach it.
const genericToolFailure = "the tool failed"

// Tool is a function the model may call during [Model.Run]. It is an
// immutable definition, normally a package-level variable shared by agents:
// the model sees its name, description and the JSON Schema of its arguments,
// and the function receives the arguments already decoded and validated.
//
// A Tool whose definition is invalid reports it from [Tool.Err], and an
// [Agent] that uses it reports the same error. The zero Tool is invalid.
//
// Example:
//
//	lookup := llm.NewTool("lookup-order", "Find an order.", orderArgs, findOrder)
//	if err := lookup.Err(); err != nil {
//		return err
//	}
//	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
//		agent.Tools(lookup)
//	})
type Tool struct {
	// name identifies the tool in [ToolCall] parts.
	name string

	// description tells the model when to use the tool.
	description string

	// schema is the JSON Schema of the arguments.
	schema []byte

	// call decodes and validates the arguments, runs the function and
	// encodes its result.
	call func(ctx context.Context, arguments []byte) (string, error)

	// available is shared capability policy, evaluated afresh for each context.
	// Its captures remain application-owned and must support concurrent calls.
	available func(context.Context) (bool, error)

	// err is the first invalid input.
	err error
}

// NewTool returns a Tool called toolName. description tells the model when to
// use it. schema describes and validates the arguments: its JSON Schema is
// sent to the model, and every call is decoded with json/v2 into In and
// checked with [validate.Schema.Parse] before fn runs, so fn sees defaults
// applied and never sees invalid input.
//
// toolName follows the naming rules of the async packages: 1-64 bytes of
// lowercase letters, digits and inner hyphens. The first invalid input (the
// name, a nil schema or fn, or a schema that cannot render) is kept and
// reported by [Tool.Err] wrapped in [ErrInvalidName] or [ErrInvalidRequest].
//
// fn's result is sent to the model as is when it is a string and as JSON
// otherwise. Return an error wrapping [ToolError] to tell the model what went
// wrong; any other error shows the model only a generic failure, and
// [Permanent] stops the run.
//
// If the arguments do not decode or validate, the model receives the
// validation messages as an error result and may try again; fn is not called.
// Availability is checked before decoding; denial returns ErrToolUnavailable,
// while a policy error stops the run without exposing its message to the model.
// This capability check does not replace resource authorization inside fn.
//
// Construction callbacks run once, synchronously in order, even for invalid
// positional inputs. Nil entries and callback panics propagate. Positional
// validation and schema rendering precede callbacks; the first error wins.
// Rendered schema bytes and draft settings are detached, but schemas, handlers,
// policies and their captures remain shared. There is no output schema.
//
//	lookupOrder := llm.NewTool("lookup-order", "Find an order by number.", orderArgs,
//		func(ctx context.Context, in OrderArgs) (Order, error) {
//			return orders.Find(ctx, in.Number)
//		})
//	if err := lookupOrder.Err(); err != nil {
//		return err
//	}
func NewTool[In, Out any](
	toolName, description string,
	schema validate.Schema[In],
	fn func(context.Context, In) (Out, error),
	configure ...ToolBuilderFunc,
) Tool {
	tool := newTool(toolName, description, schema, fn)
	action := ToolBuilder{err: tool.err}

	for _, configure := range configure {
		configure(&action)
	}

	tool.err = action.err
	tool.available = action.available

	// Capture the published value, not the mutable construction draft. The
	// underlying executor remains private behind the availability guard.
	execute := tool.call
	tool.call = func(ctx context.Context, arguments []byte) (string, error) {
		offered, err := tool.Available(ctx)

		if err != nil {
			if errors.Is(err, ErrToolAvailability) {
				return "", Permanent(err)
			}

			return "", err
		}

		if !offered {
			return "", ErrToolUnavailable
		}

		return execute(ctx, arguments)
	}

	return tool
}

// newTool validates positional inputs and renders their schema before any
// construction callbacks. Invalid names, missing schemas, missing handlers and
// rendering failures retain their historical precedence and timing.
func newTool[In, Out any](
	toolName, description string,
	schema validate.Schema[In],
	fn func(context.Context, In) (Out, error),
) Tool {
	tool := Tool{
		name:        toolName,
		description: description,
	}

	if err := name.Identifier(toolName); err != nil {
		tool.err = fmt.Errorf("%w: tool %q: %w", ErrInvalidName, toolName, err)

		return tool
	}

	switch {
	case schema == nil:
		tool.err = fmt.Errorf("%w: tool %q needs a schema", ErrInvalidRequest, toolName)

		return tool
	case fn == nil:
		tool.err = fmt.Errorf("%w: tool %q needs a function", ErrInvalidRequest, toolName)

		return tool
	}

	doc, err := schema.JSONSchema()

	if err != nil {
		tool.err = fmt.Errorf("%w: tool %q: %w", ErrInvalidRequest, toolName, err)

		return tool
	}

	tool.schema = slices.Clone(doc)
	tool.call = engine.Executor(toolName, schema, fn, ToolError)

	return tool
}

// Name returns the tool's name.
// This stable identity is shared by agent lookup, advertised specs, and telemetry.
//
//	lookup := llm.NewTool("lookup-order", "Find an order.", orderArgs, findOrder)
//	fmt.Println(lookup.Name()) // lookup-order
func (tool Tool) Name() string {
	return tool.name
}

// Description returns the text that tells the model when to use the tool.
// It is explanatory prompt content, not an authorization rule.
//
//	lookup := llm.NewTool("lookup-order", "Find an order.", orderArgs, findOrder)
//	fmt.Println(lookup.Description())
func (tool Tool) Description() string {
	return tool.description
}

// Err returns the first invalid input given to [NewTool], or nil.
// The zero Tool is invalid. Available and agent construction reject invalid
// definitions before evaluating policy, decoding arguments, or invoking handlers.
//
//	lookup := llm.NewTool("lookup-order", "Find an order.", orderArgs, findOrder)
//	if err := lookup.Err(); err != nil {
//		return err
//	}
func (tool Tool) Err() error {
	if tool.err == nil && tool.call == nil {
		return fmt.Errorf("%w: tool was not built with NewTool", ErrInvalidRequest)
	}

	return tool.err
}

// toolError carries text meant for the model. Create it with [ToolError].
type toolError struct {
	// message is the text the model sees.
	message string
}

// Error returns the text the model sees.
// ToolError is an explicit publication decision: its message must not contain
// backend diagnostics, secrets, or unauthorized resource information.
//
//	failure := llm.ToolError("no order with that number")
//	fmt.Println(failure.Error())
func (failure toolError) Error() string {
	return failure.message
}

// ToolError returns an error whose message is safe to show to the model. A
// tool function that returns it, or an error wrapping it, tells the model
// message instead of the generic "the tool failed", so the model can correct
// itself. Keep internal details such as queries, hosts and paths out of
// message.
//
//	return Order{}, fmt.Errorf("find %s: %w", number, llm.ToolError("no order with that number"))
func ToolError(message string) error {
	return toolError{message: message}
}

// modelText returns what the model sees of a tool error: the message of the
// first [ToolError] in the chain, or a generic failure text.
func modelText(err error) string {
	if errors.Is(err, ErrToolAvailability) {
		return genericToolFailure
	}

	if errors.Is(err, ErrToolUnavailable) {
		return "tool unavailable"
	}

	var visible toolError

	if errors.As(err, &visible) {
		return visible.message
	}

	return genericToolFailure
}

// permanentError makes a tool failure stop the run and matches
// [ErrPermanent]. Create it with [Permanent].
type permanentError struct {
	// cause is the tool's error.
	cause error
}

// Error returns the cause's message.
// It is diagnostic text for the caller, not permission to forward the cause to
// a model or event subscriber. Permanent failures stop the agent turn.
//
//	failure := llm.Permanent(errors.New("credential revoked"))
//	fmt.Println(failure.Error()) // credential revoked
func (failure permanentError) Error() string {
	return failure.cause.Error()
}

// Unwrap returns the tool's error.
// Caller-side recovery can still match the original error; failure events
// publish a bounded classification rather than retaining this cause.
//
//	cause := errors.New("credential revoked")
//	fmt.Println(errors.Unwrap(llm.Permanent(cause)) == cause) // true
func (failure permanentError) Unwrap() error {
	return failure.cause
}

// Is reports whether target is [ErrPermanent].
// Unwrap separately preserves cause matching, so callers need not parse a
// diagnostic message to distinguish fatal policy from correctable tool input.
//
//	failure := llm.Permanent(errors.New("credential revoked"))
//	fmt.Println(errors.Is(failure, llm.ErrPermanent)) // true
func (failure permanentError) Is(target error) bool {
	return target == ErrPermanent
}

// Permanent marks err as a failure that stops the run. A tool function that
// returns it cancels the context of the sibling calls still running in the
// same turn, and [Model.Run] returns err, still matching [ErrPermanent] under
// [errors.Is] and unwrapping to err. The model is not told; use it for
// failures retrying cannot fix, such as a revoked credential. err must not
// be nil.
//
//	return Order{}, llm.Permanent(fmt.Errorf("orders database unavailable: %w", err))
func Permanent(err error) error {
	return permanentError{cause: err}
}

// ToolBuilder configures capability policy during NewTool construction.
// It is a mutable draft, not an executable tool. Retaining it cannot change a
// published Tool; function captures themselves remain shared.
//
// Example:
//
//	tool := llm.NewTool("lookup-order", "Find an order.", orderArgs, lookup,
//		func(action *llm.ToolBuilder) {
//			action.Available(func(ctx context.Context) (bool, error) {
//				return permissions.CanLookupOrders(ctx)
//			})
//		})
//	if err := tool.Err(); err != nil {
//		return err
//	}
type ToolBuilder struct {
	// available is the most recently supplied policy, shared without caching.
	available func(context.Context) (bool, error)

	// err retains the first positional or policy declaration error.
	err error
}

// Available replaces the capability policy and returns the same draft.
// An explicit nil policy records sticky ErrInvalidRequest; a later replacement
// does not clear it. Omission allows calls by default. Policies run at discovery
// and again before execution, after acquiring the run's concurrency slot, and
// must support concurrent use with request-specific contexts. A false result is
// safe denial; an error aborts the run without reporting its text to the model.
// Resource authorization still belongs in the typed handler.
//
// Example:
//
//	tool := llm.NewTool("lookup-order", "Find an order.", orderArgs, lookup,
//		func(action *llm.ToolBuilder) {
//			action.Available(func(ctx context.Context) (bool, error) {
//				return permissions.CanLookupOrders(ctx)
//			})
//		})
//	if err := tool.Err(); err != nil {
//		return err
//	}
func (action *ToolBuilder) Available(policy func(context.Context) (bool, error)) *ToolBuilder {
	action.available = policy

	if policy == nil && action.err == nil {
		action.err = fmt.Errorf("%w: tool availability policy cannot be nil", ErrInvalidRequest)
	}

	return action
}

// ToolBuilderFunc configures one fresh ToolBuilder during NewTool construction.
// Callbacks run synchronously in order, including on invalid declarations; nil
// entries panic. An expanded nil or empty slice omits configuration.
//
// Example:
//
//	var configure llm.ToolBuilderFunc = func(action *llm.ToolBuilder) {
//		action.Available(func(context.Context) (bool, error) {
//			return true, nil
//		})
//	}
//	tool := llm.NewTool("lookup-order", "Find an order.", orderArgs, lookup, configure)
//	if err := tool.Err(); err != nil {
//		return err
//	}
type ToolBuilderFunc = func(*ToolBuilder)
