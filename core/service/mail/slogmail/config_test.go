package slogmail

import (
	"encoding/json/v2"
	"testing"
)

// TestDetailsJSONUsesStrings keeps logging configuration human-readable and
// stable even if additional verbosity levels are introduced later.
func TestDetailsJSONUsesStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]Detail{"", DetailSummary, DetailBody})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["","summary","body"]` {
		t.Fatalf("details JSON = %s", data)
	}
}

// TestEmptyDetailDefaultsToSummary checks explicit empty configuration as well
// as omission, so an empty config never enables sensitive body logging.
func TestEmptyDetailDefaultsToSummary(t *testing.T) {
	t.Parallel()

	config := newSettings(func(mailer *Builder) {
		mailer.Detail("")
	})

	if config.Detail != DetailSummary || newSettings().Detail != DetailSummary {
		t.Fatalf("default detail = %q", config.Detail)
	}
}

// TestUnknownDetailPreserved ensures construction does not reinterpret unknown
// values as body logging. Send enables bodies only for the exact DetailBody.
func TestUnknownDetailPreserved(t *testing.T) {
	t.Parallel()

	config := newSettings(func(mailer *Builder) {
		mailer.Detail("unknown")
	})

	if config.Detail != "unknown" {
		t.Fatalf("unknown detail = %q", config.Detail)
	}
}
