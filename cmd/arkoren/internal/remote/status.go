package remote

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// SummarizeHealth keeps aggregate status useful without printing every check.
// The health role is explicit metadata; ordinary tool results are untouched.
func SummarizeHealth(raw jsontext.Value, verbose bool) jsontext.Value {
	if verbose {
		return raw
	}

	var fields map[string]jsontext.Value

	if err := json.Unmarshal(raw, &fields); err != nil || len(fields["status"]) == 0 {
		if console.ConciseContext(raw) {
			return raw
		}

		return jsontext.Value(`{"details_omitted":true}`)
	}

	summary := make(map[string]any)

	for _, name := range []string{"status", "reason", "checked_at"} {
		if value := fields[name]; len(value) != 0 {
			var text string

			if err := json.Unmarshal(value, &text); err != nil {
				continue
			}

			text, truncated := console.DiagnosticText(text)
			summary[name] = text

			if truncated {
				summary["truncated"] = true
			}
		}
	}

	if len(summary) == 0 {
		return jsontext.Value(`{"details_omitted":true}`)
	}

	if checks := fields["items"]; checks.Kind() == '[' {
		addCheckCounts(summary, checks)
	}

	if checks := fields["checks"]; checks.Kind() == '[' {
		addCheckCounts(summary, checks)
	}

	encoded, err := json.Marshal(summary, json.Deterministic(true))

	if err != nil {
		return jsontext.Value(`{"details_omitted":true}`)
	}

	return encoded
}

// addCheckCounts projects a health check list onto bounded aggregate counts.
// Unknown check statuses are counted in the total but never guessed as failures.
func addCheckCounts(summary map[string]any, raw jsontext.Value) {
	var checks []struct {
		// Status is the advertised outcome of this cached check.
		Status string `json:"status"`
	}

	if err := json.Unmarshal(raw, &checks); err != nil {
		return
	}

	failing, pending := 0, 0

	for _, check := range checks {
		switch check.Status {
		case "failing", "failed":
			failing++
		case "pending":
			pending++
		}
	}

	summary["check_count"] = len(checks)
	summary["failing_count"], summary["pending_count"] = failing, pending
}
