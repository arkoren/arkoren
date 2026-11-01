package console

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
)

// ErrorReport contains actionable diagnostics without repeating the message
// across protocol title/detail fields. Optional context is omitted when absent.
type ErrorReport struct {
	// Message is the safe explanation of the failure.
	Message string `json:"message"`

	// Type is an advertised failure classification, when available.
	Type string `json:"type,omitzero"`

	// Status is an actual HTTP failure Status, never a tool classification.
	Status int `json:"status,omitzero"`

	// Fields contains validation messages keyed by JSON pointer.
	Fields map[string][]string `json:"fields,omitzero"`

	// Sources maps validation pointers to the supplying command-line argument.
	Sources map[string]string `json:"sources,omitzero"`

	// Details contains safe nonvalidation tool context.
	Details jsontext.Value `json:"details,omitzero"`

	// Hint suggests the next useful action.
	Hint string `json:"hint,omitzero"`

	// Destination is the local directory whose existing files blocked an action.
	Destination string `json:"destination,omitzero"`

	// ConflictCount counts all local conflicts, including undisplayed ones.
	ConflictCount int `json:"conflict_count,omitzero"`

	// Conflicts contains complete paths only when verbose output is requested.
	Conflicts []string `json:"conflicts,omitzero"`

	// Truncated reports abbreviated messages or validation context.
	Truncated bool `json:"truncated,omitzero"`

	// OmittedFields counts validation pointers excluded from the default view.
	OmittedFields int `json:"omitted_fields,omitzero"`

	// DetailsOmitted reports safe tool context available with --verbose.
	DetailsOmitted bool `json:"details_omitted,omitzero"`
}

// Result writes one local result using the remote result presentation policy.
func (env Environment) Result(value any) error {
	return WriteJSON(env.Stdout, value, env.JSONStyle())
}

// DiagnosticStyle uses the same JSON policy as results, but checks stderr's
// terminal state so redirecting either stream does not style the other one.
func (env Environment) DiagnosticStyle() Style {
	env.IsTerminal = env.ErrorTerminal

	return env.JSONStyle()
}

// JSONStyle applies formatting without resolving inspector configuration.
func (env Environment) JSONStyle() Style {
	color := env.IsTerminal && !env.Presentation.NoColor

	if color && env.Env != nil {
		value, _ := env.Env("NO_COLOR")
		color = value == ""
	}

	return Style{Indent: !env.Presentation.Compact, Color: color}
}

// WriteJSON encodes before writing and checks short writes on both boundaries.
func WriteJSON(out io.Writer, value any, presentation Style) error {
	encoded, err := json.Marshal(value, json.Deterministic(true))

	if err != nil {
		return fmt.Errorf("encode output: %w", err)
	}

	output := &Output{Writer: out}

	if _, err := output.Write(Render(encoded, presentation)); err != nil {
		return err
	}

	_, err = output.Write([]byte("\n"))

	return err
}

// Output retains the first stdout failure for run to report after the
// command returns. This boundary covers direct text, JSON, tabwriter flushes,
// and follow callbacks alike, without making rendering code own exit policy.
// It does not buffer or retry writes, and suppresses writes after a failure.
type Output struct {
	// Writer receives successful writes with the original byte boundaries.
	Writer io.Writer
	// Err retains the first failure, including a short write without an error.
	Err error
}

// Write forwards data until the first failure and normalizes short writes to
// io.ErrShortWrite. Returning the retained error also stops follow callbacks
// immediately; run catches failures from renderers that do not return errors.
func (output *Output) Write(data []byte) (int, error) {
	if output.Err != nil {
		return 0, output.Err
	}

	written, err := output.Writer.Write(data)

	if written < len(data) && err == nil {
		err = io.ErrShortWrite
	}

	output.Err = err

	return written, err
}
