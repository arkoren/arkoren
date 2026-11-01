package remote

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// localCommandNames supplies typo suggestions; usage lives in the command tree.
var localCommandNames = []string{"certificate", "key", "new", "skills", "tools", "status"}

// GlobalFlagDef documents one global flag, which is accepted anywhere on the
// command line and parsed before the operation's own flags.
type GlobalFlagDef struct {
	// Names are the spellings without dashes.
	Names []string `json:"names"`

	// Value is the placeholder of the flag's argument; empty for a boolean.
	Value string `json:"value,omitzero"`

	// Doc says what the flag does.
	Doc string `json:"doc"`
}

// GlobalFlagDefs is every global flag. A property of an operation's input
// that has one of these names can only be set with -input.
var GlobalFlagDefs = []GlobalFlagDef{
	{
		Names: []string{"url"},
		Value: "url",
		Doc:   "Inspection root URL (required; else " + EnvURL + ", else ./.env)",
	},
	{
		Names: []string{"token"},
		Value: "token",
		Doc:   "Bearer token (else " + EnvToken + ", else ./.env); environment/flag tokens need a pinned or explicitly approved URL",
	},
	{
		Names: []string{"env-file"},
		Value: "path",
		Doc:   "Read this env file instead of ./.env and approve its URL for environment/flag tokens; it must exist; an empty value reads none",
	},
	{
		Names: []string{"compact"},
		Value: "",
		Doc:   "Print each JSON document on one line",
	},
	{
		Names: []string{"verbose"},
		Doc:   "Include detailed diagnostics, file inventories, tool schemas, and status/build information",
	},
	{
		Names: []string{"no-color"},
		Value: "",
		Doc:   "Never color output (NO_COLOR does the same); output is colored only on a terminal",
	},
	{
		Names: []string{"request-timeout"},
		Value: "duration",
		Doc:   "HTTP request deadline, default 10s; wait=true uses one total timeout budget instead; 0 removes only the per-request deadline; not applied to -f",
	},
	{
		Names: []string{"f", "follow"},
		Value: "",
		Doc:   "Stream an operation that can follow, one compact JSON object per line, until interrupted",
	},
	{
		Names: []string{"h", "help"},
		Value: "",
		Doc:   "Show help for the command",
	},
}

// writeExtensionHelp lists one extension with unambiguous command spellings.
// The complete catalog preserves exact native-name precedence across groups.
// run's stdout boundary retains write errors; a failed flush stops rendering.
func writeExtensionHelp(out io.Writer, manifest *Catalog, extension *Group, color bool) {
	fmt.Fprintf(out, "arkoren %s:<operation> [flags] [arguments]\n", extension.Name)

	if extension.Summary != "" {
		fmt.Fprintf(out, "\n%s\n", extension.Summary)
	}

	fmt.Fprintln(out, "\n"+helpLabel("Operations:", color))

	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	for _, operation := range extension.Operations {
		fmt.Fprintf(table, "  %s\t%s\t%s\n", manifest.command(extension, operation), operationTraits(operation), operation.Summary)
	}

	if err := table.Flush(); err != nil {
		return
	}

	fmt.Fprintf(out, "\nRun 'arkoren %s:<operation> --help' for flags, examples, and output.\n", extension.Name)
}

// WriteOperationHelp writes the full help of one operation: usage, flags
// generated from the input schema, examples as command lines and as -input
// JSON, and the output schemas.
// run's stdout boundary retains write errors; a failed flush stops rendering.
func WriteOperationHelp(out io.Writer, extension string, operation *Operation, color bool) {
	root := ParseSchema(operation.Input)
	command := "arkoren " + commandName(extension, operation.Name)

	usage := command + " [flags]"

	for _, name := range operation.Positional {
		usage += " [" + name + "]"
	}

	fmt.Fprintf(out, "%s\n", usage)

	if operation.Summary != "" {
		fmt.Fprintf(out, "\n%s\n", operation.Summary)
	}

	fmt.Fprintf(out, "\n%s %s", helpLabel("Kind:", color), operation.Kind)

	if operation.Kind != "read" {
		fmt.Fprint(out, " (may change application state; it runs as soon as you call it)")
	}

	fmt.Fprintln(out)

	if operation.Follow {
		fmt.Fprintln(out, helpLabel("Follow:", color)+" yes; add -f to stream items as one JSON object per line")
	} else {
		fmt.Fprintln(out, helpLabel("Follow:", color)+" no")
	}

	if len(operation.Positional) > 0 {
		fmt.Fprintln(out, "\n"+helpLabel("Arguments, in order, set the property of the same name:", color))
		fmt.Fprintf(out, "  %s\n", strings.Join(operation.Positional, ", "))
	}

	if err := writeInputFlags(out, root, color); err != nil {
		return
	}

	if len(operation.Examples) > 0 {
		fmt.Fprintln(out, "\n"+helpLabel("Examples:", color))
	}

	for _, example := range operation.Examples {
		fmt.Fprintf(out, "  %s\n", example.Description)
		fmt.Fprintf(out, "    %s\n", exampleCommand(command, operation.Positional, example.Input))
		fmt.Fprintf(out, "    %s -input %s\n", command, shellQuote(string(CompactJSON(example.Input))))
	}

	fmt.Fprintf(out, "\n%s\n%s\n", helpLabel("Output (JSON Schema):", color), indentSchema(operation.Output, color))
}

// helpLabel matches the bold headings of generated local help, without styling
// piped text or bypassing the invocation's color opt-outs.
func helpLabel(label string, color bool) string {
	if color {
		return "\x1b[1m" + label + "\x1b[0m"
	}

	return label
}

// writeInputFlags renders the supported schema subset and reserved-name advice.
// Unsupported schema constructs remain available through whole-object -input.
func writeInputFlags(out io.Writer, root *Schema, color bool) error {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	flags := root.flags("", 0)

	fmt.Fprintln(out, "\n"+helpLabel("Flags (each sets that path of the input; -input takes the whole object as JSON):", color))

	if len(flags) == 0 {
		fmt.Fprintln(out, "  none")
	}

	var shadowed []string

	for _, info := range flags {
		fmt.Fprintf(table, "  %s\t%s\t%s\n", inputFlagSpelling(root, info), info.schema.typeName(), inputFlagDescription(info))

		if isGlobalName(info.path) || info.path == "input" {
			shadowed = append(shadowed, info.path)
		}
	}

	if err := table.Flush(); err != nil {
		return err
	}

	if len(shadowed) > 0 {
		fmt.Fprintf(out, "\nThese properties share a name with a global flag, so only -input can set them: %s\n", strings.Join(shadowed, ", "))
	}

	fmt.Fprintln(out, "\nGlobal flags: -input, -f, -compact, -verbose, -request-timeout, -url, -token, -no-color, -env-file (run 'arkoren --help' for global flag usage).")

	return nil
}

// inputFlagDescription combines owner descriptions with schema hints without
// implying that the CLI enforces enum, required, or default validation locally.
func inputFlagDescription(info flagInfo) string {
	details := info.schema.Description

	if info.required {
		details = joinNonEmpty(" ", details, "(required)")
	}

	if len(info.schema.Enum) > 0 {
		details = joinNonEmpty(" ", details, "(one of "+joinValues(info.schema.Enum)+")")
	}

	if len(info.schema.Default) > 0 {
		details = joinNonEmpty(" ", details, "(default "+string(info.schema.Default)+")")
	}

	return details
}

// inputFlagSpelling advertises kebab aliases only when the input resolver would
// map them back to this exact property and no global flag reserves the name.
func inputFlagSpelling(root *Schema, info flagInfo) string {
	spelling := "-" + info.path
	alias := strings.ReplaceAll(info.path, "_", "-")

	if alias == info.path || isGlobalName(alias) {
		return spelling
	}

	path, _, err := root.flagPath(alias)

	if err == nil && strings.Join(path, ".") == info.path {
		return spelling + ", -" + alias
	}

	return spelling
}

// isGlobalName reports whether a flag path is the name of a global flag.
func isGlobalName(path string) bool {
	return GlobalFlagFor(path) != nil
}

// GlobalFlagFor returns the global flag spelled name, or nil.
func GlobalFlagFor(name string) *GlobalFlagDef {
	for index := range GlobalFlagDefs {
		if slices.Contains(GlobalFlagDefs[index].Names, name) {
			return &GlobalFlagDefs[index]
		}
	}

	return nil
}

// operationTraits renders the kind and follow support of an operation for the
// listings: "read", "read, follow", or "action".
func operationTraits(operation Operation) string {
	if operation.Follow {
		return operation.Kind + ", follow"
	}

	return operation.Kind
}

// joinNonEmpty joins the non-empty parts.
func joinNonEmpty(separator string, parts ...string) string {
	return strings.Join(slices.DeleteFunc(slices.Clone(parts), func(part string) bool {
		return part == ""
	}), separator)
}

// joinValues renders enum values as a comma-separated list, without the
// quotes of strings.
func joinValues(values []jsontext.Value) string {
	texts := make([]string, len(values))

	for index, value := range values {
		texts[index] = scalarText(value)
	}

	return strings.Join(texts, ", ")
}

// scalarText returns the text of a scalar JSON value: a string unquoted,
// anything else as written.
func scalarText(value jsontext.Value) string {
	if value.Kind() == '"' {
		var text string

		if json.Unmarshal(value, &text) == nil {
			return text
		}
	}

	return string(value)
}

// indentSchema formats an advertised schema under its heading. Missing schemas
// are described as unavailable rather than rendered as a misleading JSON null.
func indentSchema(doc jsontext.Value, color bool) string {
	if len(doc) == 0 || doc.Kind() == 'n' {
		return "  not advertised"
	}

	text := strings.TrimSpace(string(console.Render(doc, console.Style{
		Indent: true,
		Color:  color,
	})))

	return "  " + strings.ReplaceAll(text, "\n", "\n  ")
}

// exampleCommand renders an example input as a command line: leading
// positional properties with scalar values become arguments, and everything
// else becomes path flags, so the line teaches the flag syntax.
func exampleCommand(command string, positional []string, input jsontext.Value) string {
	tree, err := parseTree(input)
	members, ok := tree.(map[string]any)

	if err != nil || !ok {
		return command + " -input " + shellQuote(string(CompactJSON(input)))
	}

	parts := []string{command}

	for _, name := range positional {
		value, present := members[name].(jsontext.Value)
		text := scalarText(value)

		if !present || value.Kind() == '{' || strings.HasPrefix(text, "-") {
			break
		}

		parts = append(parts, shellQuote(text))
		delete(members, name)
	}

	return strings.Join(append(parts, flagsFor("", members)...), " ")
}

// flagsFor renders a tree as path flags, in key order. An object becomes one
// flag per member, an array of scalars a repeated flag, an array of objects
// flags with indexes, and anything a path cannot express (a key containing a
// dot, mixed elements) a JSON value.
func flagsFor(prefix string, node any) []string {
	asJSON := func() []string {
		// parseTree produced only containers and valid raw JSON scalars, all JSON-encodable.
		encoded, _ := json.Marshal(node, json.Deterministic(true))

		return []string{flagText(prefix, string(encoded))}
	}

	join := func(name string) string {
		if prefix == "" {
			return name
		}

		return prefix + "." + name
	}

	switch typed := node.(type) {
	case map[string]any:
		if len(typed) == 0 && prefix != "" || slices.ContainsFunc(slices.Collect(maps.Keys(typed)), func(key string) bool {
			return key == "" || strings.Contains(key, ".")
		}) {
			return asJSON()
		}

		var flags []string

		for _, key := range slices.Sorted(maps.Keys(typed)) {
			flags = append(flags, flagsFor(join(key), typed[key])...)
		}

		return flags
	case []any:
		if len(typed) == 0 {
			return asJSON()
		}

		var flags []string

		for index, element := range typed {
			switch element.(type) {
			case jsontext.Value:
				flags = append(flags, flagsFor(prefix, element)...)
			case map[string]any:
				if _, scalar := typed[0].(jsontext.Value); scalar {
					return asJSON()
				}

				flags = append(flags, flagsFor(join(strconv.Itoa(index)), element)...)
			default:
				return asJSON()
			}
		}

		return flags
	case jsontext.Value:
		switch string(typed) {
		case "true":
			return []string{"-" + prefix}
		case "false":
			return []string{"-" + prefix + "=false"}
		}

		return []string{flagText(prefix, scalarText(typed))}
	}

	return asJSON()
}

// flagText renders one flag with its value, quoted for a shell. A value that
// starts with a dash uses the -name=value form so it is not read as a flag.
func flagText(name, value string) string {
	if strings.HasPrefix(value, "-") {
		return "-" + name + "=" + shellQuote(value)
	}

	return "-" + name + " " + shellQuote(value)
}

// shellQuote quotes text for a POSIX shell only when it needs it.
func shellQuote(text string) string {
	safe := text != "" && !strings.ContainsFunc(text, func(character rune) bool {
		plain := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("_@%+=:,./-", character)

		return !plain
	})

	if safe {
		return text
	}

	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}
