package remote

import (
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// TestLongToolDescriptionsAreOptIn bounds discovery summaries even when an
// external tool supplies an essay instead of a short description.
func TestLongToolDescriptionsAreOptIn(t *testing.T) {
	t.Parallel()

	description := strings.Repeat("Detailed explanation. ", 100)
	manifest := &Catalog{Extensions: []Group{{Operations: []Operation{{
		Name: "external", NativeName: "external", Summary: description, Kind: "read",
		Input: []byte(`{"type":"object"}`),
	}}}}}

	brief := SummarizeTools(manifest, false)[0]
	full := SummarizeTools(manifest, true)[0]

	if !brief.DescriptionTruncated || len([]rune(brief.Description)) > console.MaxDiagnosticRunes+1 {
		t.Fatal("default catalog did not abbreviate the description")
	}

	if full.Description != description || full.DescriptionTruncated || len(full.InputSchema) == 0 {
		t.Fatal("verbose catalog lost tool details")
	}
}

// TestWaitingReadRequiresSchemaDeclaration checks replay eligibility.
// Only a read with a declared boolean wait property and true input qualifies.
func TestWaitingReadRequiresSchemaDeclaration(t *testing.T) {
	t.Parallel()

	operation := &Operation{
		Kind: "read",
	}

	if WaitingRead(operation, ParseSchema([]byte(`{"type":"object"}`)), []byte(`{"wait":true}`)) {
		t.Fatal("undeclared wait enabled waiting")
	}

	root := ParseSchema([]byte(`{"type":"object","properties":{"wait":{"type":"boolean"}}}`))

	if !WaitingRead(operation, root, []byte(`{"wait":true}`)) {
		t.Fatal("declared wait was ignored")
	}

	if WaitingRead(operation, root, []byte(`{"wait":false}`)) {
		t.Fatal("false enabled waiting")
	}

	operation.Kind = "action"

	if WaitingRead(operation, root, []byte(`{"wait":true}`)) {
		t.Fatal("action enabled the read convention")
	}
}
