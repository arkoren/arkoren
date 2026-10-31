package mcp_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/mcp"
)

func TestFailureDetailsDetached(t *testing.T) {
	t.Parallel()

	details := jsontext.Value(`{"retry":true}`)

	err := mcp.ToolFailure("unavailable", "capture disabled", details)
	details[0] = '['
	info, ok := mcp.ToolFailureInfo(err)

	if !ok || string(info.Details) != `{"retry":true}` {
		t.Fatalf("failure = %+v, %v", info, ok)
	}

	info.Details[0] = '['
	info, _ = mcp.ToolFailureInfo(err)

	if string(info.Details) != `{"retry":true}` {
		t.Fatalf("failure report mutated marker: %+v", info)
	}
}

// ExampleToolFailureInfo obtains approved structured diagnostics from a failed
// public call. The error interface contains only the approved message.
func ExampleToolFailureInfo() {
	cause := mcp.ToolFailure("unavailable", "capture is disabled", jsontext.Value(`{"retry":false}`))
	tool := mcp.NewTool("capture", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, fmt.Errorf("private capture context: %w", cause)
	})

	_, err := tool.Call(context.Background(), nil)
	failure, ok := mcp.ToolFailureInfo(err)

	if !ok {
		fmt.Println(err)

		return
	}

	fmt.Println(failure.Code, failure.Message)
	fmt.Println(string(failure.Details))
	fmt.Println(cause.Error())
	// Output:
	// unavailable capture is disabled
	// {"retry":false}
	// capture is disabled
}

// TestToolFailureRejectsEmptyCode preserves an ordinary local error rather than
// approving malformed structured diagnostics for a protocol adapter to expose.
func TestToolFailureRejectsEmptyCode(t *testing.T) {
	t.Parallel()

	cause := mcp.ToolFailure("", "private message", nil)
	tool := mcp.NewTool("failure", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
		return mcp.NoInput{}, cause
	})

	_, err := tool.Call(t.Context(), nil)

	if err == nil || !errors.Is(err, cause) {
		t.Fatalf("invalid failure cause lost: %v", err)
	}

	if _, ok := mcp.ToolFailureInfo(err); ok {
		t.Fatal("empty code approved structured diagnostics")
	}

	if _, ok := mcp.ToolErrorMessage(err); ok {
		t.Fatal("empty code approved text diagnostics")
	}
}

// TestToolFailureRejectsInvalidDetails keeps both nonobject and malformed JSON
// unmarked. Each public call retains the exact handler cause for local use.
func TestToolFailureRejectsInvalidDetails(t *testing.T) {
	t.Parallel()

	for _, details := range []jsontext.Value{jsontext.Value(`[]`), jsontext.Value(`{`)} {
		cause := mcp.ToolFailure("unavailable", "private message", details)
		tool := mcp.NewTool("failure", func(context.Context, mcp.NoInput) (mcp.NoInput, error) {
			return mcp.NoInput{}, cause
		})

		_, err := tool.Call(t.Context(), nil)

		if err == nil || !errors.Is(err, cause) {
			t.Fatalf("invalid failure cause lost: %v", err)
		}

		if _, ok := mcp.ToolFailureInfo(err); ok {
			t.Fatal("invalid details approved structured diagnostics")
		}

		if _, ok := mcp.ToolErrorMessage(err); ok {
			t.Fatal("invalid details approved text diagnostics")
		}
	}
}
