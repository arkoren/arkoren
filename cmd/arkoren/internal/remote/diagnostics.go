package remote

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// DescribeError projects safe failures onto the CLI diagnostic contract.
// Validation fields replace their duplicate representation in tool details.
func DescribeError(err error, verbose bool) console.ErrorReport {
	if conflict, ok := errors.AsType[*console.ConflictError](err); ok {
		return conflict.Report(verbose)
	}

	diagnostic := console.ErrorReport{Message: err.Error()}

	if failure, ok := errors.AsType[*APIError](err); ok {
		diagnostic.Type = failure.Type
		diagnostic.Status = failure.Status

		if len(failure.Fields) > 0 {
			diagnostic.Fields = failure.Fields
			labels := make(map[string]string, len(failure.Fields))

			for pointer := range failure.Fields {
				labels[pointer] = failure.label(pointer)
			}

			diagnostic.Sources = labels
		}

		diagnostic.Details = errorContext(failure)

		switch {
		case failure.Status == 401:
			diagnostic.Hint = fmt.Sprintf("pass -token or set %s (in your shell or ./.env)", EnvToken)
		case failure.Type == "invalid_input" && failure.operation != "":
			diagnostic.Hint = fmt.Sprintf("run 'arkoren %s --help' for the flags and examples", failure.operation)
		}
	}

	if _, ok := errors.AsType[*unreachableError](err); ok {
		diagnostic.Type = "unreachable"
		diagnostic.Hint = "start the admin server or check -url / " + EnvURL + "; forward the admin port for a remote app"
	}

	return diagnostic.Concise(verbose)
}

// label names what set the value at a JSON Pointer of the request: the flag
// or argument that set it, the flag with the remaining pointer when it set a
// larger JSON value, or the bare pointer for a value that came from -input.
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
