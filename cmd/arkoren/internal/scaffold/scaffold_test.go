package scaffold

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// TestScaffoldPlanExecutionRejectsDuplicateFiles supplies a duplicate that
// current templates cannot produce. Future template changes must not turn
// exclusive-create failure into the first detection of an invalid plan.
func TestScaffoldPlanExecutionRejectsDuplicateFiles(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	inventory := []files.File{{Path: "go.mod"}, {Path: "go.mod"}}
	options := Options{Folder: filepath.Join(env.Dir, "hello"), Module: "hello", Yes: true}

	requireNewUsage(t, Execute(t.Context(), options, inventory, env))

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("duplicate plan partially wrote scaffold: %v, %v", entries, err)
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

// requireNewUsage checks the existing CLI usage-error contract, not just the
// message. Validation failures must produce exit 2 rather than runtime failure.
func requireNewUsage(t *testing.T, err error) {
	t.Helper()

	exit, ok := errors.AsType[*console.ExitError](err)

	if !ok || exit.Code != console.ExitUsage {
		t.Fatalf("error = %v, want usage error", err)
	}
}

// TestScaffoldPlanDryRunRejectsCaseFoldedDuplicateFiles covers leaf aliases
// below differently cased directory names. The preview must reject without
// attempting either exclusive file creation.
func TestScaffoldPlanDryRunRejectsCaseFoldedDuplicateFiles(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Presentation.Verbose = true
	inventory := []files.File{
		{Path: filepath.FromSlash("cmd/server/main.go")},
		{Path: filepath.FromSlash("CMD/SERVER/MAIN.GO")},
	}
	options := Options{Folder: filepath.Join(env.Dir, "hello"), Module: "hello", DryRun: true}

	requireNewUsage(t, Execute(t.Context(), options, inventory, env))

	var document map[string]any

	if err := json.Unmarshal(env.Stdout.(*bytes.Buffer).Bytes(), &document); err != nil {
		t.Fatal(err)
	}

	if conflicts := document["conflicts"].([]any); len(conflicts) != 1 {
		t.Fatalf("case-folded duplicate conflicts = %v", conflicts)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("duplicate preview changed destination: %v, %v", entries, err)
	}
}
