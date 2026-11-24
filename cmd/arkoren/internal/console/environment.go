// Package console owns the CLI's injected process environment and output policy.
package console

import (
	"fmt"
	"io"
	"time"
)

// Exit codes of the process; see the package documentation.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
	ExitAuth    = 4
)

// DefaultTimeout bounds each non-waiting request without --request-timeout.
const DefaultTimeout = 10 * time.Second

// Environment supplies process state without consulting global streams or
// configuration. Commands retain caller ownership of the injected streams.
type Environment struct {
	// Stdin is read by "--input -".
	Stdin io.Reader

	// Stdout receives results and help; Stderr receives errors and warnings.
	Stdout, Stderr io.Writer

	// Env looks up an environment variable.
	Env func(string) (string, bool)

	// Dir is the working directory: where ./.env and relative @file paths are
	// read from.
	Dir string

	// IsTerminal reports that Stdout is a terminal, which enables color
	// unless NO_COLOR or --no-color say otherwise.
	IsTerminal bool

	// ErrorTerminal reports that Stderr is a terminal independently of Stdout.
	ErrorTerminal bool

	// InputTerminal reports that Stdin is a terminal. Local project creation
	// prompts only with terminal input, independently of output coloring.
	InputTerminal bool

	// Presentation carries formatting options for local results.
	Presentation Globals

	// Now is the clock for relative times in text output; nil uses time.Now.
	Now func() time.Time

	// Invocation is the command line without the program name, as typed. It
	// lets a step repeat the real command, for example with --verbose; nil
	// makes such steps text-only.
	Invocation []string
}

// Globals holds the global flags of one run.
type Globals struct {
	// URL and Token are explicit flag values, before environment resolution.
	URL, Token string

	// EnvFile is nil unless --env-file was given, including an empty value.
	EnvFile *string

	// Timeout bounds each non-streaming request.
	Timeout time.Duration

	// TimeoutSet distinguishes an explicit deadline from the ordinary default.
	TimeoutSet bool

	// Compact, NoColor, Follow, and Help are the boolean flags.
	Compact, NoColor, Follow, Help bool

	// JSON prints results and errors as JSON even on a terminal.
	JSON bool

	// Verbose includes complete diagnostic context and local file lists.
	Verbose bool
}

// ExitError ends the run with a chosen code. A nil Err means everything was
// already printed (a failing health report or check).
type ExitError struct {
	// Code selects the process status after any diagnostic is printed. For a
	// failure with a Type it is [ExitCodeFor] of that type.
	Code int

	// Err is the diagnostic cause; nil suppresses additional error output.
	Err error

	// Type is the error type of the diagnostic, one of the Type constants. When
	// Err is nil it only records what the report already printed (a preview that
	// would fail with a file conflict) and may be empty.
	Type string

	// Steps resolve the failure and become the top-level next_steps of the
	// error document. A usage error without steps gets a "See usage" step for
	// the command that was run.
	Steps []Step
}

// Error implements error.
func (failure *ExitError) Error() string {
	if failure.Err == nil {
		return fmt.Sprintf("exit %d", failure.Code)
	}

	return failure.Err.Error()
}

// Unwrap returns the cause.
func (failure *ExitError) Unwrap() error {
	return failure.Err
}

// Fail returns a failure of the given type, with the steps that resolve it and
// the exit code of the type. The message is formatted like [fmt.Errorf], so %w
// keeps the cause available to errors.Is and errors.As.
func Fail(errorType string, steps []Step, format string, args ...any) error {
	return &ExitError{
		Code:  ExitCodeFor(errorType),
		Err:   fmt.Errorf(format, args...),
		Type:  errorType,
		Steps: steps,
	}
}

// UsageError returns a [TypeUsage] failure that exits with [ExitUsage]. The
// message must name the argument or flag at fault and, when the choice is
// closed, the accepted values; the run adds the "See usage" step.
func UsageError(format string, args ...any) error {
	return Fail(TypeUsage, nil, format, args...)
}
