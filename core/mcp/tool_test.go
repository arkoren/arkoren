package mcp_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/collection"
	"arkoren.dev/foundation/validate"
)

// collectionEnvelope exercises native collection fields through typed tool I/O.
type collectionEnvelope struct {
	// Names retains the decoded sequence's order.
	Names collection.Slice[string] `json:"names"`
	// Counts retains typed integer map values.
	Counts collection.Map[string, int] `json:"counts"`
}

// TestToolRoundTripsCollectionFields verifies native collection DTOs work with
// existing generic input decoding and output encoding without adapter APIs.
func TestToolRoundTripsCollectionFields(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("collections.echo", func(_ context.Context, input collectionEnvelope) (collectionEnvelope, error) {
		input.Names = input.Names.Map(func(_ int, name string) string {
			return "hello " + name
		})

		return input, nil
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	output, err := tool.Call(t.Context(), jsontext.Value(`{"names":["Ada","Cy"],"counts":{"ready":2}}`))

	if err != nil {
		t.Fatal(err)
	}

	var result collectionEnvelope

	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}

	if len(result.Names) != 2 || result.Names[0] != "hello Ada" || result.Names[1] != "hello Cy" || result.Counts["ready"] != 2 {
		t.Fatalf("round-trip result = %+v", result)
	}
}

// ExampleNewTool demonstrates a transport-free call with inferred object schemas.
// Real adapters additionally validate the emitted result against OutputSchema.
func ExampleNewTool() {
	type Document struct {
		// ID is the document identifier echoed by this example.
		ID string `json:"id"`
	}
	tool := mcp.NewTool("documents.echo", func(_ context.Context, input Document) (Document, error) {
		return input, nil
	}, func(action *mcp.ToolBuilder[Document, Document]) {
		action.Description("Echo a document identifier").ReadOnly(true).OpenWorld(false)
	})

	result, err := tool.Call(context.Background(), jsontext.Value(`{"id":"doc-42"}`))

	if err != nil {
		panic(err)
	}

	fmt.Println(string(result))
	// Output: {"id":"doc-42"}
}

// ExampleToolBuilderFunc uses the public callback alias directly at publication.
// The resulting tool owns the captured settings, not the construction draft.
func ExampleToolBuilderFunc() {
	policy := mcp.ToolBuilderFunc[mcp.NoInput, mcp.NoInput](
		func(action *mcp.ToolBuilder[mcp.NoInput, mcp.NoInput]) {
			action.ReadOnly(true)
		},
	)

	tool := mcp.NewTool("status", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, nil
	}, policy)

	if err := tool.Err(); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(tool.Name(), tool.Describe().Annotations.ReadOnly)
	// Output: status true
}

// request is a typed input/output object shared by the decoding scenarios.
type request struct {
	// Name is intentionally optional so omitted-argument behavior is visible.
	Name string `json:"name"`
}

// echo preserves a decoded argument for tests that isolate declaration policy.
func echo(_ context.Context, input request) (request, error) {
	return input, nil
}

func TestToolInfersObjectSchemas(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("Documents.lookup-1", echo)

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	info := tool.Describe()

	if info.Name != tool.Name() || info.Name != "Documents.lookup-1" {
		t.Fatal(info)
	}

	if !strings.Contains(string(info.InputSchema), `"type":"object"`) {
		t.Fatal(string(info.InputSchema))
	}

	if string(info.OutputSchema) != string(info.InputSchema) {
		t.Fatal(info)
	}

	if info.Annotations != (mcp.Annotations{Destructive: true, OpenWorld: true}) {
		t.Fatal(info.Annotations)
	}
}

func TestToolCallbacksPublishDetachedState(t *testing.T) {
	t.Parallel()

	var retained *mcp.ToolBuilder[request, request]
	order := ""
	tool := mcp.NewTool("echo", echo,
		func(action *mcp.ToolBuilder[request, request]) {
			retained = action
			order += "a"
			action.Description("first").ReadOnly(true).Destructive(false)
		},
		func(action *mcp.ToolBuilder[request, request]) {
			order += "b"
			action.Description("second").Idempotent(true).OpenWorld(false)
		})
	retained.Description("changed").ReadOnly(false).Input(nil).Output(nil)

	if order != "ab" {
		t.Fatal(order)
	}

	info := tool.Describe()

	if info.Description != "second" || info.Annotations != (mcp.Annotations{ReadOnly: true, Idempotent: true}) {
		t.Fatal(info)
	}

	clear(info.InputSchema)
	clear(info.OutputSchema)

	if !jsontext.Value(tool.Describe().InputSchema).IsValid() {
		t.Fatal("report mutated input schema")
	}

	if !jsontext.Value(tool.Describe().OutputSchema).IsValid() {
		t.Fatal("report mutated output schema")
	}

	if _, err := tool.Call(t.Context(), jsontext.Value(`{"name":"ok"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestToolRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "bad name", "bad/name", "é", strings.Repeat("x", 129)} {
		if err := mcp.NewTool(name, echo).Err(); !errors.Is(err, mcp.ErrInvalidTool) {
			t.Fatalf("name %q: %v", name, err)
		}
	}
}

func TestToolAcceptsMaximumName(t *testing.T) {
	t.Parallel()

	if err := mcp.NewTool(strings.Repeat("x", 128), echo).Err(); err != nil {
		t.Fatal(err)
	}
}

func TestZeroToolInvalid(t *testing.T) {
	t.Parallel()

	var tool mcp.Tool

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatal(tool.Err())
	}

	if _, err := tool.Call(t.Context(), nil); !errors.Is(err, mcp.ErrInvalidTool) {
		t.Fatal(err)
	}
}

func TestToolNilHandlerStillRunsCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	tool := mcp.NewTool("echo", (func(context.Context, request) (request, error))(nil),
		func(action *mcp.ToolBuilder[request, request]) {
			calls++
			action.Description("configured")
		})

	if calls != 1 || !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatalf("callbacks %d, error %v", calls, tool.Err())
	}
}

func TestToolNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	mcp.NewTool("echo", echo, nil)
}

func TestToolRejectsScalarInputSchema(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("scalar", func(_ context.Context, input string) (request, error) {
		return request{Name: input}, nil
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatal(tool.Err())
	}
}

func TestToolRejectsScalarOutputSchema(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("scalar", func(_ context.Context, _ mcp.NoInput) (string, error) {
		return "unused", nil
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatal(tool.Err())
	}
}

func TestToolKeepsFirstSchemaError(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Input(nil).Input(validate.Infer[request]()).Output(nil)
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) || !strings.Contains(tool.Err().Error(), "input schema") {
		t.Fatal(tool.Err())
	}
}

func TestToolRejectsTypedNilSchema(t *testing.T) {
	t.Parallel()

	var schema *validate.StructSchema[request]
	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Input(schema)
	})

	if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
		t.Fatal(tool.Err())
	}
}

func TestToolRejectsNonobjectAndMalformedArguments(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("echo", echo)

	for _, input := range []string{"null", "[]", `"text"`, "1", "", "{", "{} {}", `{"name":"a","name":"b"}`} {
		if _, err := tool.Call(t.Context(), jsontext.Value(input)); !errors.Is(err, mcp.ErrInvalidInput) {
			t.Fatalf("input %q: %v", input, err)
		}
	}
}

func TestToolRejectsUnknownStructMembers(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("echo", echo)

	_, err := tool.Call(t.Context(), jsontext.Value(`{"unknown":true}`))
	var cause *json.SemanticError

	if !errors.Is(err, mcp.ErrInvalidInput) || !errors.As(err, &cause) {
		t.Fatal(err)
	}
}

func TestToolNilArgumentsMeanEmptyObject(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("empty", func(_ context.Context, _ mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, nil
	})

	output, err := tool.Call(t.Context(), nil)

	if err != nil || string(output) != "{}" {
		t.Fatalf("%s: %v", output, err)
	}
}

func TestToolParsesInputOnceAndDoesNotParseOutput(t *testing.T) {
	t.Parallel()

	inputCalls, outputCalls, handlerCalls, policyCalls := 0, 0, 0, 0
	input := validate.Struct[request](func(object *validate.StructBuilder[request]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Normalize(func(value string) string {
				inputCalls++

				return value + "!"
			})
		})
	})

	output := validate.Struct[request](func(object *validate.StructBuilder[request]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Normalize(func(value string) string {
				outputCalls++

				return "changed"
			})
		})
	})

	tool := mcp.NewTool("echo", func(_ context.Context, input request) (request, error) {
		handlerCalls++

		return input, nil
	}, func(action *mcp.ToolBuilder[request, request]) {
		action.Input(input).Output(output)
		action.Available(func(context.Context) (bool, error) {
			policyCalls++

			return true, nil
		})
	})

	if inputCalls+outputCalls+handlerCalls+policyCalls != 0 {
		t.Fatal("construction executed application code")
	}

	value, err := tool.Call(t.Context(), jsontext.Value(`{"name":"hello"}`))

	if err != nil {
		t.Fatal(err)
	}

	if string(value) != `{"name":"hello!"}` || inputCalls != 1 || outputCalls != 0 || handlerCalls != 1 {
		t.Fatalf("%s: input %d output %d handler %d", value, inputCalls, outputCalls, handlerCalls)
	}

	if policyCalls != 1 {
		t.Fatalf("policy evaluated %d times", policyCalls)
	}
}

func TestToolInputValidationPreservesCause(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Input(validate.Struct[request](func(object *validate.StructBuilder[request]) {
			object.Field("Name", func(name *validate.StringBuilder[string]) {
				name.Min(3)
			})
		}))
	})

	_, err := tool.Call(t.Context(), jsontext.Value(`{"name":"x"}`))
	var violations validate.Errors

	if !errors.Is(err, mcp.ErrInvalidInput) || !errors.As(err, &violations) {
		t.Fatal(err)
	}
}

// TestToolInputSchemaOperationalFailurePreservesCause distinguishes failed
// schema execution from a caller's invalid input without losing diagnostics.
func TestToolInputSchemaOperationalFailurePreservesCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("private schema dependency failed")
	tool := mcp.NewTool("echo", echo, func(action *mcp.ToolBuilder[request, request]) {
		action.Input(validate.Struct(func(object *validate.StructBuilder[request]) {
			object.Check(func(context.Context, request) error {
				return cause
			})
		}))
	})

	_, err := tool.Call(t.Context(), jsontext.Value(`{"name":"valid"}`))

	if !errors.Is(err, cause) || errors.Is(err, mcp.ErrInvalidInput) {
		t.Fatalf("operational schema failure = %v", err)
	}

	if !errors.Is(err, mcp.ErrInputSchema) {
		t.Fatalf("schema failure classification lost: %v", err)
	}
}

func TestToolHandlerErrorPreservesIdentity(t *testing.T) {
	t.Parallel()

	cause := errors.New("handler failed")
	tool := mcp.NewTool("failure", func(_ context.Context, _ request) (request, error) {
		return request{}, cause
	})

	_, err := tool.Call(t.Context(), nil)

	if !errors.Is(err, cause) {
		t.Fatal(err)
	}
}

func TestToolRejectsNullOutput(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("null", func(_ context.Context, _ mcp.NoInput) (*request, error) {
		return nil, nil
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	if _, err := tool.Call(t.Context(), nil); !errors.Is(err, mcp.ErrInvalidOutput) {
		t.Fatal(err)
	}
}

func TestToolOutputEncodingPreservesCause(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("number", func(_ context.Context, _ mcp.NoInput) (map[string]float64, error) {
		return map[string]float64{"number": math.NaN()}, nil
	})

	_, err := tool.Call(t.Context(), nil)
	var cause *json.SemanticError

	if !errors.Is(err, mcp.ErrInvalidOutput) || !errors.As(err, &cause) {
		t.Fatal(err)
	}
}

func TestToolHandlerPanicPropagates(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("panic", func(_ context.Context, _ mcp.NoInput) (mcp.NoInput, error) {
		panic("application panic")
	})
	defer func() {
		if recovered := recover(); recovered != "application panic" {
			t.Fatalf("unexpected panic: %v", recovered)
		}
	}()
	_, _ = tool.Call(t.Context(), nil)
}

func TestToolCanceledBeforeHandler(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tool := mcp.NewTool("echo", func(_ context.Context, input request) (request, error) {
		t.Fatal("handler executed")

		return input, nil
	})

	if _, err := tool.Call(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestToolCancellationDuringParsePreventsHandler(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	tool := mcp.NewTool("echo", func(_ context.Context, input request) (request, error) {
		t.Fatal("handler executed")

		return input, nil
	}, func(action *mcp.ToolBuilder[request, request]) {
		action.Input(validate.Struct[request](func(object *validate.StructBuilder[request]) {
			object.Check(func(_ context.Context, _ request) error {
				cancel()

				return nil
			})
		}))
	})

	if _, err := tool.Call(ctx, jsontext.Value(`{"name":"active"}`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestToolConcurrentCalls(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	tool := mcp.NewTool("echo", func(_ context.Context, input request) (request, error) {
		calls.Add(1)

		return input, nil
	})

	var group sync.WaitGroup

	for range 20 {
		group.Go(func() {
			if _, err := tool.Call(t.Context(), nil); err != nil {
				t.Error(err)
			}
		})
	}

	group.Wait()

	if calls.Load() != 20 {
		t.Fatal(calls.Load())
	}
}
