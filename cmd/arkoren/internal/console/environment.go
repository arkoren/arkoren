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

// DefaultTimeout bounds each non-waiting request without -request-timeout.
const DefaultTimeout = 10 * time.Second

// Environment supplies process state without consulting global streams or
// configuration. Commands retain caller ownership of the injected streams.
type Environment struct {
	// Stdin is read by "-input -".
	Stdin io.Reader

	// Stdout receives results and help; Stderr receives errors and hints.
	Stdout, Stderr io.Writer

	// Env looks up an environment variable.
	Env func(string) (string, bool)

	// Dir is the working directory: where ./.env and relative @file paths are
	// read from.
	Dir string

	// IsTerminal reports that Stdout is a terminal, which enables color
	// unless NO_COLOR or -no-color say otherwise.
	IsTerminal bool

	// ErrorTerminal reports that Stderr is a terminal independently of Stdout.
	ErrorTerminal bool

	// InputTerminal reports that Stdin is a terminal. Local project creation
	// prompts only with terminal input, independently of output coloring.
	InputTerminal bool

	// Presentation carries formatting options for local results.
	Presentation Globals
}

// Globals holds the global flags of one run.
type Globals struct {
	// URL and Token are explicit flag values, before environment resolution.
	URL, Token string

	// EnvFile is nil unless -env-file was given, including an empty value.
	EnvFile *string

	// Timeout bounds each non-streaming request.
	Timeout time.Duration

	// TimeoutSet distinguishes an explicit deadline from the ordinary default.
	TimeoutSet bool

	// Compact, NoColor, Follow, and Help are the boolean flags.
	Compact, NoColor, Follow, Help bool

	// Verbose includes complete diagnostic context and local file lists.
	Verbose bool
}

// ExitError ends the run with a chosen code. A nil err means everything was
// already printed (a failing health report).
type ExitError struct {
	// Code selects the process status after any diagnostic is printed.
	Code int

	// Err is the diagnostic cause; nil suppresses additional error output.
	Err error
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

// UsageError returns an error that exits with [ExitUsage].
func UsageError(format string, args ...any) error {
	return &ExitError{
		Code: ExitUsage,
		Err:  fmt.Errorf(format, args...),
	}
}
