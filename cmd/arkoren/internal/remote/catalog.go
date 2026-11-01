package remote

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	coremcp "arkoren.dev/core/mcp"
)

// Operation is the CLI's rendering model for one native MCP tool. NativeName
// always selects the actual tool; optional CLI hints only affect presentation.
type Operation struct {
	// Group is the owner-supplied CLI namespace, empty for native-only tools.
	Group string
	// Name is the hinted operation, or exact tool Name without metadata.
	Name string
	// NativeName is the exact name used by tools/call.
	NativeName string
	// Summary is the native tool description.
	Summary string
	// Kind describes read-only annotations; it is never authorization policy.
	Kind string
	// Role selects explicitly advertised identity and health interpretation.
	Role string
	// Follow requires the advertised cursor convention, never inferred schemas.
	Follow bool
	// Positional is empty unless metadata explicitly declares argument ordering.
	Positional []string
	// Examples are optional CLI Examples attached by the tool owner.
	Examples []coremcp.Example
	// Input and Output preserve native JSON Schema, including unsupported parts.
	Input, Output jsontext.Value
}

// Group groups tools for help. The empty group holds native MCP tools
// that do not advertise CLI grouping; they remain directly callable by name.
type Group struct {
	// Name is the optional CLI group.
	Name string
	// Summary is the optional CLI group description.
	Summary string
	// Operations retains discovery order for readable help and suggestions.
	Operations []Operation
}

// Catalog is the discovered CLI catalog plus native server identity. The SDK
// owns protocol negotiation; no local version gate or custom manifest exists.
type Catalog struct {
	// Protocol is the negotiated native MCP Protocol version.
	Protocol string
	// Arkoren is the server implementation version, when supplied.
	Arkoren string
	// App retains flexible identity output rather than a replicated App DTO.
	App map[string]any
	// Instance retains optional process identity from the identity tool.
	Instance any
	// Extensions is only a CLI grouping of discovered tools.
	Extensions []Group
}

// catalog lists all pages through the SDK and constructs CLI-only view records.
// It does not call identity or other tools as a hidden discovery side effect.
// Each page's generic schemas and metadata are restored from the unchanged wire
// result before presentation, avoiding the SDK's float64 representation.
func (client *Client) catalog(ctx context.Context) (*Catalog, error) {
	initialized := client.Session.InitializeResult()
	manifest := &Catalog{Protocol: initialized.ProtocolVersion, Arkoren: initialized.ServerInfo.Version}
	params := &mcp.ListToolsParams{}

	for {
		page, err := client.Session.ListTools(ctx, params)

		if err != nil {
			return nil, err
		}

		if err := client.wire.restoreTools(page.Tools); err != nil {
			return nil, err
		}

		if err := manifest.addTools(page.Tools); err != nil {
			return nil, err
		}

		if page.NextCursor == "" {
			return manifest, nil
		}

		params.Cursor = page.NextCursor
	}
}

// addTools projects one decoded SDK page onto the CLI's native-name catalog.
// The caller restores raw generic JSON before optional hints are interpreted.
func (manifest *Catalog) addTools(tools []*mcp.Tool) error {
	for _, tool := range tools {
		operation, hints, err := catalogOperation(tool)

		if err != nil {
			return err
		}

		manifest.addOperation(operation, hints)
	}

	return nil
}

// catalogOperation keeps the native tool name authoritative. Missing metadata
// never grants roles, positionals, or follow, even for familiar tool names.
func catalogOperation(tool *mcp.Tool) (Operation, coremcp.CLIInfo, error) {
	operation := Operation{Name: tool.Name, NativeName: tool.Name, Summary: tool.Description, Kind: "action"}
	input, err := json.Marshal(tool.InputSchema, json.Deterministic(true))

	if err != nil {
		return operation, coremcp.CLIInfo{}, err
	}

	output, err := json.Marshal(tool.OutputSchema, json.Deterministic(true))

	if err != nil {
		return operation, coremcp.CLIInfo{}, err
	}

	operation.Input, operation.Output = input, output

	if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
		operation.Kind = "read"
	}

	hints := cliHints(tool)

	if hints.Operation != "" {
		operation.Name = hints.Operation
	}

	operation.Role, operation.Follow = hints.Role, hints.Cursor
	operation.Group = hints.Group
	operation.Positional, operation.Examples = hints.Positional, hints.Examples

	return operation, hints, nil
}

// cliHints normalizes optional SDK metadata through JSON because SDK servers may
// store either structs or decoded maps. Malformed optional hints deliberately
// fall back to native invocation rather than making an external tool unusable.
// Incomplete or colon-bearing hints cannot grant roles or positional inputs.
func cliHints(tool *mcp.Tool) coremcp.CLIInfo {
	var hints coremcp.CLIInfo
	metadata, ok := tool.Meta["arkoren.dev/cli"]

	if !ok {
		return hints
	}

	encoded, err := json.Marshal(metadata, json.Deterministic(true))

	if err != nil {
		return coremcp.CLIInfo{}
	}

	if err := json.Unmarshal(encoded, &hints); err != nil {
		return coremcp.CLIInfo{}
	}

	if hints.Group == "" || hints.Operation == "" {
		return coremcp.CLIInfo{}
	}

	if strings.Contains(hints.Group, ":") || strings.Contains(hints.Operation, ":") {
		return coremcp.CLIInfo{}
	}

	return hints
}

// addOperation groups one tool for help without modifying its native identity.
func (manifest *Catalog) addOperation(operation Operation, hints coremcp.CLIInfo) {
	for index := range manifest.Extensions {
		if manifest.Extensions[index].Name == hints.Group {
			if manifest.Extensions[index].Summary == "" {
				manifest.Extensions[index].Summary = hints.Summary
			}

			manifest.Extensions[index].Operations = append(manifest.Extensions[index].Operations, operation)

			return
		}
	}

	manifest.Extensions = append(manifest.Extensions, Group{
		Name: hints.Group, Summary: hints.Summary, Operations: []Operation{operation},
	})
}

// commandName renders hinted qualified names and bare native names consistently.
func commandName(group, operation string) string {
	if group == "" {
		return operation
	}

	return group + ":" + operation
}

// role finds a uniquely advertised role. Ambiguous roles are not implicitly
// selected for status; explicit tool names remain available to callers.
func (manifest *Catalog) role(name string) *Operation {
	var found *Operation

	for _, extension := range manifest.Extensions {
		matches := extension.roles(name)

		if len(matches) > 1 {
			return nil
		}

		if len(matches) == 0 {
			continue
		}

		if found != nil {
			return nil
		}

		found = matches[0]
	}

	return found
}

// roles finds a group's explicit role declarations for status discovery.
// It retains duplicates so a caller never silently selects an ambiguous role.
func (extension *Group) roles(name string) []*Operation {
	var matches []*Operation

	for index := range extension.Operations {
		operation := &extension.Operations[index]

		if operation.Role == name {
			matches = append(matches, operation)
		}
	}

	return matches
}

// nativeTool resolves an exact protocol identifier before CLI grouping. Native
// tools do not require CLI metadata, and their identifiers are never rewritten.
func (manifest *Catalog) nativeTool(name string) (*Group, *Operation) {
	for index := range manifest.Extensions {
		group := &manifest.Extensions[index]
		position := slices.IndexFunc(group.Operations, func(operation Operation) bool { return operation.NativeName == name })

		if position >= 0 {
			return group, &group.Operations[position]
		}
	}

	return nil, nil
}

// readIdentity invokes the uniquely advertised identity role for an explicit
// status report, retaining arbitrary identity members and any version override.
// Missing or ambiguous roles are ignored; call and JSON decoding failures stop
// status before health or output. Discovery and help must not call this method:
// an identity role does not establish that the tool is free of side effects.
func (conn *connection) readIdentity(ctx context.Context) error {
	operation := conn.role("identity")

	if operation == nil {
		return nil
	}

	body, err := conn.callTool(ctx, operation.NativeName, []byte("{}"))

	if err != nil {
		return err
	}

	var identity map[string]any

	if err := json.Unmarshal(body, &identity); err != nil {
		return err
	}

	conn.App = identity
	conn.Instance = identity["instance"]

	if version, ok := identity["arkoren"].(string); ok {
		conn.Arkoren = version
	}

	return nil
}
