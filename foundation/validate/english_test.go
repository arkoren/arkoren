package validate_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestEveryEmittedCodeHasEnglishMessage scans the package source for
// violation code literals and checks each has an English message.
func TestEveryEmittedCodeHasEnglishMessage(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()

	paths, err := filepath.Glob("*.go")

	if err != nil {
		t.Fatal(err)
	}

	var files []*ast.File

	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") || path == "messages_en.go" {
			continue
		}

		file, err := parser.ParseFile(fset, path, nil, 0)

		if err != nil {
			t.Fatal(err)
		}

		files = append(files, file)
	}

	english := validate.English()
	found := map[string]bool{}

	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)

			if !ok || lit.Kind != token.STRING {
				return true
			}

			value, err := strconv.Unquote(lit.Value)

			if err == nil && codeLiteral.MatchString(value) {
				found[value] = true
			}

			return true
		})
	}

	if len(found) < 40 {
		t.Fatalf("scan found only %d codes", len(found))
	}

	for code := range found {
		if english[code] == "" {
			t.Errorf("code %q has no English message", code)
		}
	}
}

// TestEnglishReturnsCopy verifies mutating English() does not change the
// built-in messages.
func TestEnglishReturnsCopy(t *testing.T) {
	t.Parallel()

	messages := validate.English()
	messages["required"] = "changed"
	delete(messages, "string.min")

	again := validate.English()

	if again["required"] != "is required" || again["string.min"] == "" {
		t.Fatal("built-in messages were mutated")
	}

	for _, code := range []string{"type", "unknown", "required", "string.email", "slice.min", "time.future"} {
		if _, ok := again[code]; !ok {
			t.Errorf("English misses %q", code)
		}
	}
}

// TestEnglishCoversEveryBuiltInCode verifies each built-in rule has an
// English message.
func TestEnglishCoversEveryBuiltInCode(t *testing.T) {
	t.Parallel()

	english := validate.English()

	for code, template := range english {
		if strings.TrimSpace(template) == "" {
			t.Errorf("empty message for %q", code)
		}
	}

	if len(english) == 0 {
		t.Fatal("no messages")
	}
}
