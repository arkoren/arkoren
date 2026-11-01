package remote

import (
	"bytes"
	"strings"
	"testing"
)

// TestKebabFlagBuildsSnakeCaseInputAndPreservesOrigin checks alias normalization.
// JSON uses the schema spelling while diagnostics retain the typed flag name.
func TestKebabFlagBuildsSnakeCaseInputAndPreservesOrigin(t *testing.T) {
	t.Parallel()

	root := ParseSchema([]byte(`{"type":"object","properties":{"fail_fast":{"type":"boolean"}}}`))

	args, err := parseOpArgs(root, "custom:check", []string{"--fail-fast"})

	if err != nil {
		t.Fatal(err)
	}

	input, err := buildInput(root, nil, args, "", strings.NewReader(""))

	if err != nil {
		t.Fatal(err)
	}

	if string(input.body) != `{"fail_fast":true}` || len(input.origins) != 1 {
		t.Fatalf("input = %+v", input)
	}

	if input.origins[0].pointer != "/fail_fast" || input.origins[0].label != "-fail-fast" {
		t.Fatalf("origin = %+v", input.origins[0])
	}
}

// TestKebabFlagPrefersExactProperty checks ambiguous property spellings.
// A literal kebab-case property takes precedence over a snake_case alias.
func TestKebabFlagPrefersExactProperty(t *testing.T) {
	t.Parallel()

	root := ParseSchema([]byte(`{"type":"object","properties":{"fail_fast":{"type":"boolean"},"fail-fast":{"type":"string"}}}`))

	path, leaf, err := root.flagPath("fail-fast")

	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(path, ".") != "fail-fast" || leaf.kind() != "string" {
		t.Fatalf("path = %v, leaf = %+v", path, leaf)
	}
}

// TestKebabFlagLeavesMapKeysLiteral checks dynamic-key spelling.
// Alias normalization applies to declared properties, not arbitrary map keys.
func TestKebabFlagLeavesMapKeysLiteral(t *testing.T) {
	t.Parallel()

	root := ParseSchema([]byte(`{"type":"object","properties":{"labels":{"type":"object","additionalProperties":{"type":"string"}}}}`))

	path, _, err := root.flagPath("labels.connection-name")

	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(path, ".") != "labels.connection-name" {
		t.Fatalf("path = %v", path)
	}
}

// TestKebabFlagResolvesNestedProperties checks each segment of a nested path.
// Every declared snake_case property may use its kebab-case alias.
func TestKebabFlagResolvesNestedProperties(t *testing.T) {
	t.Parallel()

	root := ParseSchema([]byte(`{"type":"object","properties":{"health_policy":{"type":"object","properties":{"fail_fast":{"type":"boolean"}}}}}`))

	path, _, err := root.flagPath("health-policy.fail-fast")

	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(path, ".") != "health_policy.fail_fast" {
		t.Fatalf("path = %v", path)
	}
}

// TestOperationHelpShowsKebabAlias checks discoverability of input aliases.
// Help must show the schema spelling and its usable kebab-case form together.
func TestOperationHelpShowsKebabAlias(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	WriteOperationHelp(&output, "custom", &Operation{
		Name:  "check",
		Kind:  "read",
		Input: []byte(`{"type":"object","properties":{"fail_fast":{"type":"boolean"}}}`),
	}, false)

	if !strings.Contains(output.String(), "-fail_fast, -fail-fast") {
		t.Fatalf("help = %s", output.String())
	}
}

// TestOperationHelpExplainsMissingOutputSchema distinguishes absent metadata
// from an actual JSON schema without printing protocol nulls in the help page.
func TestOperationHelpExplainsMissingOutputSchema(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	WriteOperationHelp(&output, "custom", &Operation{
		Name: "check", Kind: "read", Input: []byte(`{"type":"object"}`), Output: []byte("null"),
	}, false)

	contains(t, output.String(), "Output (JSON Schema):\n  not advertised")
}

// contains fails the test unless text holds every fragment.
func contains(t *testing.T, text string, fragments ...string) {
	t.Helper()

	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Fatalf("output lacks %q:\n%s", fragment, text)
		}
	}
}
