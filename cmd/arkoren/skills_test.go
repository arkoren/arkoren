package main

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
	"arkoren.dev/cmd/arkoren/internal/skill"
)

// TestSkillInstallRejectsSymlinkDirectory prevents writes through chosen aliases.
func TestSkillInstallRejectsSymlinkDirectory(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(env.Dir, "linked")); err != nil {
		t.Fatal(err)
	}

	if err := runSkills(t.Context(), []string{"install", "--dir", "linked"}, env); err == nil {
		t.Fatal("install followed a directory symlink")
	}

	entries, err := os.ReadDir(outside)

	if err != nil || len(entries) != 0 {
		t.Fatalf("outside target changed: %v, %v", entries, err)
	}
}

// TestSkillInstallRejectsDanglingManifestLink cannot mistake a broken link for absence.
func TestSkillInstallRejectsDanglingManifestLink(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")

	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("missing", filepath.Join(folder, skill.SkillManifestName)); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runSkills(t.Context(), []string{"install"}, env))
}

// runSkills exercises actual offline command dispatch rather than maintaining
// a second parser in tests. Filesystem fixtures remain scoped to the injected dir.
func runSkills(ctx context.Context, args []string, env console.Environment) error {
	return execute(ctx, append([]string{"skills"}, args...), env)
}

// testSkillBundle builds small content-addressed bundles for update scenarios.
func testSkillBundle(t *testing.T, content map[string]string) skill.Bundle {
	t.Helper()

	bundle := skill.Bundle{Manifest: skill.Manifest{Schema: 1, Name: "arkoren", Files: make(map[string]string)}}

	for _, path := range slices.Sorted(maps.Keys(content)) {
		bundle.Files = append(bundle.Files, files.File{Path: path, Content: []byte(content[path])})
		bundle.Manifest.Files[path] = skill.Hash([]byte(content[path]))
	}

	bundle.Manifest.Revision = skill.Revision(bundle.Manifest.Files)

	if err := skill.ValidateSkillManifest(bundle.Manifest); err != nil {
		t.Fatal(err)
	}

	return bundle
}

// installTestSkill creates a complete managed fixture, including its manifest.
func installTestSkill(t *testing.T, folder string, bundle skill.Bundle) {
	t.Helper()

	inventory, err := skill.Files(bundle)

	if err != nil {
		t.Fatal(err)
	}

	if err := files.Write(t.Context(), folder, inventory); err != nil {
		t.Fatal(err)
	}
}

// testSkillPlan runs real inspection before the read-only update planner.
func testSkillPlan(t *testing.T, folder string, bundle skill.Bundle) skill.Plan {
	t.Helper()

	installed, err := skill.Inspect(t.Context(), folder)

	if err != nil {
		t.Fatal(err)
	}

	plan, err := skill.NewPlan(t.Context(), folder, "update", bundle, installed)

	if err != nil {
		t.Fatal(err)
	}

	return plan
}

// TestSkillsInstallWorksOffline checks dispatch, complete bundle installation,
// and independence from endpoint configuration and process environment.
func TestSkillsInstallWorksOffline(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.WriteFile(filepath.Join(env.Dir, ".env"), []byte("invalid env\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := run(t.Context(), []string{"skills", "install"}, env); code != console.ExitOK {
		t.Fatalf("exit=%d: %s", code, env.Stderr)
	}

	bundle, err := skill.LoadBundle(t.Context(), skillSource)

	if err != nil {
		t.Fatal(err)
	}

	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")
	installed, err := skill.Inspect(t.Context(), folder)

	if err != nil || installed.Manifest == nil || len(installed.Modified) != 0 {
		t.Fatalf("installed=%+v error=%v", installed, err)
	}

	if installed.Manifest.Revision != bundle.Manifest.Revision || len(installed.Manifest.Files) != len(bundle.Files) {
		t.Fatal("installation does not match the complete bundle")
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skills install wrote AGENTS.md: %v", err)
	}
}

// TestSkillsMatchingInstallIsUnchanged prevents repeated installs rewriting files.
func TestSkillsMatchingInstallIsUnchanged(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runSkills(t.Context(), []string{"install"}, env); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren", skill.SkillManifestName)
	before, err := os.Stat(path)

	if err != nil {
		t.Fatal(err)
	}

	if err := runSkills(t.Context(), []string{"install"}, env); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)

	if err != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		t.Fatalf("matching install rewrote manifest: %v", err)
	}
}

// TestSkillsCustomDirectoryAppendsSkillName makes --dir's parent semantics explicit.
func TestSkillsCustomDirectoryAppendsSkillName(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runSkills(t.Context(), []string{"install", "--dir", ".claude/skills"}, env); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(env.Dir, ".claude/skills/arkoren/SKILL.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(env.Dir, ".agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom install created default target: %v", err)
	}
}

// TestSkillsDryRunNeverCreatesDirectories keeps preview entirely read-only.
func TestSkillsDryRunNeverCreatesDirectories(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runSkills(t.Context(), []string{"install", "--dry-run", "--verbose"}, env); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("dry-run wrote files: %v, %v", entries, err)
	}

	contains(t, env.Stdout.(*bytes.Buffer).String(), "SKILL.md", skill.SkillManifestName, `"conflicts": []`)
}

// TestSkillsFlagsBeforeSubcommand exercises inherited family flags without
// writing. The selected parent still receives the arkoren child directory.
func TestSkillsFlagsBeforeSubcommand(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	args := []string{"--verbose", "--dir", ".claude/skills", "--dry-run", "install"}

	if err := runSkills(t.Context(), args, env); err != nil {
		t.Fatal(err)
	}

	contains(t, env.Stdout.(*bytes.Buffer).String(),
		filepath.Join(env.Dir, ".claude/skills/arkoren"), "SKILL.md", `"conflicts": []`)

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("dry-run wrote files: %v, %v", entries, err)
	}
}

// TestSkillsStatusMissingNeverWrites verifies status is useful before installation.
func TestSkillsStatusMissingNeverWrites(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runSkills(t.Context(), []string{"status"}, env); err != nil {
		t.Fatal(err)
	}

	document := (result{stdout: env.Stdout.(*bytes.Buffer).String()}).json(t)

	if document["state"] != "not installed" || document["bundled_revision"] == "" {
		t.Fatalf("status = %v", document)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("status wrote files: %v, %v", entries, err)
	}
}

// TestSkillsInstallRejectsUnmanagedFiles protects installations from other tools.
func TestSkillsInstallRejectsUnmanagedFiles(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")

	if err := files.WriteFile(t.Context(), filepath.Join(folder, "SKILL.md"), []byte("custom\n")); err != nil {
		t.Fatal(err)
	}

	err := runSkills(t.Context(), []string{"install"}, env)

	requireNewUsage(t, err)

	if !strings.Contains(err.Error(), "unmanaged") {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(folder, "SKILL.md"))

	if err != nil || string(content) != "custom\n" {
		t.Fatalf("unmanaged skill replaced: %q, %v", content, err)
	}
}

// TestSkillsUpdateRequiresManagedInstall prevents accidental adoption of a folder.
func TestSkillsUpdateRequiresManagedInstall(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runSkills(t.Context(), []string{"update"}, env)

	requireNewUsage(t, err)

	if !strings.Contains(err.Error(), "no managed installation") {
		t.Fatal(err)
	}
}

// TestSkillsHelpIsOffline covers local help before settings or path validation.
func TestSkillsHelpIsOffline(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"skills", "--help"}, {"skills", "-h"}, {"skills", "update", "--help"}} {
		env := scaffoldEnvironment(t)

		if code := run(t.Context(), args, env); code != console.ExitOK {
			t.Fatalf("%v: exit=%d %s", args, code, env.Stderr)
		}

		contains(t, env.Stdout.(*bytes.Buffer).String(), "arkoren skills", "--dir", "--dry-run")
	}
}

// TestSkillsHelpSkipsPathValidation keeps generated offline help available even
// when the supplied installation path would be invalid for an actual action.
func TestSkillsHelpSkipsPathValidation(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"skills", "install", "--dir=", "--help"},
	} {
		env := scaffoldEnvironment(t)

		if code := run(t.Context(), args, env); code != console.ExitOK {
			t.Fatalf("%v: exit=%d %s", args, code, env.Stderr)
		}

		contains(t, env.Stdout.(*bytes.Buffer).String(), "arkoren skills", "--help")

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 0 {
			t.Fatalf("help wrote files: %v, %v", entries, err)
		}
	}
}

// TestSkillsInvalidArgumentsAreUsageErrors checks the shared invalid-input path.
func TestSkillsInvalidArgumentsAreUsageErrors(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"remove"}, {"install", "extra"}, {"install", "--force"}, {"status", "--dir="}} {
		env := scaffoldEnvironment(t)

		requireNewUsage(t, runSkills(t.Context(), args, env))
	}
}

// TestSkillsOutputFailureExitsNonzero preserves the CLI's stdout failure contract.
func TestSkillsOutputFailureExitsNonzero(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Stdout = &failingOutput{}

	if code := run(t.Context(), []string{"skills", "status"}, env); code != console.ExitFailure {
		t.Fatalf("exit=%d", code)
	}
}

// TestSkillsStatusReportsEditsWithoutFailure keeps status useful for recovery.
func TestSkillsStatusReportsEditsWithoutFailure(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")
	installTestSkill(t, folder, testSkillBundle(t, map[string]string{"SKILL.md": "old"}))

	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := run(t.Context(), []string{"skills", "status", "--verbose"}, env); code != console.ExitOK {
		t.Fatalf("exit=%d: %s", code, env.Stderr)
	}

	document := (result{stdout: env.Stdout.(*bytes.Buffer).String()}).json(t)

	if len(document["modified_files"].([]any)) != 1 || document["installed_revision"] == "" || document["bundled_revision"] == "" {
		t.Fatalf("status = %v", document)
	}
}

// TestSkillsUpdateDryRunPreservesInstalledRevision previews real bundled updates.
func TestSkillsUpdateDryRunPreservesInstalledRevision(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")
	old := testSkillBundle(t, map[string]string{"SKILL.md": "old"})
	installTestSkill(t, folder, old)

	if err := runSkills(t.Context(), []string{"update", "--dry-run", "--verbose"}, env); err != nil {
		t.Fatal(err)
	}

	installed, err := skill.Inspect(t.Context(), folder)

	if err != nil || installed.Manifest.Revision != old.Manifest.Revision || len(installed.Modified) != 0 {
		t.Fatalf("dry-run changed installed revision: %+v, %v", installed, err)
	}

	contains(t, env.Stdout.(*bytes.Buffer).String(), `"operation": "write"`, `"path": "SKILL.md"`, `"conflicts": []`)
}

// TestSkillsInstallDifferentRevisionRequiresExplicitUpdate avoids implicit upgrades.
func TestSkillsInstallDifferentRevisionRequiresExplicitUpdate(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")
	installTestSkill(t, folder, testSkillBundle(t, map[string]string{"SKILL.md": "old"}))

	err := runSkills(t.Context(), []string{"install"}, env)

	requireNewUsage(t, err)

	if !strings.Contains(err.Error(), "run arkoren skills update") {
		t.Fatal(err)
	}
}

// TestSkillsUpdateRemovesManagedAPIPage exercises migration from an archived
// API page to the actual embedded skill through offline command dispatch.
// The ownership manifest must remain the last write in the resulting plan.
func TestSkillsUpdateRemovesManagedAPIPage(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")
	old := testSkillBundle(t, map[string]string{
		"SKILL.md":                     "old skill",
		"references/api/core/cache.md": "obsolete API page",
	})
	installTestSkill(t, folder, old)

	bundle, err := skill.LoadBundle(t.Context(), skillSource)

	if err != nil {
		t.Fatal(err)
	}

	plan := testSkillPlan(t, folder, bundle)

	if plan.Changes[len(plan.Changes)-1].Path != skill.SkillManifestName {
		t.Fatal("ownership manifest is not the final planned write")
	}

	if err := runSkills(t.Context(), []string{"update"}, env); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(folder, "references/api/core/cache.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete managed API page remains: %v", err)
	}

	installed, err := skill.Inspect(t.Context(), folder)

	if err != nil {
		t.Fatal(err)
	}

	if installed.Manifest.Revision != bundle.Manifest.Revision || len(installed.Modified) != 0 {
		t.Fatalf("update incomplete: %+v", installed)
	}
}

// TestSkillsUpdateProtectsModifiedAPIPage preserves edits to retired managed
// API pages and leaves the previous ownership record intact on conflict.
func TestSkillsUpdateProtectsModifiedAPIPage(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren")
	old := testSkillBundle(t, map[string]string{
		"SKILL.md":                     "old skill",
		"references/api/core/cache.md": "obsolete API page",
	})
	installTestSkill(t, folder, old)
	path := filepath.Join(folder, "references/api/core/cache.md")

	if err := os.WriteFile(path, []byte("user edits"), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(filepath.Join(folder, skill.SkillManifestName))

	if err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runSkills(t.Context(), []string{"update"}, env))

	content, err := os.ReadFile(path)

	if err != nil || string(content) != "user edits" {
		t.Fatalf("edited API page changed: %q, %v", content, err)
	}

	after, err := os.ReadFile(filepath.Join(folder, skill.SkillManifestName))

	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("conflict changed ownership manifest: %v", err)
	}

	content, err = os.ReadFile(filepath.Join(folder, "SKILL.md"))

	if err != nil || string(content) != "old skill" {
		t.Fatalf("conflict partially updated skill: %q, %v", content, err)
	}
}
