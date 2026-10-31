package mcp

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
)

// Example is a documented argument object, never an invocation. Input is copied
// when captured and reported; examples do not execute schema rules or handlers.
// Publication checks typed JSON decoding, which may invoke custom unmarshaling
// methods. Those application methods must be safe to run during construction.
//
// Example: sample := mcp.Example{Description: "Recent", Input: jsontext.Value(`{"limit":10}`)}
type Example struct {
	// Description explains the example's purpose, not its result.
	// Example: fmt.Println(sample.Description)
	Description string `json:"description"`
	// Input is a JSON argument object compatible with the tool's Go input type.
	// Example: fmt.Println(string(sample.Input))
	Input jsontext.Value `json:"input"`
}

// CLIInfo reports optional CLI presentation in tools/list metadata. It does not
// rename the protocol tool or grant permissions. Cursor indicates ordinary
// bounded calls with cursor arguments, not a separate streaming transport.
// Roles label diagnostic meaning for clients, not command aliases or policy.
// Field examples below inspect a published Tool; its CLI report can be nil.
//
// Example: if info := tool.Describe(); info.CLI != nil { fmt.Println(info.CLI.Operation) }
type CLIInfo struct {
	// Group is the CLI namespace; protocol names remain unchanged.
	//
	// Example:
	//
	//	if info := tool.Describe().CLI; info != nil {
	//		fmt.Println(info.Group)
	//	}
	Group string `json:"group"`
	// Operation is the command within Group.
	//
	// Example:
	//
	//	if info := tool.Describe().CLI; info != nil {
	//		fmt.Println(info.Operation)
	//	}
	Operation string `json:"operation"`
	// Summary optionally describes the group in help output.
	//
	// Example:
	//
	//	if info := tool.Describe().CLI; info != nil {
	//		fmt.Println(info.Summary)
	//	}
	Summary string `json:"summary,omitempty"`
	// Role optionally labels a diagnostic meaning, such as identity or health.
	// Empty means unspecified; a role neither authorizes use nor promises safety.
	//
	// Example:
	//
	//	if info := tool.Describe().CLI; info != nil {
	//		fmt.Println(info.Role)
	//	}
	Role string `json:"role,omitempty"`
	// Cursor advertises follow through repeated bounded tool calls. The handler
	// still owns cursor arguments and results; this field installs no behavior.
	//
	// Example:
	//
	//	if info := tool.Describe().CLI; info != nil {
	//		fmt.Println(info.Cursor)
	//	}
	Cursor bool `json:"cursor,omitempty"`
	// Positional lists input property names in command-line argument order.
	//
	// Example:
	//
	//	if info := tool.Describe().CLI; info != nil {
	//		fmt.Println(info.Positional)
	//	}
	Positional []string `json:"positional,omitempty"`
	// Examples contains detached, typed-decodable argument objects, or nil when
	// none were declared. Reading a report does not repeat their decoding hooks.
	//
	// Example:
	//
	//	if info := tool.Describe().CLI; info != nil {
	//		for _, sample := range info.Examples {
	//			fmt.Println(sample.Description, string(sample.Input))
	//		}
	//	}
	Examples []Example `json:"examples,omitempty"`
}

// cloneCLI detaches owned containers, including each example's JSON bytes.
// A nil report stays nil, distinguishing absence from an invalid empty group.
func cloneCLI(info *CLIInfo) *CLIInfo {
	if info == nil {
		return nil
	}

	report := *info
	report.Positional = slices.Clone(info.Positional)
	report.Examples = slices.Clone(info.Examples)

	for index := range report.Examples {
		report.Examples[index].Input = slices.Clone(report.Examples[index].Input)
	}

	return &report
}

// cli allocates optional presentation only when a CLI setting is supplied.
// Construction owns this draft until publication detaches it.
func (action *ToolBuilder[In, Out]) cli() *CLIInfo {
	if action.info.CLI == nil {
		action.info.CLI = &CLIInfo{}
	}

	return action.info.CLI
}

// CLI sets the command namespace and operation without changing Tool.Name.
// Both must be nonempty protocol-style identifiers; final checks run after all
// callbacks, so settings can be declared in any order.
//
// Example: action.CLI("telemetry", "traces")
func (action *ToolBuilder[In, Out]) CLI(group, operation string) *ToolBuilder[In, Out] {
	action.cli().Group, action.cli().Operation = group, operation

	return action
}

// GroupSummary replaces optional group help text. Empty removes the summary;
// it is presentation only and does not affect the tool description.
//
// Example: action.GroupSummary("Captured telemetry")
func (action *ToolBuilder[In, Out]) GroupSummary(summary string) *ToolBuilder[In, Out] {
	action.cli().Summary = summary

	return action
}

// Role labels optional diagnostic meaning for clients. Accepted values are
// identity, health, routes, traces, trace, logs, metrics, or empty to omit it.
// It does not rename the command, register a shortcut, authorize a call, or
// certify that the handler is read-only; those remain separate responsibilities.
//
// Example: action.Role("health")
func (action *ToolBuilder[In, Out]) Role(role string) *ToolBuilder[In, Out] {
	action.cli().Role = role

	return action
}

// Cursor advertises follow by repeated bounded calls using ordinary arguments.
// It installs no handler, goroutine, or transport and defaults to false.
//
// Example: action.Cursor(true)
func (action *ToolBuilder[In, Out]) Cursor(cursor bool) *ToolBuilder[In, Out] {
	action.cli().Cursor = cursor

	return action
}

// Positional replaces the ordered input property names and copies the slice.
// Names must be unique properties of the final input schema; empty clears them.
//
// Example: action.Positional("id")
func (action *ToolBuilder[In, Out]) Positional(names ...string) *ToolBuilder[In, Out] {
	action.cli().Positional = slices.Clone(names)

	return action
}

// Example appends a documented JSON argument object, retaining its own bytes.
// Final validation checks strict typed decoding only, never contextual schema
// rules or handlers. Examples are illustrative, not authorization guarantees.
// Custom JSON unmarshaling methods can run during NewTool publication, so they
// must be safe at construction time. Their errors become declaration errors.
// Availability guards calls, not these publication-time hooks. Custom methods
// remain responsible for their own decoding semantics and side effects.
//
// Example: action.Example("Recent traces", `{"limit":10}`)
func (action *ToolBuilder[In, Out]) Example(description, input string) *ToolBuilder[In, Out] {
	action.cli().Examples = append(action.cli().Examples, Example{
		Description: description, Input: jsontext.Value(input),
	})

	return action
}

// validateCLI checks structural presentation against the completed schemas.
// It never calls schema Parse, availability policies, or handlers, but typed
// example decoding may run application JSON methods and retain their failures.
func (action *ToolBuilder[In, Out]) validateCLI() error {
	info := action.info.CLI

	if info == nil {
		return nil
	}

	if !toolName.MatchString(info.Group) || !toolName.MatchString(info.Operation) {
		return errors.New("CLI group and operation must be nonempty identifiers")
	}

	if !slices.Contains([]string{"", "identity", "health", "routes", "traces", "trace", "logs", "metrics"}, info.Role) {
		return fmt.Errorf("unsupported CLI role %q", info.Role)
	}

	if err := validatePositional(action.info.InputSchema, info.Positional); err != nil {
		return err
	}

	for _, example := range info.Examples {
		if err := validateExample[In](example.Input); err != nil {
			return fmt.Errorf("CLI example %q: %w", example.Description, err)
		}
	}

	return nil
}

// validatePositional requires distinct top-level input properties, preserving
// their declared ordering rather than inferring an order from a map.
func validatePositional(document jsontext.Value, names []string) error {
	var schema struct {
		// Properties indexes the final schema's top-level arguments.
		Properties map[string]jsontext.Value `json:"properties"`
	}

	if err := json.Unmarshal(document, &schema); err != nil {
		return err
	}

	seen := make(map[string]bool, len(names))

	for _, name := range names {
		if seen[name] || schema.Properties[name] == nil {
			return fmt.Errorf("CLI positional %q is duplicate or not an input property", name)
		}

		seen[name] = true
	}

	return nil
}

// validateExample checks object shape and strict Go input compatibility only.
// It deliberately avoids Parse, which may depend on context or perform work.
// Ordinary JSON decoding still invokes custom unmarshaling methods; it is not
// a side-effect-free inspection of the input type. Hook errors are preserved.
func validateExample[In any](input jsontext.Value) error {
	if input.Kind() != '{' {
		return errors.New("input must be an object")
	}

	var value In

	return json.Unmarshal(input, &value, json.RejectUnknownMembers(true))
}
