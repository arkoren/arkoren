// Package mcptest calls owner diagnostic tools directly, without a protocol
// adapter. It keeps transport testing separate from capture and query behavior.
//
// Example: result := mcptest.Call[map[string]any](t, tool, `{}`)
package mcptest

import (
	"encoding/json/v2"
	"testing"

	"arkoren.dev/core/mcp"
)

// Find locates one tool by its full managed name and checks its declaration.
// Missing or invalid declarations fail the test before any handler is invoked.
// Example: tool := mcptest.Find(t, tools, "app.info")
func Find(t *testing.T, tools []mcp.Tool, name string) mcp.Tool {
	t.Helper()

	for _, tool := range tools {
		if tool.Name() == name {
			if err := tool.Err(); err != nil {
				t.Fatalf("tool %s: %v", name, err)
			}

			return tool
		}
	}

	t.Fatalf("missing tool %s", name)

	return mcp.Tool{}
}

// Call invokes a tool with strict JSON arguments and decodes its object result.
// Tool and decode failures fail the test with the managed name as context.
// Example: result := mcptest.Call[map[string]any](t, mcptest.Find(t, tools, "app.info"), `{}`)
func Call[T any](t *testing.T, tool mcp.Tool, arguments string) T {
	t.Helper()

	value, err := tool.Call(t.Context(), []byte(arguments))

	if err != nil {
		t.Fatalf("call %s: %v", tool.Name(), err)
	}

	var result T

	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatalf("decode %s: %v (%s)", tool.Name(), err, value)
	}

	return result
}
