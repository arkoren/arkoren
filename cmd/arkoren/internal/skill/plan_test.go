package skill

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// TestSkillUpdateBlocksModifiedFilesBeforeAnyWrites protects user edits globally.
func TestSkillUpdateBlocksModifiedFilesBeforeAnyWrites(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(scaffoldEnvironment(t).Dir, "arkoren")
	old := testSkillBundle(t, map[string]string{"SKILL.md": "old", "references/old.md": "obsolete"})
	installTestSkill(t, folder, old)

	if err := os.WriteFile(filepath.Join(folder, "references/old.md"), []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}

	next := testSkillBundle(t, map[string]string{"SKILL.md": "new"})

	plan := testSkillPlan(t, folder, next)

	if !slices.Equal(plan.Conflicts, []string{"references/old.md"}) {
		t.Fatalf("conflicts=%v", plan.Conflicts)
	}

	requireNewUsage(t, Write(t.Context(), folder, plan))

	content, err := os.ReadFile(filepath.Join(folder, "SKILL.md"))

	if err != nil || string(content) != "old" {
		t.Fatalf("conflict partially updated files: %q, %v", content, err)
	}
}

// requireNewUsage checks the existing CLI usage-error contract, not just the
// message. Validation failures must produce exit 2 rather than runtime failure.
func requireNewUsage(t *testing.T, err error) {
	t.Helper()

	exit, ok := errors.AsType[*console.ExitError](err)

	if !ok || exit.Code != console.ExitUsage {
		t.Fatalf("error = %v, want usage error", err)
	}
}

// TestSkillUpdateBlocksDeletedFiles treats local deletion as a change to preserve.
func TestSkillUpdateBlocksDeletedFiles(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(scaffoldEnvironment(t).Dir, "arkoren")
	bundle := testSkillBundle(t, map[string]string{"SKILL.md": "old", "references/page.md": "page"})
	installTestSkill(t, folder, bundle)

	if err := os.Remove(filepath.Join(folder, "references/page.md")); err != nil {
		t.Fatal(err)
	}

	plan := testSkillPlan(t, folder, bundle)

	if !slices.Equal(plan.Conflicts, []string{"references/page.md"}) {
		t.Fatalf("missing file conflicts=%v", plan.Conflicts)
	}
}

// TestSkillUpdateBlocksUnownedNewPath protects unrelated files from new releases.
func TestSkillUpdateBlocksUnownedNewPath(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(scaffoldEnvironment(t).Dir, "arkoren")
	installTestSkill(t, folder, testSkillBundle(t, map[string]string{"SKILL.md": "old"}))

	if err := os.WriteFile(filepath.Join(folder, "notes.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	next := testSkillBundle(t, map[string]string{"SKILL.md": "new", "notes.md": "bundled"})

	plan := testSkillPlan(t, folder, next)

	if len(plan.Conflicts) != 1 || !strings.Contains(plan.Conflicts[0], "notes.md") {
		t.Fatalf("conflicts=%v", plan.Conflicts)
	}
}

// TestSkillManifestInvalidJSONIsNotAdopted keeps broken state from granting ownership.
func TestSkillManifestInvalidJSONIsNotAdopted(t *testing.T) {
	t.Parallel()

	folder := scaffoldEnvironment(t).Dir

	if err := os.WriteFile(filepath.Join(folder, SkillManifestName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Inspect(t.Context(), folder)

	requireNewUsage(t, err)
}
