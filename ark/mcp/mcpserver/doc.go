// Package mcpserver adapts core/mcp tools to the official MCP Go SDK's
// stateless Streamable HTTP transport. It is not an LLM model driver or an
// separate inspection protocol. The application owns authentication and HTTP lifecycle.
//
// Construct tools with core/mcp.NewTool, register them through Builder.Tools,
// and mount the resulting Server behind authorization middleware:
//
//	status := mcp.NewTool("status", func(context.Context, mcp.NoInput) (struct{}, error) {
//		return struct{}{}, nil
//	})
//	server, err := mcpserver.New("catalog", "1.0", func(listener *mcpserver.Builder) {
//		listener.Tools(status)
//	})
//	if err != nil {
//		return err
//	}
//	mux.Handle("/mcp", server)
//
// Managed calls decode and parse input once in core, validate encoded output
// without normalization, and never retry application handlers. Errors are safe
// generic messages unless handler errors are explicitly marked safe. ToolError
// approves text; ToolFailure also publishes a detached arkoren.dev/failure record
// in result metadata, never in successful StructuredContent. Local logs
// retain failure details and must be protected. Tools must be concurrency-safe.
// Plain ToolError messages are limited to 4096 UTF-8 bytes at a code-point
// boundary; invalid UTF-8 becomes generic safe text. Core retains the full cause.
// Framework input/output failures override nested approval markers. Operational
// input Check errors become internal failures, not client-correctable violations.
// Raw JSON arguments, schemas, examples, output, and approved failure details
// retain exact number tokens across SDK serialization. Managed output validation
// uses validate.FromJSONSchemaStrict: numeric comparisons are exact, null remains
// distinct from absence, and defaults and formats remain annotations. Unknown
// assertions and unsupported dialects fail registration instead of being ignored.
// The validator's documented resource limits and context cancellation produce
// internal failures, not invalid_output or raw validator diagnostics. A valid
// number such as 1e1000 is accepted within those limits. Output normalization
// never runs. The SDK separately probes numeric literals in declared schemas;
// schemas outside its numeric decoding range fail New before SDK registration.
// That probe never replaces the original schema bytes or the strict validator.
// Client-side generic decoders can still round or reject valid wire numbers.
// Native SDK additions through Server.Server bypass these managed guarantees.
// Managed availability is evaluated independently for listing and calling; policy
// diagnostics always remain local, even when marked with core/mcp.ToolError.
// Listing returns one complete filtered catalog with no cursor or visibility
// cache. Native additions remain visible without managed availability checks.
// Optional CLI presentation is published under arkoren.dev/cli in tools/list
// metadata. There is no separate catalog or Inspection method.
//
// Telemetry uses the arkoren.dev/driver/mcp scope, the mcp call_tool span, and
// arkoren.mcp.calls and arkoren.mcp.call.duration metrics. Only managed names
// and bounded outcomes are recorded, never payloads or gen_ai attributes.
package mcpserver
