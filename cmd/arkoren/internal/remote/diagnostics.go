package remote

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// DescribeError projects safe failures onto the CLI error document: the error
// with its always-present type, and the steps that resolve it. Validation
// fields replace their duplicate representation in tool details. invocation is
// the command line as typed, used to offer the same command with --verbose when
// the default view left out context; nil makes that step text-only.
//
// The type comes from the failure: an [console.ExitError] states its own, a
// file conflict is file_conflict, a remote failure keeps the type the server
// advertised (tool_error when it sent none), HTTP 401 is unauthorized, an
// unanswered request is unreachable, and anything else is internal.
func DescribeError(err error, verbose bool, invocation []string) console.ErrorDocument {
	report := console.ErrorReport{Message: err.Error(), Type: console.TypeInternal}
	var steps []console.Step

	if exit, ok := errors.AsType[*console.ExitError](err); ok && exit.Type != "" {
		report.Type, steps = exit.Type, exit.Steps
	}

	if conflict, ok := errors.AsType[*console.ConflictError](err); ok {
		report = conflict.Report(verbose)
		steps = []console.Step{{Text: "Move or remove the conflicting files, then run the command again; existing files are never overwritten"}}

		if !verbose {
			steps = append(steps, console.VerboseStep("List every conflicting path", invocation))
		}

		return console.ErrorDocument{Error: report, NextSteps: steps}
	}

	if failure, ok := errors.AsType[*APIError](err); ok {
		report, steps = describeAPIError(failure)
		report.Message = err.Error()
	}

	if _, ok := errors.AsType[*unreachableError](err); ok {
		report.Type, steps = console.TypeUnreachable, UnreachableSteps()
	}

	report = report.Concise(verbose)

	if !verbose && (report.Truncated || report.DetailsOmitted) {
		steps = append(steps, console.VerboseStep("Show the full diagnostics", invocation))
	}

	return console.ErrorDocument{Error: report, NextSteps: steps}
}

// ToolFailure marks a failure of the remote path as a tool error unless it
// already carries a type of its own: an [console.ExitError] (usage, conflict,
// not_configured and the silent verdicts), a remote [APIError] or an
// unanswered request. Local commands do not use it; their unclassified
// failures stay internal.
func ToolFailure(err error) error {
	if err == nil {
		return nil
	}

	if _, ok := errors.AsType[*console.ExitError](err); ok {
		return err
	}

	if _, ok := errors.AsType[*APIError](err); ok {
		return err
	}

	if _, ok := errors.AsType[*unreachableError](err); ok {
		return err
	}

	return console.Fail(console.TypeToolError, nil, "%w", err)
}

// describeAPIError reports a remote failure. Its type is the advertised one,
// tool_error without it, or unauthorized for HTTP 401, and the steps are the
// remedy for the types the CLI can advise on.
func describeAPIError(failure *APIError) (console.ErrorReport, []console.Step) {
	report := console.ErrorReport{
		Message: failure.Error(),
		Type:    toolType(failure.Type),
		Status:  failure.Status,
		Details: errorContext(failure),
	}

	if len(failure.Fields) > 0 {
		report.Fields = failure.Fields
		labels := make(map[string]string, len(failure.Fields))

		for pointer := range failure.Fields {
			labels[pointer] = failure.label(pointer)
		}

		report.Sources = labels
	}

	switch {
	case failure.Status == 401:
		report.Type = console.TypeUnauthorized

		return report, []console.Step{{Text: "Pass --token, or set " + EnvToken + " in your shell or ./.env"}}
	case failure.Type == console.TypeInvalidInput && failure.operation != "":
		return report, []console.Step{{Text: "See usage", Command: "arkoren " + failure.operation + " --help"}}
	}

	return report, nil
}

// toolType is the error type of a remote failure: the type the tool advertised,
// tool_error when it sent none, and also tool_error when it advertised a type
// that decides the CLI's exit status (usage, not_configured, unauthorized), so
// a tool's own vocabulary can never change how the CLI exits. Only an actual
// HTTP 401 is unauthorized.
func toolType(advertised string) string {
	switch advertised {
	case "", console.TypeUsage, console.TypeNotConfigured, console.TypeUnauthorized:
		return console.TypeToolError
	}

	return advertised
}

// UnreachableSteps are the remedies for an inspector that did not answer.
func UnreachableSteps() []console.Step {
	return []console.Step{
		{Text: "Start the admin server"},
		{Text: "Check --url or " + EnvURL},
		{Text: "Forward the admin port when the app runs on another machine"},
	}
}

// NoURLSteps are the remedies for a command that needs an inspector URL but
// has none: the flag, or the environment variable in the shell or ./.env.
func NoURLSteps() []console.Step {
	return []console.Step{
		{Text: "Pass the endpoint with --url"},
		{Text: "Or set " + EnvURL + " in your shell or ./.env"},
	}
}

// label names what set the value at a JSON Pointer of the request: the flag
// or argument that set it, the flag with the remaining pointer when it set a
// larger JSON value, or the bare pointer for a value that came from --input.
func (failure *APIError) label(pointer string) string {
	for _, source := range slices.Backward(failure.origins) {
		switch {
		case source.pointer == pointer:
			return source.label
		case strings.HasPrefix(pointer, source.pointer+"/"):
			return source.label + " (at " + strings.TrimPrefix(pointer, source.pointer) + ")"
		}
	}

	if pointer == "" {
		return "input"
	}

	return pointer
}

// errorContext removes duplicate validation fields while retaining any other
// safe context and exact JSON number representations from the tool failure.
func errorContext(failure *APIError) jsontext.Value {
	if len(failure.Details) == 0 || failure.Details.Kind() == 'n' {
		return nil
	}

	if len(failure.Fields) == 0 {
		return failure.Details
	}

	var members map[string]jsontext.Value

	if err := json.Unmarshal(failure.Details, &members); err != nil {
		return failure.Details
	}

	delete(members, "fields")

	if len(members) == 0 {
		return nil
	}

	encoded, err := json.Marshal(members, json.Deterministic(true))

	if err != nil {
		return failure.Details
	}

	return encoded
}
