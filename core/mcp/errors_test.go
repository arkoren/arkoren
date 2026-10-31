package mcp_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

func TestToolErrorMessageOmitsWrapper(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("secret credential: %w", mcp.ToolError("not available"))
	message, ok := mcp.ToolErrorMessage(err)

	if !ok || message != "not available" {
		t.Fatalf("%q, %v", message, ok)
	}

	if _, ok := mcp.ToolErrorMessage(errors.New("secret")); ok {
		t.Fatal("unmarked error was public")
	}

	if _, ok := mcp.ToolErrorMessage(nil); ok {
		t.Fatal("nil error was public")
	}

	if message, ok := mcp.ToolErrorMessage(mcp.ToolError("")); !ok || message != "" {
		t.Fatal("empty approved message lost")
	}
}

// ExampleErrInputSchema shows how a caller distinguishes an unavailable schema
// dependency from invalid arguments without losing the original local cause.
func ExampleErrInputSchema() {
	type Input struct {
		// ID makes the input present so the schema's object Check executes.
		ID string `json:"id"`
	}
	cause := errors.New("schema dependency unavailable")
	tool := mcp.NewTool("lookup", func(_ context.Context, input Input) (Input, error) {
		return input, nil
	}, func(action *mcp.ToolBuilder[Input, Input]) {
		action.Input(validate.Struct(func(object *validate.StructBuilder[Input]) {
			object.Check(func(context.Context, Input) error {
				return cause
			})
		}))
	})

	_, err := tool.Call(context.Background(), jsontext.Value(`{"id":"doc-42"}`))

	if err != nil {
		fmt.Println(errors.Is(err, mcp.ErrInputSchema), errors.Is(err, cause))
		fmt.Println(errors.Is(err, mcp.ErrInvalidInput))
	}
	// Output:
	// true true
	// false
}

// ExampleToolError exercises the error interface and approved-message extraction
// through Call. Private wrapping remains available locally, outside the marker.
func ExampleToolError() {
	cause := mcp.ToolError("document is not available")
	tool := mcp.NewTool("lookup", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, fmt.Errorf("private lookup context: %w", cause)
	})

	_, err := tool.Call(context.Background(), nil)
	message, ok := mcp.ToolErrorMessage(err)
	fmt.Println(message, ok)
	fmt.Println(errors.Is(err, cause))
	fmt.Println(cause.Error())
	// Output:
	// document is not available true
	// true
	// document is not available
}
