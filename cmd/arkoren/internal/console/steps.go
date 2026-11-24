package console

import (
	"slices"
	"strings"
)

// The closed list of error types. Every error document carries exactly one in
// error.type, so scripts and agents branch on the cause without parsing the
// message. A remote tool may report types of its own, which pass through; the
// CLI never invents one outside this list.
const (
	// TypeUsage is a command line the CLI cannot accept: an unknown command,
	// a missing or surplus argument, or an unknown or malformed flag.
	TypeUsage = "usage"

	// TypeNotConfigured means the command needs a setting that is absent,
	// such as the inspector URL.
	TypeNotConfigured = "not_configured"

	// TypeUnreachable means the inspector did not answer.
	TypeUnreachable = "unreachable"

	// TypeUnauthorized means the inspector refused the credentials (HTTP 401).
	TypeUnauthorized = "unauthorized"

	// TypeInvalidInput is a request the server rejected as invalid.
	TypeInvalidInput = "invalid_input"

	// TypeFileConflict means existing files block a local action; nothing was
	// written.
	TypeFileConflict = "file_conflict"

	// TypeToolError is a failure reported by a remote tool or the protocol.
	TypeToolError = "tool_error"

	// TypeInternal is an unexpected local failure, such as an I/O error.
	TypeInternal = "internal"
)

// ExitCodeFor maps an error type to the process exit code: usage and
// not_configured are 2, unauthorized is 4 ([ExitAuth]) and every other type,
// including a tool-provided one, is 1.
func ExitCodeFor(errorType string) int {
	switch errorType {
	case TypeUsage, TypeNotConfigured:
		return ExitUsage
	case TypeUnauthorized:
		return ExitAuth
	}

	return ExitFailure
}

// Step is one thing the reader can do next. Every list of steps is the
// top-level "next_steps" member of a document, on successes and errors alike.
type Step struct {
	// Text is one imperative sentence, such as "Enter the project".
	Text string `json:"text"`

	// Command is a single exact, pasteable command line that performs the
	// step. It is omitted when the step needs judgment, several commands or a
	// value only the reader has.
	Command string `json:"command,omitzero"`
}

// Warning is something the reader should know although the command succeeded,
// listed in the top-level "warnings" member and printed as "! message".
type Warning struct {
	// Message is one sentence.
	Message string `json:"message"`

	// Details are further sentences or facts, printed as bullets.
	Details []string `json:"details,omitzero"`
}

// Problem is one finding of a check: a broken layout rule, an invalid file or
// a stale skill. A check lists them in "problems", an empty array when ok.
type Problem struct {
	// Rule names the broken rule of a rule-based check, such as a layout rule.
	Rule string `json:"rule,omitzero"`

	// Field names what is wrong when there is no rule: a flag, argument or
	// skill name.
	Field string `json:"field,omitzero"`

	// Path is the file the problem is in, when there is one.
	Path string `json:"path,omitzero"`

	// Line is the 1-based line in Path, when known.
	Line int `json:"line,omitzero"`

	// Message explains the problem.
	Message string `json:"message"`
}

// ErrorDocument is what the CLI writes to stderr for a failure: the error and,
// at the top level beside it, the steps that resolve it.
type ErrorDocument struct {
	// Error describes the failure.
	Error ErrorReport `json:"error"`

	// NextSteps resolves the failure; it is omitted when nothing helps.
	NextSteps []Step `json:"next_steps,omitzero"`
}

// tokenPlaceholder replaces a bearer token in a rendered command line, so a
// credential typed once is never printed again.
const tokenPlaceholder = `"$ARKOREN_MCP_TOKEN"`

// Command renders an invocation of this CLI as one pasteable command line:
// the program name, then the arguments quoted where a shell needs it, then
// extra. The value of a token flag, whether "--token VALUE" or "--token=VALUE",
// is replaced by "$ARKOREN_MCP_TOKEN".
func Command(args []string, extra ...string) string {
	parts := []string{"arkoren"}
	redact := false

	for _, arg := range slices.Concat(args, extra) {
		name, _, hasValue := strings.Cut(arg, "=")
		isToken := name == "--token" || name == "-token"

		switch {
		case redact:
			parts = append(parts, tokenPlaceholder)
		case isToken && hasValue:
			parts = append(parts, name+"="+tokenPlaceholder)
		default:
			parts = append(parts, Quote(arg))
		}

		redact = isToken && !hasValue
	}

	return strings.Join(parts, " ")
}

// Quote returns arg as one shell word: unchanged when it holds only characters
// a shell leaves alone, single-quoted otherwise. Use it for every value
// interpolated into a step's command.
func Quote(arg string) string {
	plain := func(char rune) bool {
		return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			strings.ContainsRune("_@%+=:,./-", char)
	}

	if arg != "" && !strings.ContainsFunc(arg, func(char rune) bool { return !plain(char) }) {
		return arg
	}

	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// VerboseStep is the step that repeats the current command with --verbose.
// With a known invocation it carries the exact command line; without one
// (a caller that supplied no arguments) it is a text-only step.
func VerboseStep(text string, invocation []string) Step {
	if len(invocation) == 0 {
		return Step{Text: text}
	}

	return Step{Text: text, Command: Command(invocation, "--verbose")}
}
