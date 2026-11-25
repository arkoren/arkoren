package remote

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// TestLargeToolDiagnosticsRequireVerbose bounds messages, validation lists, and
// incidental context while retaining their complete safe originals on request.
func TestLargeToolDiagnosticsRequireVerbose(t *testing.T) {
	t.Parallel()

	failure := &APIError{
		Title:   strings.Repeat("message ", 1000),
		Fields:  make(map[string][]string),
		Details: []byte(`{"context":"` + strings.Repeat("safe ", 1000) + `"}`),
	}

	for index := range 10 {
		failure.Fields[fmt.Sprintf("/field-%02d", index)] = []string{strings.Repeat("invalid ", 100), "another error"}
	}

	brief := DescribeError(failure, false, nil).Error

	if len([]rune(brief.Message)) > console.MaxDiagnosticRunes+1 || len(brief.Fields) != console.MaxValidationFields || brief.OmittedFields != 7 {
		t.Fatalf("diagnostic was not bounded: %+v", brief)
	}

	if !brief.Truncated || !brief.DetailsOmitted || len(brief.Details) != 0 {
		t.Fatal("omitted context was not identified")
	}

	full := DescribeError(failure, true, nil).Error

	if full.Message != failure.Title || len(full.Fields) != 10 || len(full.Details) == 0 || full.Truncated {
		t.Fatal("verbose diagnostic lost original safe data")
	}

	if len(failure.Fields["/field-00"]) != 2 {
		t.Fatal("summarizing mutated the original failure")
	}
}

// TestCompactContextStillBoundsLongArrays catches lists whose compact bytes
// look small but whose pretty-printed form would dominate the diagnostic.
func TestCompactContextStillBoundsLongArrays(t *testing.T) {
	t.Parallel()

	failure := &APIError{Title: "failed", Details: []byte(`{"items":[` + strings.Repeat("0,", 99) + `0]}`)}

	if !DescribeError(failure, false, nil).Error.DetailsOmitted {
		t.Fatal("default diagnostic dumped a long compact array")
	}

	if len(DescribeError(failure, true, nil).Error.Details) == 0 {
		t.Fatal("verbose diagnostic lost context")
	}
}

// errorType is the type DescribeError assigns to err.
func errorType(err error) string {
	return DescribeError(err, false, nil).Error.Type
}

// TestErrorTypeOfLocalFailures is internal for the unexpected and the type the
// failure states for the rest.
func TestErrorTypeOfLocalFailures(t *testing.T) {
	t.Parallel()

	if got := errorType(errors.New("disk full")); got != console.TypeInternal {
		t.Fatalf("plain error type = %q", got)
	}

	if got := errorType(console.UsageError("bad flag")); got != console.TypeUsage {
		t.Fatalf("usage type = %q", got)
	}

	if got := errorType(console.Fail(console.TypeNotConfigured, nil, "no url")); got != console.TypeNotConfigured {
		t.Fatalf("not configured type = %q", got)
	}

	if got := errorType(console.ConflictingPaths("create project", "dir", []string{"go.mod"})); got != console.TypeFileConflict {
		t.Fatalf("conflict type = %q", got)
	}
}

// TestErrorTypeOfRemoteFailures keeps the type a tool advertised, and maps what
// it did not or must not decide.
func TestErrorTypeOfRemoteFailures(t *testing.T) {
	t.Parallel()

	if got := errorType(&unreachableError{url: "http://x", err: errors.New("refused")}); got != console.TypeUnreachable {
		t.Fatalf("unreachable type = %q", got)
	}

	if got := errorType(&APIError{Status: 401, Title: "nope"}); got != console.TypeUnauthorized {
		t.Fatalf("401 type = %q", got)
	}

	if got := errorType(&APIError{Title: "boom"}); got != console.TypeToolError {
		t.Fatalf("untyped tool failure = %q", got)
	}

	if got := errorType(&APIError{Title: "late", Type: "payment_declined"}); got != "payment_declined" {
		t.Fatalf("advertised type = %q", got)
	}
}

// TestToolTypesCannotChangeTheExitStatus maps the advertised types that decide
// an exit code to tool_error, so only the CLI's own conditions exit 2 or 4.
func TestToolTypesCannotChangeTheExitStatus(t *testing.T) {
	t.Parallel()

	for _, advertised := range []string{console.TypeUsage, console.TypeNotConfigured, console.TypeUnauthorized} {
		got := errorType(&APIError{Title: "x", Type: advertised})

		if got != console.TypeToolError || console.ExitCodeFor(got) != console.ExitFailure {
			t.Fatalf("advertised %q became %q", advertised, got)
		}
	}
}

// TestDescribeErrorStepsResolveTheFailure checks the remedies, which live in
// steps and never in the message.
func TestDescribeErrorStepsResolveTheFailure(t *testing.T) {
	t.Parallel()

	unauthorized := DescribeError(&APIError{Status: 401, Title: "unauthorized"}, false, nil)

	if len(unauthorized.NextSteps) != 1 || !strings.Contains(unauthorized.NextSteps[0].Text, "--token") || strings.Contains(unauthorized.Error.Message, "--token") {
		t.Fatalf("unauthorized = %+v", unauthorized)
	}

	invalid := DescribeError(&APIError{Type: console.TypeInvalidInput, Title: "bad", operation: "billing:pay"}, false, nil)

	if len(invalid.NextSteps) != 1 || invalid.NextSteps[0].Command != "arkoren billing:pay --help" {
		t.Fatalf("invalid input = %+v", invalid)
	}

	unreachable := DescribeError(&unreachableError{url: "http://x", err: errors.New("refused")}, false, nil)

	if len(unreachable.NextSteps) != 3 {
		t.Fatalf("unreachable steps = %+v", unreachable.NextSteps)
	}

	for _, step := range unreachable.NextSteps {
		if step.Command != "" {
			t.Fatalf("unreachable step %q has a command", step.Text)
		}
	}
}

// TestDescribeErrorOffersVerboseAsAStep turns "--verbose for more" into a step
// whose command is the real invocation, and omits it once verbose.
func TestDescribeErrorOffersVerboseAsAStep(t *testing.T) {
	t.Parallel()

	failure := &APIError{Title: strings.Repeat("message ", 1000)}
	invocation := []string{"billing:pay", "INV", "--token", "secret"}

	brief := DescribeError(failure, false, invocation)
	last := brief.NextSteps[len(brief.NextSteps)-1]

	if last.Command != `arkoren billing:pay INV --token "$ARKOREN_MCP_TOKEN" --verbose` {
		t.Fatalf("verbose step = %+v", last)
	}

	if full := DescribeError(failure, true, invocation); len(full.NextSteps) != 0 {
		t.Fatalf("verbose output still offers --verbose: %+v", full.NextSteps)
	}

	if bare := DescribeError(failure, false, nil); len(bare.NextSteps) != 1 || bare.NextSteps[0].Command != "" {
		t.Fatalf("a text-only step is expected without an invocation: %+v", bare.NextSteps)
	}
}

// TestDescribeConflictOffersEveryPathAsAStep lists the paths on request.
func TestDescribeConflictOffersEveryPathAsAStep(t *testing.T) {
	t.Parallel()

	err := console.ConflictingPaths("update skill", "dir", []string{"a", "b"})
	brief := DescribeError(err, false, []string{"skills", "update"})

	if len(brief.NextSteps) != 2 || brief.NextSteps[1].Command != "arkoren skills update --verbose" || len(brief.Error.Conflicts) != 0 {
		t.Fatalf("brief = %+v", brief)
	}

	if full := DescribeError(err, true, nil); len(full.Error.Conflicts) != 2 || len(full.NextSteps) != 1 {
		t.Fatalf("full = %+v", full)
	}
}

// TestRemoteFailureClassifiesUnknownFailures marks protocol failures as tool
// errors and leaves already typed ones alone.
func TestRemoteFailureClassifiesUnknownFailures(t *testing.T) {
	t.Parallel()

	if ToolFailure(nil) != nil {
		t.Fatal("nil became an error")
	}

	if got := errorType(ToolFailure(errors.New("decode raw tool result"))); got != console.TypeToolError {
		t.Fatalf("protocol failure type = %q", got)
	}

	usage := console.UsageError("bad")

	if !errors.Is(ToolFailure(usage), usage) {
		t.Fatal("a typed failure was rewrapped")
	}

	silent := &console.ExitError{Code: console.ExitFailure}

	if !errors.Is(ToolFailure(silent), silent) {
		t.Fatal("a silent verdict was rewrapped")
	}
}
