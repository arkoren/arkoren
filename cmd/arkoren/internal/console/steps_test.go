package console_test

import (
	"errors"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// TestExitCodeFollowsErrorType pins the documented table: usage and
// not_configured exit 2, unauthorized exits 4, everything else exits 1.
func TestExitCodeFollowsErrorType(t *testing.T) {
	t.Parallel()

	want := map[string]int{
		console.TypeUsage:         console.ExitUsage,
		console.TypeNotConfigured: console.ExitUsage,
		console.TypeUnauthorized:  console.ExitAuth,
		console.TypeUnreachable:   console.ExitFailure,
		console.TypeInvalidInput:  console.ExitFailure,
		console.TypeFileConflict:  console.ExitFailure,
		console.TypeToolError:     console.ExitFailure,
		console.TypeInternal:      console.ExitFailure,
		"a_type_a_tool_invented":  console.ExitFailure,
	}

	for errorType, code := range want {
		if got := console.ExitCodeFor(errorType); got != code {
			t.Fatalf("ExitCodeFor(%q) = %d, want %d", errorType, got, code)
		}
	}
}

// TestFailCarriesTypeStepsAndCode builds the failure every command returns.
func TestFailCarriesTypeStepsAndCode(t *testing.T) {
	t.Parallel()

	steps := []console.Step{{Text: "Do it", Command: "arkoren do"}}
	err := console.Fail(console.TypeNotConfigured, steps, "missing %s", "thing")

	exit, ok := errors.AsType[*console.ExitError](err)

	if !ok || exit.Type != console.TypeNotConfigured || exit.Code != console.ExitUsage || len(exit.Steps) != 1 || err.Error() != "missing thing" {
		t.Fatalf("failure = %+v (%v)", exit, err)
	}
}

// TestUsageErrorIsTypedUsage exits 2 without steps of its own.
func TestUsageErrorIsTypedUsage(t *testing.T) {
	t.Parallel()

	exit, ok := errors.AsType[*console.ExitError](console.UsageError("bad %s", "flag"))

	if !ok || exit.Type != console.TypeUsage || exit.Code != console.ExitUsage || len(exit.Steps) != 0 {
		t.Fatalf("usage failure = %+v", exit)
	}
}

// TestCommandQuotesArguments quotes what a shell would split and leaves plain
// words alone.
func TestCommandQuotesArguments(t *testing.T) {
	t.Parallel()

	got := console.Command([]string{"key", "generate", "shared", "--out", "my key.pem", "it's"}, "--verbose")

	if got != `arkoren key generate shared --out 'my key.pem' 'it'\''s' --verbose` {
		t.Fatalf("command = %s", got)
	}
}

// TestCommandRedactsTheToken never prints a credential again, in either flag
// spelling.
func TestCommandRedactsTheToken(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--token", "secret-value", "status"},
		{"-token", "secret-value", "status"},
		{"--token=secret-value", "status"},
	} {
		got := console.Command(args)

		if strings.Contains(got, "secret-value") || !strings.Contains(got, `"$ARKOREN_MCP_TOKEN"`) || !strings.HasSuffix(got, " status") {
			t.Fatalf("command = %s", got)
		}
	}
}

// TestVerboseStepRepeatsTheInvocation carries the real command line with
// --verbose added, and is text-only when the invocation is unknown.
func TestVerboseStepRepeatsTheInvocation(t *testing.T) {
	t.Parallel()

	step := console.VerboseStep("Show everything", []string{"skills", "update"})

	if step.Text != "Show everything" || step.Command != "arkoren skills update --verbose" {
		t.Fatalf("step = %+v", step)
	}

	if bare := console.VerboseStep("Show everything", nil); bare.Command != "" {
		t.Fatalf("step without an invocation has a command: %+v", bare)
	}
}

// TestTextKeepsAMidPathDirectory shortens a path only where the working
// directory starts it: "/other/nowhere/x/shop" is not "/nowhere/x/shop" shortened.
func TestTextKeepsAMidPathDirectory(t *testing.T) {
	t.Parallel()

	got := string(console.Format([]byte(`{"destination":"/other/nowhere/x/shop","inside":"/nowhere/x/shop","message":"wrote /nowhere/x/a.go and /nowhere/x/b.go"}`),
		console.Style{Text: true, Dir: "/nowhere/x"}))

	for _, want := range []string{"Destination  /other/nowhere/x/shop", "Inside       shop", "Message      wrote a.go and b.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output lacks %q:\n%s", want, got)
		}
	}
}
