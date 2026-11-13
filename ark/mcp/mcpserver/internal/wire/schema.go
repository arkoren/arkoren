// Package wire converts exact managed-tool declarations and results to SDK wire
// records. Execution, discovery authorization, and transport remain adapter-owned.
package wire

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

// Prepare validates exact schemas and snapshots CLI metadata before any native
// registry mutation. The SDK compatibility probe never replaces schema bytes.
func Prepare(info mcp.ToolInfo) (validate.EncodedSchema, sdkmcp.Meta, error) {
	output, err := compileOutput(info.OutputSchema)

	if err != nil {
		return validate.EncodedSchema{}, nil, fmt.Errorf("mcpserver: tool %q output schema: %w", info.Name, err)
	}

	if err := checkSDKSchema(info.InputSchema); err != nil {
		return validate.EncodedSchema{}, nil, fmt.Errorf("mcpserver: tool %q input schema: %w", info.Name, err)
	}

	if err := checkSDKSchema(info.OutputSchema); err != nil {
		return validate.EncodedSchema{}, nil, fmt.Errorf("mcpserver: tool %q output schema: %w", info.Name, err)
	}

	meta, err := cliMetadata(info.CLI)

	if err != nil {
		return validate.EncodedSchema{}, nil, fmt.Errorf("mcpserver: tool %q CLI metadata: %w", info.Name, err)
	}

	return output, meta, nil
}

// Output validates emitted bytes without inference or normalization and builds
// the SDK success envelope. Operational failures are returned without redaction
// so the execution owner can log the cause before selecting a safe failure.
func Output(ctx context.Context, schema validate.EncodedSchema, document jsontext.Value) (*sdkmcp.CallToolResult, error) {
	if err := validateOutput(ctx, schema, document); err != nil {
		return nil, err
	}

	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(document)}},
		StructuredContent: document,
	}, nil
}

// checkSDKSchema rejects schemas the pinned SDK cannot register. Its non-generic
// AddTool probes both schemas through an any decoder and panics on overflow.
// This compatibility probe discards the decoded value: publication and strict
// validation must continue to use the original, exact schema bytes.
func checkSDKSchema(document jsontext.Value) error {
	var probe any

	if err := json.Unmarshal(document, &probe); err != nil {
		return fmt.Errorf("schema exceeds native SDK decoding support: %w", err)
	}

	return nil
}

// compileOutput compiles the published schema once without network I/O. Strict
// validation retains exact numeric literals and rejects unsupported assertions.
func compileOutput(document jsontext.Value) (validate.EncodedSchema, error) {
	return validate.FromJSONSchemaStrict(document)
}

// validateOutput checks emitted bytes without defaults, normalization, or a
// floating-point instance decoder. Only validate.Errors denotes a mismatch;
// cancellation, malformed JSON, and resource refusals stay operational errors.
func validateOutput(ctx context.Context, schema validate.EncodedSchema, document jsontext.Value) error {
	return schema.Validate(ctx, document)
}
