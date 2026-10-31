package mcp

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
)

// NoInput describes an empty argument object, not JSON null.
// It is useful for tools that obtain all their data from dependencies.
//
// Example:
//
//	tool := mcp.NewTool("status", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
//		return mcp.NoInput{}, nil
//	})
//	result, err := tool.Call(context.Background(), nil)
//	if err != nil {
//		return err
//	}
//	fmt.Println(string(result)) // {}
type NoInput struct{}

// Annotations are detached protocol hints, not authorization guarantees.
// Their conservative defaults are false, true, false, true respectively.
//
// Example: hints := tool.Describe().Annotations; fmt.Println(hints.ReadOnly)
type Annotations struct {
	// ReadOnly reports that a tool does not mutate its environment.
	// Example: if tool.Describe().Annotations.ReadOnly { fmt.Println("read only") }
	ReadOnly bool
	// Destructive reports possible destructive updates, unless ReadOnly applies.
	// Example: if tool.Describe().Annotations.Destructive { fmt.Println("confirm") }
	Destructive bool
	// Idempotent reports repeatable effects for the same arguments.
	// Example: fmt.Println(tool.Describe().Annotations.Idempotent)
	Idempotent bool
	// OpenWorld reports possible interaction with an unbounded external world.
	// Example: fmt.Println(tool.Describe().Annotations.OpenWorld)
	OpenWorld bool
}

// ToolInfo is a detached report suitable for protocol registration.
// Editing its fields or schema bytes cannot change the published Tool.
//
// Example: info := tool.Describe(); fmt.Println(info.Name, string(info.InputSchema))
type ToolInfo struct {
	// Name is the case-sensitive protocol identifier.
	// Example: fmt.Println(tool.Describe().Name)
	Name string
	// Description explains the tool to a client; it may be empty.
	// Example: fmt.Println(tool.Describe().Description)
	Description string
	// InputSchema is an owned JSON Schema document with object root type.
	// Example: fmt.Println(string(tool.Describe().InputSchema))
	InputSchema jsontext.Value
	// OutputSchema describes the emitted object; adapters validate emitted bytes.
	// Example: fmt.Println(string(tool.Describe().OutputSchema))
	OutputSchema jsontext.Value
	// Annotations contains application-declared hints, not security policy.
	// Example: fmt.Println(tool.Describe().Annotations.ReadOnly)
	Annotations Annotations
	// CLI is optional command presentation, independent of the protocol name.
	// The returned pointer and its containers are detached on every Describe.
	// Example: if info := tool.Describe(); info.CLI != nil { fmt.Println(info.CLI.Group) }
	CLI *CLIInfo
}

// Tool is an immutable typed handler erased for heterogeneous registration.
// Its zero value is invalid. Closure state is shared, not deep-copied.
//
// Example:
//
//	tool := mcp.NewTool("status", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
//		return mcp.NoInput{}, nil
//	})
//	tools := []mcp.Tool{tool}
//	result, err := tools[0].Call(context.Background(), nil)
//	if err != nil {
//		return err
//	}
//	fmt.Println(string(result)) // {}
type Tool struct {
	// info owns the published metadata and schema bytes.
	info ToolInfo
	// err retains the first declaration failure, wrapped by Err.
	err error
	// call decodes, parses and invokes the captured typed declaration.
	call func(context.Context, jsontext.Value) (jsontext.Value, error)
	// available is a shared observational capability policy; nil permits use.
	available func(context.Context) (bool, error)
}

// toolName matches the MCP identifier grammar without normalization.
var toolName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// Name returns the exact declared identifier, even for an invalid declaration.
// It does not validate or normalize the name.
//
// Example: fmt.Println(tool.Name())
func (tool Tool) Name() string {
	return tool.info.Name
}

// Err reports the first declaration defect, or nil for a usable tool.
// A zero Tool fails with ErrInvalidTool; callbacks still run on invalid drafts.
//
// Example: if err := tool.Err(); err != nil { return err }
func (tool Tool) Err() error {
	if tool.err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTool, tool.err)
	}

	if tool.call == nil {
		return fmt.Errorf("%w: tool is uninitialized", ErrInvalidTool)
	}

	return nil
}

// Describe returns metadata with independent copies of both schema documents.
// Check Err before registering; invalid tools may have incomplete metadata.
//
// Example: info := tool.Describe(); fmt.Println(info.Description)
func (tool Tool) Describe() ToolInfo {
	info := tool.info
	info.InputSchema = slices.Clone(info.InputSchema)
	info.OutputSchema = slices.Clone(info.OutputSchema)
	info.CLI = cloneCLI(info.CLI)

	return info
}

// Call checks availability, decodes one object, parses input once, and invokes
// the handler once. A false policy returns ErrUnavailable before even decoding
// arguments; policy failures preserve ErrAvailability and their original cause.
// Nil arguments mean {}. Null, unknown struct members, and nonobjects fail
// with ErrInvalidInput. Canceled contexts prevent handler invocation. Output
// must encode as an object; output Parse is deliberately never called, so
// adapters must validate the emitted bytes against the published OutputSchema.
// Handler errors retain identity; panics propagate for adapter-level recovery.
// Operational input Check failures retain their cause under ErrInputSchema,
// not ErrInvalidInput; adapters must not disclose their error markers.
// Integer fields and jsontext.Value retain numeric precision; interface-held
// numbers follow encoding/json/v2's float64 decoding semantics. Use raw JSON
// fields instead of any when opaque numbers must remain exact.
//
// Example:
//
//	result, err := tool.Call(context.Background(), jsontext.Value(`{}`))
//	if err != nil {
//		return err
//	}
//	fmt.Println(string(result))
func (tool Tool) Call(ctx context.Context, arguments jsontext.Value) (jsontext.Value, error) {
	offered, err := tool.Available(ctx)

	if err != nil {
		return nil, err
	}

	if !offered {
		return nil, ErrUnavailable
	}

	if arguments == nil {
		arguments = jsontext.Value(`{}`)
	}

	if arguments.Kind() != '{' {
		return nil, fmt.Errorf("%w: arguments must be an object", ErrInvalidInput)
	}

	return tool.call(ctx, arguments)
}

// encodeOutput serializes the handler result without executing output rules.
// Even custom JSON marshalers must emit an object; encoding causes are retained.
func encodeOutput[Out any](output Out) (jsontext.Value, error) {
	data, err := json.Marshal(output)

	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}

	value := jsontext.Value(data)

	if value.Kind() != '{' {
		return nil, fmt.Errorf("%w: result must be an object", ErrInvalidOutput)
	}

	return value, nil
}
