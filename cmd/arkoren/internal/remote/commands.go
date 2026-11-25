package remote

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/core/mcp"
)

// States of [inspectorState].
const (
	StateOK            = "ok"
	StateNotConfigured = "not configured"
	StateUnreachable   = "unreachable"
	stateError         = "error"
)

// inspectorState describes the selected endpoint without full runtime identity.
type inspectorState struct {
	// State is one of the State constants.
	State string `json:"state"`

	// Protocol is the SDK-negotiated native MCP version after discovery.
	Protocol string `json:"protocol,omitzero"`

	// Arkoren retains the server implementation version after discovery.
	Arkoren string `json:"arkoren,omitzero"`

	// App is optional application context. Discovery leaves it empty because
	// obtaining application identity requires an explicit tool invocation.
	App map[string]any `json:"app,omitzero"`

	// Error says why the state is not ok. Its remedies are not nested: the
	// command lists them in its top-level next_steps.
	Error console.ErrorReport `json:"error,omitzero"`

	// steps resolve Error; the report hoists them to its next_steps.
	steps []console.Step
}

// probe connects without failing the command: it returns the connection when
// the handshake worked, and otherwise the state to report. failure is the
// error to end the command with: nil when the inspector is merely not
// configured or unreachable, which catalog discovery reports as information.
// Only MCP initialization and tools/list run; identity roles are not invoked.
func (session *Session) probe(ctx context.Context) (conn *connection, state inspectorState, failure error) {
	if session.settings.url.value == "" {
		return nil, inspectorState{
			State: StateNotConfigured,
			Error: console.ErrorReport{Message: noURLMessage, Type: console.TypeNotConfigured},
			steps: NoURLSteps(),
		}, nil
	}

	conn, err := session.connect(ctx)

	if err == nil {
		return conn, inspectorState{
			State:    StateOK,
			Protocol: conn.Protocol,
			Arkoren:  conn.Arkoren,
		}, nil
	}

	doc := DescribeError(err, session.flags.Verbose, session.env.Invocation)

	if _, ok := errors.AsType[*unreachableError](err); ok {
		return nil, inspectorState{State: StateUnreachable, Error: doc.Error, steps: doc.NextSteps}, nil
	}

	if exit, ok := errors.AsType[*console.ExitError](err); ok && exit.Code == console.ExitUsage {
		return nil, inspectorState{}, err
	}

	return nil, inspectorState{State: stateError, Error: doc.Error, steps: doc.NextSteps}, err
}

// noURLMessage says the inspector URL is missing; [NoURLSteps] say how to
// supply it.
const noURLMessage = "no inspector URL is configured"

// Tools prints JSON discovery after static argument validation. Discovery
// failures retain their exit policy after the catalog state is written.
func (session *Session) Tools(ctx context.Context) error {
	conn, state, failure := session.probe(ctx)
	defer conn.close()

	if failure != nil && state.State == "" {
		return failure
	}

	report := ToolsReport{Inspector: state, Tools: []ToolSummary{}, NextSteps: state.steps}

	if conn != nil {
		report.Tools = SummarizeTools(conn.Catalog, session.flags.Verbose)
	}

	if err := session.env.Result(report); err != nil {
		return err
	}

	return failure
}

// ToolsReport contains only endpoint context and callable tool summaries.
type ToolsReport struct {
	// Inspector distinguishes empty catalogs from unavailable discovery.
	Inspector inspectorState `json:"inspector"`

	// Tools is sorted by command; verbose mode adds invocation schemas and hints.
	Tools []ToolSummary `json:"tools"`

	// NextSteps resolves an inspector error: the remedies of Inspector.Error,
	// hoisted so every document lists steps at its top level.
	NextSteps []console.Step `json:"next_steps,omitzero"`
}

// ToolSummary is the information needed to choose a tool before reading help.
// Kind and Follow are advertised hints, not authorization guarantees. Fields
// are ordered for reading: the identity of the tool, then its description,
// then the verbose-only detail.
type ToolSummary struct {
	// Command is the CLI spelling, including any advertised namespace.
	Command string `json:"command"`

	// NativeName is the exact MCP name when it differs from Command.
	NativeName string `json:"native_name,omitzero"`

	// Kind is read when annotated read-only, otherwise action.
	Kind string `json:"kind"`

	// Follow reports support for bounded cursor polling.
	Follow bool `json:"follow"`

	// Description is the owner's explanation of the tool. An abbreviated
	// summary ends with "…"; --verbose gives the full text.
	Description string `json:"description,omitzero"`

	// InputSchema is included only with --verbose.
	InputSchema jsontext.Value `json:"input_schema,omitzero"`

	// OutputSchema is included with --verbose when the owner advertises one.
	OutputSchema jsontext.Value `json:"output_schema,omitzero"`

	// Positional preserves advertised argument order in verbose discovery.
	Positional []string `json:"positional,omitzero"`

	// Examples contains owner-supplied Examples only in verbose discovery.
	Examples []mcp.Example `json:"examples,omitzero"`

	// Role is the explicit semantic Role, included only with --verbose.
	Role string `json:"role,omitzero"`
}

// SummarizeTools projects discovery onto the CLI's own presentation contract.
func SummarizeTools(manifest *Catalog, verbose bool) []ToolSummary {
	tools := []ToolSummary{}

	for _, extension := range manifest.Extensions {
		for _, operation := range extension.Operations {
			command := manifest.command(&extension, operation)
			native := operation.NativeName

			if command == native {
				native = ""
			}

			tool := ToolSummary{
				Command: command, NativeName: native, Description: operation.Summary,
				Kind: operation.Kind, Follow: operation.Follow,
			}

			if verbose {
				if operation.Input.Kind() != 'n' {
					tool.InputSchema = operation.Input
				}

				if operation.Output.Kind() != 'n' {
					tool.OutputSchema = operation.Output
				}

				tool.Positional, tool.Examples, tool.Role = operation.Positional, operation.Examples, operation.Role
			} else {
				tool.Description, _ = console.DiagnosticText(operation.Summary)
			}

			tools = append(tools, tool)
		}
	}

	slices.SortFunc(tools, func(left, right ToolSummary) int {
		return cmp.Compare(left.Command, right.Command)
	})

	return tools
}

// summarizeIdentity keeps recognizable application identity in summary reports.
// Full identity remains available through verbose status or the identity tool.
func summarizeIdentity(identity map[string]any) map[string]any {
	summary := make(map[string]any)

	for _, name := range []string{"name", "version", "environment", "state"} {
		if value, ok := identity[name]; ok {
			summary[name] = value
		}
	}

	if len(summary) == 0 {
		return nil
	}

	return summary
}

// remoteHelp describes an advertised native tool or extension without invoking
// it. Exact native names take precedence, matching ordinary remote execution;
// a trailing colon explicitly selects a metadata-defined extension instead.
func (session *Session) remoteHelp(conn *connection, word string) error {
	if _, operation := conn.nativeTool(word); operation == nil {
		if extension := conn.extension(strings.TrimSuffix(word, ":")); extension != nil {
			writeExtensionHelp(session.env.Stdout, conn.Catalog, extension, session.style().Color)

			return nil
		}
	}

	extension, operation, err := conn.resolve(word)

	if err != nil {
		return err
	}

	selected := *operation
	selected.Name = conn.command(extension, *operation)

	WriteOperationHelp(session.env.Stdout, "", &selected, session.style().Color)

	return nil
}

// StatusReport is the output of the status command, in the local report
// envelope: a summary and, when the server has a health role, an ok verdict.
type StatusReport struct {
	// Summary names the connected application, as "Connected to shop 1.2.3
	// (production)", or the URL when the server has no application identity.
	Summary string `json:"summary"`

	// OK is the health verdict: false when the health report failed or its
	// status is "failing". It is absent when the server has no health role.
	OK *bool `json:"ok,omitzero"`

	// App is the application name, version, environment, and state when provided.
	App map[string]any `json:"app,omitzero"`

	// Health is the output of "app:Health" when the server has it, or an
	// object with the error that stopped it.
	Health jsontext.Value `json:"health,omitzero"`

	// Protocol is the SDK-negotiated native MCP Protocol version.
	Protocol string `json:"protocol"`

	// Arkoren is the server implementation version, or an identity override.
	Arkoren string `json:"arkoren,omitzero"`

	// Extensions counts the operation namespaces in the manifest.
	Extensions int `json:"extensions"`

	// Instance retains optional owner-defined process identity; absent when the
	// identity tool does not provide it. No process identity is fabricated.
	Instance any `json:"instance,omitzero"`

	// URL is the selected inspection root.
	URL string `json:"url"`

	// URLSource names the flag, environment, or file that supplied URL.
	URLSource string `json:"url_source"`

	// TokenSet reports credential presence without exposing its contents.
	TokenSet bool `json:"token_set"`

	// TokenSource identifies the credential source; empty sources are omitted.
	TokenSource string `json:"token_source,omitzero"`

	// NextSteps resolves a failed health call: the remedies of the error in
	// Health, hoisted to the top level.
	NextSteps []console.Step `json:"next_steps,omitzero"`
}

// statusSummary names the connected application from its identity members, or
// the URL when the identity has no name.
func statusSummary(identity map[string]any, url string) string {
	name, _ := identity["name"].(string)

	if name == "" {
		return "Connected to " + url
	}

	summary := "Connected to " + name

	if version, _ := identity["version"].(string); version != "" {
		summary += " " + version
	}

	if environment, _ := identity["environment"].(string); environment != "" {
		summary += " (" + environment + ")"
	}

	return summary
}

// Status explicitly obtains optional identity and health roles for its report.
// Identity failures stop before health or output; health failures remain report
// data rather than command failures. Result write errors retain precedence.
func (session *Session) Status(ctx context.Context) error {
	conn, err := session.connect(ctx)

	if err != nil {
		return err
	}

	defer conn.close()

	if err := conn.readIdentity(ctx); err != nil {
		return err
	}

	report := StatusReport{
		App:         summarizeIdentity(conn.App),
		Arkoren:     conn.Arkoren,
		Protocol:    conn.Protocol,
		Instance:    conn.Instance,
		Extensions:  len(conn.Extensions),
		URL:         session.settings.url.value,
		URLSource:   session.settings.url.from,
		TokenSet:    session.settings.token.value != "",
		TokenSource: session.settings.token.from,
	}

	if session.flags.Verbose {
		report.App = conn.App
	}

	report.Summary = statusSummary(conn.App, report.URL)

	if operation := conn.role("health"); operation != nil {
		health, err := conn.callTool(ctx, operation.NativeName, []byte("{}"))
		healthy := err == nil && healthVerdict("health", health) == nil

		if err != nil {
			doc := DescribeError(err, session.flags.Verbose, session.env.Invocation)
			health, _ = json.Marshal(map[string]console.ErrorReport{"error": doc.Error}, json.Deterministic(true))
			report.Health = health
			report.NextSteps = doc.NextSteps
		} else {
			report.Health = SummarizeHealth(health, session.flags.Verbose)
		}

		report.OK = &healthy
	}

	return session.env.Result(report)
}

// invoke sends one prepared request and prints its result, or with -f the
// stream, one compact object per line. Preparation and command-budget ownership
// stay in remote so the same context also bounds connection cleanup.
func (session *Session) invoke(ctx context.Context, conn *connection, operation *Operation, input *requestInput) error {
	label := commandName(operation.Group, operation.Name)

	if session.flags.Follow && !operation.Follow {
		return console.UsageError("%s cannot follow: -f is only valid for operations marked follow in 'arkoren tools'", label)
	}

	var err error

	if session.flags.Follow {
		err = conn.Follow(ctx, operation.NativeName, input.body, session.printLine)
	} else {
		var result []byte

		if WaitingRead(operation, input.root, input.body) {
			result, err = session.observe(ctx, func() ([]byte, error) {
				if err := remainingWait(input.root, input, session.wait); err != nil {
					return nil, err
				}

				return conn.callTool(ctx, operation.NativeName, input.body)
			})
		} else {
			result, err = conn.callTool(ctx, operation.NativeName, input.body)
		}

		if err == nil {
			session.print(result)
			err = healthVerdict(operation.Role, result)
		}
	}

	if failure, ok := errors.AsType[*APIError](err); ok {
		failure.origins, failure.operation = input.origins, label
	}

	return err
}

// prepareOperation converts CLI sources once, preserving the schema and input
// origins for wait-budget updates and server validation diagnostics.
func (session *Session) prepareOperation(ctx context.Context, operation *Operation, args []string) (*requestInput, error) {
	root := ParseSchema(operation.Input)
	parsed, err := parseOpArgs(root, commandName(operation.Group, operation.Name), args)

	if err != nil {
		return nil, err
	}

	if parsed.hasInput {
		body, err := session.readOperationSource(ctx, parsed.input)

		if err != nil {
			return nil, err
		}

		parsed.body = body
	}

	input, err := buildInput(root, operation.Positional, parsed, session.env.Dir, session.env.Stdin)

	if err != nil {
		return nil, err
	}

	input.root = root

	return input, nil
}

// printLine writes a compact follow record while preserving color policy and
// returning stdout failures before a concurrent cancellation can hide them.
func (session *Session) printLine(data []byte) error {
	look := console.Style{Color: session.style().Color}

	_, err := session.env.Stdout.Write(append(console.Render(CompactJSON(data), look), '\n'))

	return err
}

// observe retries an explicitly requested, schema-declared waiting read only.
// Every attempt receives the remaining shared budget. Actions never enter this
// path, and tool/protocol/HTTP failures are terminal rather than replayable.
func (session *Session) observe(ctx context.Context, call func() ([]byte, error)) ([]byte, error) {
	for {
		result, err := call()

		if err == nil {
			return result, nil
		}

		if session.wait == nil || !retryableTransport(err) {
			return nil, err
		}

		if err := waitRetry(ctx, waitInitialBackoff); err != nil {
			return nil, err
		}
	}
}

// WaitingRead recognizes the schema-declared wait convention without knowing
// an extension's names or condition semantics. The command budget includes
// discovery and connection retries; an explicit request deadline still applies.
func WaitingRead(operation *Operation, root *Schema, body []byte) bool {
	if operation.Kind != "read" {
		return false
	}

	wait := root.Properties["wait"]

	if wait == nil || wait.kind() != "boolean" {
		return false
	}

	var input struct {
		// Wait is decoded only after the input schema declares a boolean Wait.
		Wait bool `json:"wait"`
	}

	return json.Unmarshal(body, &input) == nil && input.Wait
}

// healthVerdict makes a failing "app:health" report an exit code 1, the one
// operation whose output the CLI interprets, so scripts and agents can gate
// on it. The report has already been printed.
func healthVerdict(role string, result []byte) error {
	if role != "health" {
		return nil
	}

	var report struct {
		// Status is the sole health member that affects the process exit code.
		Status string `json:"status"`
	}

	if json.Unmarshal(result, &report) == nil && report.Status == "failing" {
		return &console.ExitError{
			Code: console.ExitFailure,
		}
	}

	return nil
}
