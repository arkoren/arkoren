package scaffold

import (
	"path/filepath"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/files"
)

// TestScaffoldPlanFindsLaterFileAncestor checks the full plan independently of
// emission order. The file that blocks a parent may follow all its descendants.
func TestScaffoldPlanFindsLaterFileAncestor(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	inventory := []files.File{
		{Path: filepath.FromSlash("go.mod/arkoren/SKILL.md")},
		{Path: "go.mod"},
	}

	conflicts, err := Inspect(t.Context(), filepath.Join(env.Dir, "hello"), inventory)

	if err != nil {
		t.Fatal(err)
	}

	if len(conflicts) != 1 || conflicts[0] != "go.mod: generated file is also required as a directory" {
		t.Fatalf("conflicts = %v", conflicts)
	}
}

// TestScaffoldPlanAllowsDirectoryCaseAliases ensures case folding does not
// mistake shared directories for duplicate files. Distinct leaf files remain
// valid whether the filesystem shares or separates differently cased parents.
func TestScaffoldPlanAllowsDirectoryCaseAliases(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	inventory := []files.File{
		{Path: filepath.FromSlash("cmd/server/main.go")},
		{Path: filepath.FromSlash("CMD/arkoren/SKILL.md")},
	}

	conflicts, err := Inspect(t.Context(), filepath.Join(env.Dir, "hello"), inventory)

	if err != nil || len(conflicts) != 0 {
		t.Fatalf("shared directory rejected: %v, %v", conflicts, err)
	}
}
