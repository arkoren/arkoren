// Package remote owns native MCP discovery and schema-guided tool invocation.
package remote

import (
	"context"
	"slices"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// NewSession reads endpoint configuration only at an inspector boundary.
// Configuration failures are usage errors and retain their original provenance
// checks; offline commands never call this function.
func NewSession(env console.Environment, flags console.Globals) (*Session, error) {
	found, err := resolveSettings(flags.URL, flags.Token, flags.EnvFile, env.Env, env.Dir)

	if err != nil {
		return nil, console.UsageError("%v", err)
	}

	return &Session{env: env, flags: flags, settings: found}, nil
}

// Session is one run's resolved settings and the streams it prints to.
type Session struct {
	// env supplies process streams and environment lookups for this run.
	env console.Environment

	// flags retains parsed global options independently of remote input.
	flags console.Globals

	// settings contains the selected inspector credentials and their sources.
	settings settings

	// wait is the end-to-end budget for this remote command, when requested.
	wait *WaitBudget
}

// style returns how to print JSON: indented unless -compact, and colored
// only on a terminal that has not opted out with -no-color or NO_COLOR.
func (session *Session) style() console.Style {
	env := session.env
	env.Presentation = session.flags

	return env.JSONStyle()
}

// print writes a JSON document followed by a newline. The stdout boundary in
// run retains either write failure and suppresses subsequent output, so even
// callers with an independent command verdict cannot hide an output failure.
func (session *Session) print(raw []byte) {
	_, _ = session.env.Stdout.Write(console.Render(raw, session.style()))
	_, _ = session.env.Stdout.Write([]byte("\n"))
}

// client builds the HTTP client for the configured URL; without one it is the
// usage error that tells how to set it.
func (session *Session) client() (*Client, error) {
	if session.settings.url.value == "" {
		return nil, console.UsageError("%s", noURLMessage)
	}

	base, err := ParseBaseURL(session.settings.url.value)

	if err != nil {
		return nil, console.UsageError("%v", err)
	}

	client := &Client{Base: base, token: session.settings.token.value}
	client.requestTimeout.Store(int64(session.requestTimeout()))

	return client, nil
}

// requestTimeout resolves one HTTP request's duration independently of a total
// condition-wait budget. Follow and ordinary waiting reads omit the default
// request deadline; an explicitly selected request timeout still bounds waits.
func (session *Session) requestTimeout() time.Duration {
	if session.flags.Follow {
		return 0
	}

	if session.wait != nil && !session.flags.TimeoutSet {
		return 0
	}

	return session.flags.Timeout
}

// connect builds the client, negotiates MCP, and reads the tool catalog. It
// never invokes a tool: role metadata does not establish read-only behavior.
// Callers own the returned connection and must close it after selecting a result.
func (session *Session) connect(ctx context.Context) (*connection, error) {
	client, err := session.client()

	if err != nil {
		return nil, err
	}

	manifest, err := session.discover(ctx, client)

	if err != nil {
		return nil, err
	}

	return &connection{
		Client:  client,
		Catalog: manifest,
	}, nil
}

// connection is a client with the manifest it fetched.
type connection struct {
	// Client sends requests using the resolved transport settings.
	*Client

	// Catalog supplies operation discovery from this client's manifest.
	*Catalog
}

// close releases the SDK session after the command's result has been selected.
// Shutdown failures cannot replace a completed tool or stdout verdict.
func (conn *connection) close() {
	if conn != nil {
		_ = conn.Session.Close()
	}
}

// extension returns the manifest's extension called name, or nil.
func (manifest *Catalog) extension(name string) *Group {
	for index := range manifest.Extensions {
		if manifest.Extensions[index].Name == name {
			return &manifest.Extensions[index]
		}
	}

	return nil
}

// operations returns every tool advertising the same CLI alias in this group.
// Retaining duplicates lets dispatch reject ambiguity and help recommend exact
// native names instead of silently selecting the first discovered action.
func (extension *Group) operations(name string) []*Operation {
	var matches []*Operation

	for index := range extension.Operations {
		if extension.Operations[index].Name == name {
			matches = append(matches, &extension.Operations[index])
		}
	}

	return matches
}

// resolve finds an exact native name before a qualified extension:operation.
// Unknown or malformed commands return usage errors without invoking anything.
func (manifest *Catalog) resolve(word string) (*Group, *Operation, error) {
	if group, operation := manifest.nativeTool(word); operation != nil {
		return group, operation, nil
	}

	if strings.Contains(word, ":") {
		return manifest.resolveQualified(word)
	}

	return nil, nil, manifest.unknownCommand(word)
}

// resolveQualified validates the single colon separator and finds both names
// in the manifest. Suggestions always spell executable qualified commands.
func (manifest *Catalog) resolveQualified(word string) (*Group, *Operation, error) {
	extensionName, operationName, _ := strings.Cut(word, ":")

	if extensionName == "" || operationName == "" || strings.Contains(operationName, ":") {
		return nil, nil, console.UsageError("invalid command %q: use <extension>:<operation> with one colon and nonempty names", word)
	}

	extension := manifest.extension(extensionName)

	if extension == nil {
		return nil, nil, manifest.unknownCommand(word)
	}

	operations := extension.operations(operationName)

	if len(operations) == 0 {
		names := operationCommands(extension)

		return nil, nil, console.UsageError("unknown operation %q of %s%s; run 'arkoren %s: --help' to list operations", operationName, extensionName, suggest(word, names), extensionName)
	}

	if len(operations) > 1 {
		return nil, nil, console.UsageError("ambiguous command %q; use an exact native tool name from 'arkoren tools'", word)
	}

	return extension, operations[0], nil
}

// unknownCommand builds a usage error with nearby local commands
// and qualified operations. Bare extension invocation is not supported.
func (manifest *Catalog) unknownCommand(word string) error {
	names := slices.Clone(localCommandNames)

	for _, extension := range manifest.Extensions {
		names = append(names, operationCommands(&extension)...)
	}

	return console.UsageError("unknown command %q%s; use <extension>:<operation> or run 'arkoren tools' to list the commands", word, suggest(word, names))
}

// operationCommands returns the executable qualified names of an extension's
// operations in manifest order. Suggestions and error listings share this
// spelling so neither can recommend obsolete two-word invocation syntax.
func operationCommands(extension *Group) []string {
	names := make([]string, len(extension.Operations))

	for index, operation := range extension.Operations {
		names[index] = extension.command(operation)
	}

	return names
}

// command chooses an unambiguous spelling within the group. Native identifiers
// remain available when several tools advertise the same optional CLI alias.
func (extension *Group) command(operation Operation) string {
	if len(extension.operations(operation.Name)) > 1 {
		return operation.NativeName
	}

	return commandName(extension.Name, operation.Name)
}

// command prefers the owner's CLI alias only when dispatch resolves it to the
// same native tool. Discovery and operation help share this rule so examples
// cannot accidentally invoke a different tool shadowing an advertised alias.
func (manifest *Catalog) command(extension *Group, operation Operation) string {
	command := extension.command(operation)

	if _, exact := manifest.nativeTool(command); exact != nil && exact.NativeName != operation.NativeName {
		return operation.NativeName
	}

	return command
}

// suggest returns " (did you mean a, b?)" for the names closest to word, or
// "" when none is close.
func suggest(word string, names []string) string {
	near := closest(word, names, 3)

	if len(near) == 0 {
		return ""
	}

	return " (did you mean " + strings.Join(near, ", ") + "?)"
}
