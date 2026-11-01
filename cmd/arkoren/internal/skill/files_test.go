package skill

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// TestSkillUpdateRejectsSymlinkFile protects both managed paths and link targets.
func TestSkillUpdateRejectsSymlinkFile(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, "arkoren")
	bundle := testSkillBundle(t, map[string]string{"SKILL.md": "old"})
	installTestSkill(t, folder, bundle)

	outside := filepath.Join(env.Dir, "outside.md")

	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(folder, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, filepath.Join(folder, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	plan := testSkillPlan(t, folder, bundle)

	if len(plan.Conflicts) != 1 || plan.Conflicts[0] != "SKILL.md" {
		t.Fatalf("conflicts=%v", plan.Conflicts)
	}

	content, err := os.ReadFile(outside)

	if err != nil || string(content) != "outside" {
		t.Fatalf("outside target changed: %q, %v", content, err)
	}
}

// scaffoldEnvironment isolates the filesystem and streams for offline tests.
// Env panics deliberately: scaffolding must never read inspector settings.
func scaffoldEnvironment(t *testing.T) console.Environment {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())

	if err != nil {
		t.Fatal(err)
	}

	return console.Environment{
		Dir:    dir,
		Stdin:  strings.NewReader(""),
		Stdout: new(bytes.Buffer),
		Stderr: new(bytes.Buffer),
		Env: func(string) (string, bool) {
			panic("new read process environment")
		},
	}
}

// testSkillBundle builds small content-addressed bundles for update scenarios.
func testSkillBundle(t *testing.T, content map[string]string) Bundle {
	t.Helper()

	bundle := Bundle{Manifest: Manifest{Schema: 1, Name: "arkoren", Files: make(map[string]string)}}

	for _, path := range slices.Sorted(maps.Keys(content)) {
		bundle.Files = append(bundle.Files, files.File{Path: path, Content: []byte(content[path])})
		bundle.Manifest.Files[path] = Hash([]byte(content[path]))
	}

	bundle.Manifest.Revision = Revision(bundle.Manifest.Files)

	if err := ValidateSkillManifest(bundle.Manifest); err != nil {
		t.Fatal(err)
	}

	return bundle
}

// installTestSkill creates a complete managed fixture, including its manifest.
func installTestSkill(t *testing.T, folder string, bundle Bundle) {
	t.Helper()

	inventory, err := Files(bundle)

	if err != nil {
		t.Fatal(err)
	}

	if err := files.Write(t.Context(), folder, inventory); err != nil {
		t.Fatal(err)
	}
}

// testSkillPlan runs real inspection before the read-only update planner.
func testSkillPlan(t *testing.T, folder string, bundle Bundle) Plan {
	t.Helper()

	installed, err := Inspect(t.Context(), folder)

	if err != nil {
		t.Fatal(err)
	}

	plan, err := NewPlan(t.Context(), folder, "update", bundle, installed)

	if err != nil {
		t.Fatal(err)
	}

	return plan
}

// TestSkillUpdateRejectsChangedDirectoryAncestor prevents replacing an owned
// reference directory with an alias even when the linked file bytes match.
func TestSkillUpdateRejectsChangedDirectoryAncestor(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, "arkoren")
	bundle := testSkillBundle(t, map[string]string{"SKILL.md": "skill", "references/page.md": "page"})

	installTestSkill(t, folder, bundle)

	if err := os.Rename(filepath.Join(folder, "references"), filepath.Join(env.Dir, "saved")); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join(env.Dir, "saved"), filepath.Join(folder, "references")); err != nil {
		t.Fatal(err)
	}

	plan := testSkillPlan(t, folder, bundle)

	if len(plan.Conflicts) != 1 || plan.Conflicts[0] != "references/page.md" {
		t.Fatalf("conflicts=%v", plan.Conflicts)
	}
}

// TestSkillUpdateTempFilesAreRemoved keeps completed atomic replacements tidy.
func TestSkillUpdateTempFilesAreRemoved(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(scaffoldEnvironment(t).Dir, "arkoren")
	installTestSkill(t, folder, testSkillBundle(t, map[string]string{"SKILL.md": "old"}))

	plan := testSkillPlan(t, folder, testSkillBundle(t, map[string]string{"SKILL.md": "new"}))

	if err := Write(t.Context(), folder, plan); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(folder)

	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

// TestSkillInstallCollisionAppearingAfterPlanPreservesOriginal verifies exclusive
// creation when a new owned path appears after inspection.
func TestSkillInstallCollisionAppearingAfterPlanPreservesOriginal(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(scaffoldEnvironment(t).Dir, "arkoren")
	bundle := testSkillBundle(t, map[string]string{"SKILL.md": "bundled"})

	plan, err := NewPlan(t.Context(), folder, "install", bundle, Installation{})

	if err != nil {
		t.Fatal(err)
	}

	if err := files.WriteFile(t.Context(), filepath.Join(folder, "SKILL.md"), []byte("mine")); err != nil {
		t.Fatal(err)
	}

	if err := Write(t.Context(), folder, plan); err == nil || !strings.Contains(err.Error(), "no files were written") {
		t.Fatalf("unexpected collision result: %v", err)
	}

	if _, err := os.Stat(filepath.Join(folder, SkillManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("collision wrote manifest: %v", err)
	}
}

// TestSkillUpdateReplacesAddsAndRemovesOnlyManagedFiles exercises a full update
// while retaining an unrelated file and leaving the user-owned AGENTS.md alone.
func TestSkillUpdateReplacesAddsAndRemovesOnlyManagedFiles(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, "skills/arkoren")
	old := testSkillBundle(t, map[string]string{"SKILL.md": "old", "references/old.md": "obsolete"})
	installTestSkill(t, folder, old)

	if err := os.WriteFile(filepath.Join(folder, "notes.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(env.Dir, "AGENTS.md"), []byte("my rules"), 0o600); err != nil {
		t.Fatal(err)
	}

	next := testSkillBundle(t, map[string]string{"SKILL.md": "new", "references/new.md": "added"})

	plan := testSkillPlan(t, folder, next)

	if len(plan.Conflicts) != 0 || len(plan.Changes) != 4 || plan.Changes[len(plan.Changes)-1].Path != SkillManifestName {
		t.Fatalf("invalid update plan: %+v", plan)
	}

	if err := Write(t.Context(), folder, plan); err != nil {
		t.Fatal(err)
	}

	installed, err := Inspect(t.Context(), folder)

	if err != nil || installed.Manifest.Revision != next.Manifest.Revision || len(installed.Modified) != 0 {
		t.Fatalf("update incomplete: %+v, %v", installed, err)
	}

	if _, err := os.Stat(filepath.Join(folder, "references/old.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete managed file remains: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(folder, "notes.md"))

	if err != nil || string(content) != "mine" {
		t.Fatalf("unrelated file changed: %q, %v", content, err)
	}

	content, err = os.ReadFile(filepath.Join(env.Dir, "AGENTS.md"))

	if err != nil || string(content) != "my rules" {
		t.Fatalf("AGENTS.md changed: %q, %v", content, err)
	}
}

// TestSkillUpdateRechecksUnchangedFiles catches edits made after planning.
func TestSkillUpdateRechecksUnchangedFiles(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(scaffoldEnvironment(t).Dir, "arkoren")
	installTestSkill(t, folder, testSkillBundle(t, map[string]string{"SKILL.md": "same", "page.md": "old"}))

	next := testSkillBundle(t, map[string]string{"SKILL.md": "same", "page.md": "new"})

	plan := testSkillPlan(t, folder, next)

	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("local edit"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Write(t.Context(), folder, plan); err == nil {
		t.Fatal("update ignored edits after inspection")
	}

	content, err := os.ReadFile(filepath.Join(folder, "page.md"))

	if err != nil || string(content) != "old" {
		t.Fatalf("preflight failure wrote files: %q, %v", content, err)
	}
}

// TestSkillPartialUpdateKeepsOldManifest makes interruption detectable on retry.
func TestSkillPartialUpdateKeepsOldManifest(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(scaffoldEnvironment(t).Dir, "arkoren")
	old := testSkillBundle(t, map[string]string{"SKILL.md": "old"})
	installTestSkill(t, folder, old)

	next := testSkillBundle(t, map[string]string{"SKILL.md": "new"})

	plan := testSkillPlan(t, folder, next)

	if err := ApplySkillChange(t.Context(), folder, plan.Changes[0]); err != nil {
		t.Fatal(err)
	}

	installed, err := Inspect(t.Context(), folder)

	if err != nil || installed.Manifest.Revision != old.Manifest.Revision || !slices.Equal(installed.Modified, []string{"SKILL.md"}) {
		t.Fatalf("partial update was not detected: %+v, %v", installed, err)
	}
}

// TestSkillCanceledUpdateDoesNotWrite verifies cancellation before mutation.
func TestSkillCanceledUpdateDoesNotWrite(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(scaffoldEnvironment(t).Dir, "arkoren")
	old := testSkillBundle(t, map[string]string{"SKILL.md": "old"})
	installTestSkill(t, folder, old)

	plan := testSkillPlan(t, folder, testSkillBundle(t, map[string]string{"SKILL.md": "new"}))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := Write(ctx, folder, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}

	content, err := os.ReadFile(filepath.Join(folder, "SKILL.md"))

	if err != nil || string(content) != "old" {
		t.Fatalf("canceled update wrote files: %q, %v", content, err)
	}
}
