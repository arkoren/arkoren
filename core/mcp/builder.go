package mcp

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"arkoren.dev/foundation/validate"
)

// ToolBuilder holds construction-only edits for NewTool. Retaining it does
// not grant mutation of a published tool. Schemas remain shared immutable
// dependencies; functions and their captures stay application-owned and must
// support concurrent use. Schema bytes are copied during publication.
//
// Example:
//
//	// status is the application's func(context.Context, mcp.NoInput) (Status, error).
//	tool := mcp.NewTool("status", status, func(action *mcp.ToolBuilder[mcp.NoInput, Status]) {
//		action.Description("Read current status").ReadOnly(true)
//	})
//	if err := tool.Err(); err != nil {
//		return err
//	}
type ToolBuilder[In, Out any] struct {
	// info accumulates metadata before its independent publication.
	info ToolInfo
	// input is the typed parser captured by the published invocation closure.
	input validate.Schema[In]
	// available observes request capability without parsing arguments or acting.
	available func(context.Context) (bool, error)
	// err retains the earliest invalid setting, even after replacements.
	err error
}

// ToolBuilderFunc configures a single construction draft synchronously.
// NewTool invokes callbacks once in order; nil entries panic as caller misuse.
//
// Example:
//
//	policy := mcp.ToolBuilderFunc[mcp.NoInput, mcp.NoInput](
//		func(action *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
//			action.ReadOnly(true)
//		},
//	)
//	tool := mcp.NewTool("status", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
//		return mcp.NoInput{}, nil
//	}, policy)
//	if err := tool.Err(); err != nil {
//		return err
//	}
//	fmt.Println(tool.Name())
type ToolBuilderFunc[In, Out any] = func(*ToolBuilder[In, Out])

// NewTool publishes an immutable tool without invoking fn or evaluating its
// availability policy. It performs no protocol I/O of its own. Construction
// callbacks and JSON methods used by schema export or CLI example decoding are
// application code and must be safe to run during construction.
// Names contain 1–128 ASCII letters, digits, underscores, dots or hyphens.
// Unspecified schemas are inferred; both must describe JSON objects. Invalid
// declarations retain their first error for Err and Call. Every callback runs
// once in order on one draft, including invalid declarations; nil callbacks
// and callback panics propagate. Nil fn is a declaration error, not a panic.
//
// Example:
//
//	tool := mcp.NewTool("status", func(ctx context.Context, in mcp.NoInput) (struct{}, error) {
//		return struct{}{}, nil
//	}, func(action *mcp.ToolBuilder[mcp.NoInput, struct{}]) {
//		action.Description("Read status").ReadOnly(true)
//	})
//	if err := tool.Err(); err != nil {
//		panic(err)
//	}
func NewTool[In, Out any](
	name string,
	fn func(context.Context, In) (Out, error),
	configure ...ToolBuilderFunc[In, Out],
) Tool {
	action := &ToolBuilder[In, Out]{info: ToolInfo{
		Name: name, Annotations: Annotations{Destructive: true, OpenWorld: true},
	}}

	if !toolName.MatchString(name) {
		action.err = errors.New("name must contain 1–128 ASCII letters, digits, _, . or -")
	}

	if fn == nil {
		action.err = cmp.Or(action.err, errors.New("handler is nil"))
	}

	for _, configure := range configure {
		configure(action)
	}

	return action.publish(fn)
}

// publish resolves omitted schemas after configuration, then captures values
// rather than the draft pointer. A retained draft cannot redirect later calls.
func (action *ToolBuilder[In, Out]) publish(fn func(context.Context, In) (Out, error)) Tool {
	if action.input == nil {
		action.Input(validate.Infer[In]())
	}

	if action.info.OutputSchema == nil {
		action.Output(validate.Infer[Out]())
	}

	action.err = cmp.Or(action.err, action.validateCLI())
	tool := Tool{info: action.info, err: action.err, available: action.available}
	tool.info.InputSchema = slices.Clone(tool.info.InputSchema)
	tool.info.OutputSchema = slices.Clone(tool.info.OutputSchema)
	tool.info.CLI = cloneCLI(tool.info.CLI)
	input := action.input
	tool.call = func(ctx context.Context, arguments jsontext.Value) (jsontext.Value, error) {
		value, err := decodeInput(ctx, arguments, input)

		if err != nil {
			return nil, err
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		output, err := fn(ctx, value)

		if err != nil {
			return nil, err
		}

		return encodeOutput(output)
	}

	return tool
}

// decodeInput performs strict typed decoding before exactly one schema Parse.
// JSON errors and rule violations wrap ErrInvalidInput. Other schema failures
// wrap ErrInputSchema so adapters cannot disclose an operational Check error.
// Every failure preserves its original cause for local errors.Is/As inspection.
func decodeInput[In any](ctx context.Context, arguments jsontext.Value, schema validate.Schema[In]) (In, error) {
	var value In

	if err := json.Unmarshal(arguments, &value, json.RejectUnknownMembers(true)); err != nil {
		return value, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}

	value, err := schema.Parse(ctx, value)

	if err != nil {
		// violations marks only caller-correctable rules, not Check infrastructure.
		var violations validate.Errors

		if !errors.As(err, &violations) {
			return value, fmt.Errorf("%w: %w", ErrInputSchema, err)
		}

		return value, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}

	return value, nil
}

// objectSchema snapshots an object-root schema without executing its rules.
// Typed nil schema pointers are invalid, just like nil interfaces.
func objectSchema[T any](schema validate.Schema[T]) (jsontext.Value, error) {
	if schema == nil {
		return nil, errors.New("schema is nil")
	}

	value := reflect.ValueOf(schema)

	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil, errors.New("schema is nil")
	}

	data, err := schema.JSONSchema()

	if err != nil {
		return nil, err
	}

	var header struct {
		// Type limits v1 declarations to JSON objects rather than unions.
		Type string `json:"type"`
	}

	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}

	if header.Type != "object" {
		return nil, errors.New("schema must describe a JSON object")
	}

	return slices.Clone(data), nil
}

// Description replaces the human-readable description; empty is permitted.
// It returns the same construction draft and never edits a published Tool.
//
// Example: action.Description("Look up a document by its identifier")
func (action *ToolBuilder[In, Out]) Description(text string) *ToolBuilder[In, Out] {
	action.info.Description = text

	return action
}

// Input replaces the typed input parser and snapshots its object schema.
// A nil, invalid or nonobject schema retains a sticky construction error.
// Parse runs once per permitted call. Rule violations wrap ErrInvalidInput;
// operational Check failures wrap ErrInputSchema and must remain private.
//
// Example: action.Input(validate.Struct[Request]())
func (action *ToolBuilder[In, Out]) Input(schema validate.Schema[In]) *ToolBuilder[In, Out] {
	data, err := objectSchema(schema)

	if err != nil {
		action.err = cmp.Or(action.err, fmt.Errorf("input schema: %w", err))

		return action
	}

	action.input, action.info.InputSchema = schema, data

	return action
}

// Output replaces the published object schema without adding output parsing.
// Only JSON-expressible rules reach the adapter; normalizers and custom Check
// functions are not executed on output. Invalid settings retain a sticky error.
//
// Example: action.Output(validate.Struct[Result]())
func (action *ToolBuilder[In, Out]) Output(schema validate.Schema[Out]) *ToolBuilder[In, Out] {
	data, err := objectSchema(schema)

	if err != nil {
		action.err = cmp.Or(action.err, fmt.Errorf("output schema: %w", err))

		return action
	}

	action.info.OutputSchema = data

	return action
}

// ReadOnly sets the nonmutating hint, which defaults to false.
// Hints are descriptive and never enforce authorization or execution policy.
//
// Example: action.ReadOnly(true)
func (action *ToolBuilder[In, Out]) ReadOnly(value bool) *ToolBuilder[In, Out] {
	action.info.Annotations.ReadOnly = value

	return action
}

// Destructive sets the destructive-effects hint, which defaults to true.
// It is relevant when ReadOnly is false and does not trigger confirmation.
//
// Example: action.Destructive(false)
func (action *ToolBuilder[In, Out]) Destructive(value bool) *ToolBuilder[In, Out] {
	action.info.Annotations.Destructive = value

	return action
}

// Idempotent sets the repeatable-effects hint, which defaults to false.
// It does not cause retries or deduplication during Call.
//
// Example: action.Idempotent(true)
func (action *ToolBuilder[In, Out]) Idempotent(value bool) *ToolBuilder[In, Out] {
	action.info.Annotations.Idempotent = value

	return action
}

// OpenWorld sets the unbounded-external-interaction hint, defaulting to true.
// False describes a closed domain, not an enforced network restriction.
//
// Example: action.OpenWorld(false)
func (action *ToolBuilder[In, Out]) OpenWorld(value bool) *ToolBuilder[In, Out] {
	action.info.Annotations.OpenWorld = value

	return action
}
