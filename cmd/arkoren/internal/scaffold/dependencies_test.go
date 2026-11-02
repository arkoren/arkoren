package scaffold

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/files"
)

// TestExecuteResolvesDependencies verifies setup runs after writing and ignores
// an enclosing workspace, without requiring network access or external modules.
func TestExecuteResolvesDependencies(t *testing.T) {
	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, "hello")
	t.Setenv("GOWORK", filepath.Join(env.Dir, "missing.work"))
	inventory := []files.File{
		{Path: "go.mod", Content: []byte("module hello\n\ngo 1.27\n\nrequire example.com/unused v1.0.0\n")},
		{Path: "main.go", Content: []byte("package main\n\nfunc main() {}\n")},
	}

	if err := Execute(t.Context(), Options{Folder: folder, Module: "hello"}, inventory, env); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(folder, "go.mod"))

	if err != nil || strings.Contains(string(content), "example.com/unused") {
		t.Fatalf("go.mod was not tidied: %q, %v", content, err)
	}
}

// TestExecuteDependencyFailurePreservesFiles checks partial-success recovery
// and prevents a successful JSON result when Go cannot resolve the project.
func TestExecuteDependencyFailurePreservesFiles(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, "hello")
	inventory := []files.File{{Path: "go.mod", Content: []byte("invalid module file\n")}}
	err := Execute(t.Context(), Options{Folder: folder, Module: "hello"}, inventory, env)

	if err == nil || !strings.Contains(err.Error(), "retry go mod tidy") || !strings.Contains(err.Error(), folder) {
		t.Fatalf("dependency failure = %v", err)
	}

	if _, err := os.Stat(filepath.Join(folder, "go.mod")); err != nil {
		t.Fatalf("generated file was not preserved: %v", err)
	}

	if env.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatal("dependency failure emitted a success report")
	}
}

// TestExecuteSkipTidy creates even an unresolved module without invoking Go.
func TestExecuteSkipTidy(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	folder := filepath.Join(env.Dir, "hello")
	inventory := []files.File{{Path: "go.mod", Content: []byte("invalid module file\n")}}

	if err := Execute(t.Context(), Options{Folder: folder, Module: "hello", SkipTidy: true}, inventory, env); err != nil {
		t.Fatal(err)
	}
}

// TestTidyCancellationPreservesCause keeps cancellation detectable by callers.
func TestTidyCancellationPreservesCause(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := tidy(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tidy = %v", err)
	}
}

// TestTidyMissingGo reports a recoverable setup failure without a Go executable.
func TestTidyMissingGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if err := tidy(t.Context(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "retry go mod tidy") {
		t.Fatalf("missing Go = %v", err)
	}
}
