package console

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Default diagnostic limits keep incidental server context out of ordinary
// command output. Verbose diagnostics retain the complete safe original data.
const (
	MaxDiagnosticRunes    = 320
	MaxValidationFields   = 3
	maxValidationMessages = 1
	maxPointerRunes       = 160
	maxContextBytes       = 512
	maxContextTokens      = 24
)

// ConflictError retains file conflicts as data rather than joining them into a
// potentially enormous message. Constructing it never changes the filesystem.
type ConflictError struct {
	// operation describes the attempted local action.
	operation string

	// destination identifies the selected project or skill directory.
	destination string

	// paths contains deterministic conflict descriptions, usually file paths.
	paths []string
}

// Error summarizes collections instead of joining every path into the message.
func (failure *ConflictError) Error() string {
	if len(failure.paths) == 1 {
		return fmt.Sprintf("cannot %s: %s; no files were written", failure.operation, failure.paths[0])
	}

	return fmt.Sprintf("cannot %s: %d conflicts; no files were written", failure.operation, len(failure.paths))
}

// ConflictingPaths returns the local usage failure without embedding its file
// list in text. Verbose reporting exposes the retained paths as a JSON array.
func ConflictingPaths(operation, destination string, paths []string) error {
	return &ExitError{Code: ExitUsage, Err: &ConflictError{
		operation: operation, destination: destination, paths: slices.Clone(paths),
	}}
}

// Report selects either a short conflict summary or the complete structured
// path list. The original message is identical in both modes.
func (failure *ConflictError) Report(verbose bool) ErrorReport {
	report := ErrorReport{
		Message: failure.Error(), Type: "file_conflict", Destination: failure.destination,
		ConflictCount: len(failure.paths),
		Hint:          "preserve existing files before resolving conflicts",
	}

	if verbose {
		report.Conflicts = failure.paths
	} else {
		report.Hint += "; --verbose lists every conflict"
	}

	return report.Concise(verbose)
}

// Concise bounds incidental diagnostics without altering remote success data,
// raw exports, or the underlying error used for classification and exit codes.
func (report ErrorReport) Concise(verbose bool) ErrorReport {
	if verbose {
		return report
	}

	report.Message, report.Truncated = DiagnosticText(report.Message)
	var shortened bool

	report.Hint, shortened = DiagnosticText(report.Hint)
	report.Truncated = report.Truncated || shortened

	if len([]rune(report.Type)) > MaxDiagnosticRunes {
		report.Type = ""
		report.Truncated = true
	}

	if len(report.Details) > 0 && !ConciseContext(report.Details) {
		report.Details = nil
		report.DetailsOmitted = true
	}

	report = report.validationSummary()

	if (report.Truncated || report.DetailsOmitted) && !strings.Contains(report.Hint, "--verbose") {
		if report.Hint != "" {
			report.Hint += "; "
		}

		report.Hint += "--verbose includes full diagnostics"
	}

	return report
}

// ConciseContext permits small, useful failure context but rejects large lists
// even when compact JSON encodes them in only a few hundred bytes.
func ConciseContext(raw jsontext.Value) bool {
	if len(raw) > maxContextBytes {
		return false
	}

	decoder := jsontext.NewDecoder(bytes.NewReader(raw))

	for count := 0; decoder.PeekKind() != 0; count++ {
		if count == maxContextTokens {
			return false
		}

		if _, err := decoder.ReadToken(); err != nil {
			return false
		}
	}

	return true
}

// validationSummary retains a bounded, deterministic set of actionable field
// errors and matching origins. It does not mutate the original failure maps.
func (report ErrorReport) validationSummary() ErrorReport {
	if len(report.Fields) == 0 {
		return report
	}

	pointers := slices.Sorted(maps.Keys(report.Fields))
	fields := make(map[string][]string, min(len(pointers), MaxValidationFields))
	sources := make(map[string]string, min(len(pointers), MaxValidationFields))

	for _, pointer := range pointers {
		if len(fields) == MaxValidationFields || len([]rune(pointer)) > maxPointerRunes {
			report.OmittedFields++
			report.Truncated = true
			continue
		}

		messages := report.Fields[pointer]

		if len(messages) > maxValidationMessages {
			report.Truncated = true
			messages = messages[:maxValidationMessages]
		}

		for _, message := range messages {
			text, truncated := DiagnosticText(message)
			report.Truncated = report.Truncated || truncated
			fields[pointer] = append(fields[pointer], text)
		}

		if source, ok := report.Sources[pointer]; ok {
			sources[pointer] = source
		}
	}

	if len(fields) == 0 {
		fields, sources = nil, nil
	}

	report.Fields, report.Sources = fields, sources

	return report
}

// DiagnosticText abbreviates long messages at a rune boundary. Verbose mode
// bypasses this helper, so complete safe context remains available explicitly.
func DiagnosticText(text string) (string, bool) {
	runes := []rune(text)

	if len(runes) <= MaxDiagnosticRunes {
		return text, false
	}

	return string(runes[:MaxDiagnosticRunes]) + "…", true
}
