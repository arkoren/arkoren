package remote

import (
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

	brief := DescribeError(failure, false)

	if len([]rune(brief.Message)) > console.MaxDiagnosticRunes+1 || len(brief.Fields) != console.MaxValidationFields || brief.OmittedFields != 7 {
		t.Fatalf("diagnostic was not bounded: %+v", brief)
	}

	if !brief.Truncated || !brief.DetailsOmitted || len(brief.Details) != 0 {
		t.Fatal("omitted context was not identified")
	}

	full := DescribeError(failure, true)

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

	if !DescribeError(failure, false).DetailsOmitted {
		t.Fatal("default diagnostic dumped a long compact array")
	}

	if len(DescribeError(failure, true).Details) == 0 {
		t.Fatal("verbose diagnostic lost context")
	}
}
