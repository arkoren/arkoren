// Package mcp defines immutable, SDK-independent Model Context Protocol tools.
// It owns request-time capability availability, typed decoding and input
// validation; protocol adapters own transport, recovery, and emitted-output
// validation. Applications own authentication and resource authorization.
// Availability runs before decoding, not during construction, and does not
// replace resource checks inside handlers. Published tools can be shared across
// goroutines when policies, handlers and captured dependencies are concurrency-safe.
// Optional CLI presentation travels with ToolInfo, never in a second catalog.
// Example arguments are checked by typed decoding without executing input
// validation rules. Custom JSON unmarshaling methods can run during that
// construction-time check; this is not a guarantee that no user code executes.
// CLI roles describe diagnostic meaning, not command shortcuts or policy.
// ToolFailure explicitly approves structured diagnostics;
// ToolError approves text only. Adapters must keep policy failures private.
// Schemas are captured as owned JSON bytes; retained drafts and Describe reports
// cannot change them. Input schema violations wrap ErrInvalidInput, while
// operational Check errors preserve their cause under ErrInputSchema. Neither
// schema execution nor output encoding causes approve client-facing diagnostics,
// even when they wrap ToolError or ToolFailure. Typed integers and raw JSON
// fields retain number precision; decoding any uses float64 semantics.
//
// Example:
//
//	tool := mcp.NewTool("status", func(ctx context.Context, in mcp.NoInput) (struct{}, error) {
//		return struct{}{}, nil
//	})
//	result, err := tool.Call(context.Background(), nil)
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(result)) // {}
package mcp
