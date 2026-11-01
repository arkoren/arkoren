package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/remote"
	"arkoren.dev/cmd/arkoren/internal/skill"
)

// TestNewPromptWriteFailureDoesNotReadOrWrite drives the complete offline CLI
// with a ready yes line. Failure to deliver the prompt must return exit 1 before
// consuming approval or changing the nonempty destination, even if the final
// best-effort diagnostic cannot be delivered to the same failed stream.
func TestNewPromptWriteFailureDoesNotReadOrWrite(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	input := strings.NewReader("yes\n")
	env.Stdin = input
	env.InputTerminal = true
	env.Stderr = &failingOutput{err: errors.New("confirmation prompt output failed")}

	path := filepath.Join(env.Dir, "notes")

	if err := os.WriteFile(path, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code := run(t.Context(), []string{
		"new", ".", "--no-agent-files", "--url=invalid", "--env-file=missing",
	}, env)

	if code != console.ExitFailure {
		t.Errorf("exit = %d, want %d", code, console.ExitFailure)
	}

	if input.Len() != len("yes\n") {
		t.Errorf("failed prompt consumed approval: %d bytes remain", input.Len())
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 1 || entries[0].Name() != "notes" {
		t.Fatalf("failed prompt changed destination: %v, %v", entries, err)
	}

	content, err := os.ReadFile(path)

	if err != nil || string(content) != "keep\n" {
		t.Fatalf("failed prompt changed existing file: %q, %v", content, err)
	}

	if env.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatal("failed prompt emitted a creation result")
	}
}

// TestNewPromptShortWriteDoesNotReadOrWrite covers a writer that truncates the
// prompt without returning an error. The CLI must treat this as output failure,
// not accept an available yes line and proceed with filesystem mutations.
func TestNewPromptShortWriteDoesNotReadOrWrite(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	input := strings.NewReader("yes\n")
	env.Stdin = input
	env.InputTerminal = true
	env.Stderr = &failingOutput{}

	path := filepath.Join(env.Dir, "notes")

	if err := os.WriteFile(path, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code := run(t.Context(), []string{
		"new", ".", "--no-agent-files", "--url=invalid", "--env-file=missing",
	}, env)

	if code != console.ExitFailure {
		t.Errorf("exit = %d, want %d", code, console.ExitFailure)
	}

	if input.Len() != len("yes\n") {
		t.Errorf("short prompt consumed approval: %d bytes remain", input.Len())
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 1 || entries[0].Name() != "notes" {
		t.Fatalf("short prompt changed destination: %v, %v", entries, err)
	}

	content, err := os.ReadFile(path)

	if err != nil || string(content) != "keep\n" {
		t.Fatalf("short prompt changed existing file: %q, %v", content, err)
	}

	if env.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatal("short prompt emitted a creation result")
	}
}

// TestCreationDryRunIgnoresInspectorConfiguration keeps local generation
// independent of both malformed dotenv files and inspector environment values.
// The malformed .env lives in the working directory, outside the destination,
// so this test does not conflict with the scaffold's own generated .env.
func TestCreationDryRunIgnoresInspectorConfiguration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("not an assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"new", "billing", "--module=example.com/billing", "--dry-run"}, console.Environment{
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
		Dir:    dir,
		Env: func(name string) (string, bool) {
			if name == remote.EnvURL {
				return ":invalid-inspector-url", true
			}

			return "", false
		},
	})

	if code != console.ExitOK {
		t.Fatalf("exit = %d; stderr = %s", code, stderr.String())
	}

	entries, err := os.ReadDir(dir)

	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 || entries[0].Name() != ".env" {
		t.Fatalf("dry-run changed the destination: %v", entries)
	}
}

// TestCreationHelpWorksWithoutInspectorSettings checks both local help forms
// without accepting an inspector configuration as a prerequisite.
func TestCreationHelpWorksWithoutInspectorSettings(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"new", "--help"}, {"new", "-h"}} {
		var stdout, stderr bytes.Buffer

		code := run(t.Context(), args, console.Environment{
			Stdin:  strings.NewReader(""),
			Stdout: &stdout,
			Stderr: &stderr,
			Dir:    t.TempDir(),
			Env: func(string) (string, bool) {
				t.Error("local help consulted the environment")

				return "", false
			},
		})

		if code != console.ExitOK {
			t.Fatalf("%v: exit = %d; stderr = %s", args, code, stderr.String())
		}

		if !strings.Contains(stdout.String(), "arkoren new") || !strings.Contains(stdout.String(), "--module") {
			t.Fatalf("%v: incomplete creation help: %s", args, stdout.String())
		}

		if strings.Contains(stdout.String(), "--di ") {
			t.Fatal("creation help advertises removed DI support")
		}
	}
}

// TestCreationHelpSkipsScaffoldValidation keeps global help ahead of scaffold
// parsing and path validation. Asking for usage must not inspect or write files,
// even when the accompanying creation options could not produce a project.
func TestCreationHelpSkipsScaffoldValidation(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	code := run(t.Context(), []string{"new", "", "--skills-dir=../invalid", "--help"}, env)

	if code != console.ExitOK {
		t.Fatalf("exit = %d; stderr = %s", code, env.Stderr)
	}

	if !strings.Contains(env.Stdout.(*bytes.Buffer).String(), "arkoren new") {
		t.Fatalf("missing creation help: %s", env.Stdout)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("help changed the destination: %v, %v", entries, err)
	}
}

// TestCreationYesNeverOverwritesModule verifies that command dispatch cannot
// turn confirmation bypass into permission to replace existing project files.
func TestCreationYesNeverOverwritesModule(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	original := []byte("module example.com/existing\n\ngo 1.27\n")

	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"new", ".", "--module=example.com/billing", "--yes"}, console.Environment{
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
		Dir:    dir,
		Env: func(string) (string, bool) {
			return "", false
		},
	})

	if code == console.ExitOK {
		t.Fatal("creation succeeded despite an existing go.mod")
	}

	got, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, original) {
		t.Fatalf("existing module changed: %s", got)
	}

	entries, err := os.ReadDir(dir)

	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 {
		t.Fatalf("creation wrote files before rejecting the conflict: %v", entries)
	}
}

// runNew drives the production command tree so scaffold tests cover framework
// parsing and offline dispatch as well as rendering and filesystem safeguards.
func runNew(ctx context.Context, args []string, env console.Environment) error {
	return execute(ctx, append([]string{"new"}, args...), env)
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

// TestNewDefaults checks the application files, agent bundle, and basename default.
// No go command is invoked: ordinary scaffold tests remain fully offline.
func TestNewDefaults(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"."}, env); err != nil {
		t.Fatal(err)
	}

	name := filepath.Base(env.Dir)
	module, err := os.ReadFile(filepath.Join(env.Dir, "go.mod"))

	if err != nil {
		t.Fatal(err)
	}

	if string(module) != "module "+name+"\n\ngo 1.27\n\nrequire (\n\tarkoren.dev/core v0.0.0-20261031153809-a3f0efb45148\n\tarkoren.dev/driver/mcp v0.0.0-20261031184540-4c2942b4a60b\n\tgithub.com/joho/godotenv v1.5.1\n\tgithub.com/lmittmann/tint v1.2.1\n\tgithub.com/mattn/go-isatty v0.0.24\n)\n" {
		t.Fatalf("go.mod = %q", module)
	}

	inventory := []string{
		"cmd/server/main.go", "internal/bootstrap/bootstrap.go", "internal/bootstrap/mcp.go",
		"internal/bootstrap/router.go", "internal/bootstrap/server.go",
		"internal/bootstrap/app.go", "internal/bootstrap/logging.go", ".env", ".env.example", "go.mod", "README.md",
		"Dockerfile", "Makefile", ".dockerignore", ".gitignore", ".editorconfig",
	}

	for _, path := range inventory {
		if _, err := os.Stat(filepath.Join(env.Dir, path)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "internal/config")); !os.IsNotExist(err) {
		t.Fatalf("unexpected generated configuration package: %v", err)
	}

	count := 0

	err = filepath.WalkDir(env.Dir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() {
			count++
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	bundle, err := skill.LoadBundle(t.Context(), skillSource)

	if err != nil {
		t.Fatal(err)
	}

	want := len(inventory) + 2 + len(bundle.Files)

	if count != want {
		t.Fatalf("files = %d, want %d", count, want)
	}

	installed, err := skill.Inspect(t.Context(), filepath.Join(env.Dir, skill.DefaultSkillsDir, "arkoren"))

	if err != nil || installed.Manifest == nil || len(installed.Modified) != 0 {
		t.Fatalf("invalid generated skill: %+v, %v", installed, err)
	}
}

// TestNewContainerFileCollisionsNeverOverwrite protects existing deployment
// and ignore settings, including when --yes approves a nonempty destination.
func TestNewContainerFileCollisionsNeverOverwrite(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Dockerfile", "Makefile", ".dockerignore", ".gitignore", ".editorconfig", ".env", ".env.example"} {
		env := scaffoldEnvironment(t)
		path := filepath.Join(env.Dir, name)

		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		requireNewUsage(t, runNew(t.Context(), []string{".", "--yes"}, env))

		content, err := os.ReadFile(path)

		if err != nil || string(content) != "original\n" {
			t.Fatalf("%s = %q, error = %v", name, content, err)
		}

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 1 {
			t.Fatalf("%s conflict wrote files: %v, %v", name, entries, err)
		}
	}
}

// TestNewRequiresDestination keeps a bare command from writing into the current
// directory and explains how to select that destination explicitly.
func TestNewRequiresDestination(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runNew(t.Context(), nil, env)

	requireNewUsage(t, err)

	if !strings.Contains(err.Error(), "arkoren new DIR [flags]") || !strings.Contains(err.Error(), "use .") {
		t.Fatalf("missing destination guidance: %v", err)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("missing destination wrote files: %v, %v", entries, err)
	}
}

// TestNewRejectsFolderFlag ensures the removed flag cannot select a destination.
func TestNewRejectsFolderFlag(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	requireNewUsage(t, runNew(t.Context(), []string{".", "--folder", "hello"}, env))

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("removed flag wrote files: %v, %v", entries, err)
	}
}

// TestNewRejectsRemovedDIFlag rejects every formerly supported composition flag
// without writing files or silently falling back to direct construction.
func TestNewRejectsRemovedDIFlag(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"none", "do"} {
		env := scaffoldEnvironment(t)

		requireNewUsage(t, runNew(t.Context(), []string{".", "--di", mode}, env))

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 0 {
			t.Fatalf("removed flag wrote files: %v, %v", entries, err)
		}
	}
}

// TestNewNamedDestinationDefaultsModule derives module identity from the first
// positional argument without requiring any flags.
func TestNewNamedDestinationDefaultsModule(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"momentum"}, env); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(env.Dir, "momentum/go.mod"))

	if err != nil {
		t.Fatal(err)
	}

	if string(content) != "module momentum\n\ngo 1.27\n\nrequire (\n\tarkoren.dev/core v0.0.0-20261031153809-a3f0efb45148\n\tarkoren.dev/driver/mcp v0.0.0-20261031184540-4c2942b4a60b\n\tgithub.com/joho/godotenv v1.5.1\n\tgithub.com/lmittmann/tint v1.2.1\n\tgithub.com/mattn/go-isatty v0.0.24\n)\n" {
		t.Fatalf("go.mod = %q", content)
	}
}

// TestNewRejectsMissingDestinations preserves the explicit destination rule
// when only flags are supplied or an empty string is passed.
func TestNewRejectsMissingDestinations(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{""}, {"--dry-run"}, {"--yes"}} {
		env := scaffoldEnvironment(t)

		requireNewUsage(t, runNew(t.Context(), args, env))

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 0 {
			t.Fatalf("invalid destination wrote files: %v, %v", entries, err)
		}
	}
}

// TestNewFlagsBeforeDestination accepts normal framework flag ordering while
// previewing only: a parser regression must never write into the checkout.
func TestNewFlagsBeforeDestination(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	args := []string{"--no-agent-files", "--dry-run", "--module", "example.com/hello", "hello"}

	if err := runNew(t.Context(), args, env); err != nil {
		t.Fatal(err)
	}

	contains(t, env.Stdout.(*bytes.Buffer).String(), `"module": "example.com/hello"`, "hello")

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("dry-run wrote files: %v, %v", entries, err)
	}
}

// TestNewFlagsAroundDestination keeps values and booleans consistent when
// options span both sides of the sole positional argument. Preview stays local.
func TestNewFlagsAroundDestination(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	args := []string{"--module=example.com/hello", "--dry-run", "hello", "--yes", "--no-agent-files", "--verbose"}

	if err := runNew(t.Context(), args, env); err != nil {
		t.Fatal(err)
	}

	output := env.Stdout.(*bytes.Buffer).String()

	contains(t, output, `"module": "example.com/hello"`, `"yes": true`, `"conflicts": []`)

	if strings.Contains(output, "AGENTS.md") {
		t.Fatal("opt-out preview includes agent files")
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("dry-run wrote files: %v, %v", entries, err)
	}
}

// TestNewCustomFolderAndModule keeps the server path fixed while deriving the
// application identity from the destination and imports from the module.
func TestNewCustomFolderAndModule(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	args := []string{"nested/my-api", "--module", "example.com/team/service/v2"}

	err := runNew(t.Context(), args, env)

	if err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(env.Dir, "nested/my-api/cmd/server/main.go"))

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(content), `"example.com/team/service/v2/internal/bootstrap"`) {
		t.Fatalf("wrong imports: %s", content)
	}

	content, err = os.ReadFile(filepath.Join(env.Dir, "nested/my-api/internal/bootstrap/app.go"))

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(content), `app.Name("my-api")`) {
		t.Fatalf("application name does not follow the destination: %s", content)
	}
}

// TestNewInvalidModules checks hostile and malformed module syntax before any
// writes. All cases have the same rejection and empty-directory expectation.
func TestNewInvalidModules(t *testing.T) {
	t.Parallel()

	for _, module := range []string{
		"/app", "example.com//app", "example.com/app/", "../app", "-app", "example.com/a..b",
		"example.com/app\nrequire evil v1.0.0", `example.com/a"b`, "Example.com/app", "example_com.test/app",
		"example.com/CON", "example.com/COM1.txt", "example.com/app~1", "example.com/app/v1",
		"example.com/app/v01", "example.com/app/v2.0", "gopkg.in/yaml", "gopkg.in/yaml.v01",
	} {
		env := scaffoldEnvironment(t)

		err := runNew(t.Context(), []string{".", "--module", module}, env)

		requireNewUsage(t, err)

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 0 {
			t.Fatalf("module %q wrote files: %v, %v", module, entries, err)
		}
	}
}

// TestNewRejectsUnsupportedArguments makes every invalid flag or positional a
// usage error, including options that might imply extra scaffold features.
func TestNewRejectsUnsupportedArguments(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"hello", "extra"}, {".", "--unknown"}, {".", "--di", "wire"}} {
		env := scaffoldEnvironment(t)

		requireNewUsage(t, runNew(t.Context(), args, env))
	}
}

// TestNewRejectsInvalidDestination ensures command-directory names cannot
// inject source, be hidden/import-invalid, or use Windows device aliases.
func TestNewRejectsInvalidDestination(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"bad name", ".hidden", "NUL", "bad..name"} {
		env := scaffoldEnvironment(t)

		requireNewUsage(t, runNew(t.Context(), []string{name}, env))
	}
}

// TestNewCollisionNeverOverwrites verifies --yes cannot bypass a go.mod
// collision, and the entire plan is blocked rather than partially generated.
func TestNewCollisionNeverOverwrites(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	path := filepath.Join(env.Dir, "go.mod")

	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runNew(t.Context(), []string{".", "--yes"}, env))

	content, err := os.ReadFile(path)

	if err != nil || string(content) != "original" {
		t.Fatalf("go.mod = %q, error = %v", content, err)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 1 {
		t.Fatalf("partial write: %v, %v", entries, err)
	}

	if env.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("collision prompted")
	}
}

// TestNewNonemptyRequiresTerminal checks hidden .git content triggers approval
// based on input-terminal status, not the unrelated stdout-terminal status.
func TestNewNonemptyRequiresTerminal(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.IsTerminal = true

	if err := os.Mkdir(filepath.Join(env.Dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := runNew(t.Context(), []string{"."}, env)

	requireNewUsage(t, err)

	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("missing automation hint: %v", err)
	}
}

// TestNewPromptApproves allows a deliberate yes answer, while preserving the
// user's unrelated files. Output redirection does not disable input approval.
func TestNewPromptApproves(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.InputTerminal = true
	env.Stdin = strings.NewReader("yes\n")

	if err := os.WriteFile(filepath.Join(env.Dir, "notes"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runNew(t.Context(), []string{"."}, env); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(env.Stderr.(*bytes.Buffer).String(), "[y/N]") {
		t.Fatal("missing conservative prompt")
	}
}

// TestNewPromptDeclines treats EOF, unterminated approval, and blank/no input
// identically: no generated file may appear after an unconfirmed request.
func TestNewPromptDeclines(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "yes", "\n", "no\n"} {
		env := scaffoldEnvironment(t)
		env.InputTerminal = true
		env.Stdin = strings.NewReader(input)

		if err := os.Mkdir(filepath.Join(env.Dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}

		requireNewUsage(t, runNew(t.Context(), []string{"."}, env))

		if _, err := os.Stat(filepath.Join(env.Dir, "go.mod")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("decline wrote go.mod: %v", err)
		}
	}
}

// TestNewPromptCancellationReturnsWithoutInput cancels while io.Pipe has no
// input. synctest proves the command returns before the owned pipe is closed,
// without sleeps or timing assertions. Closing that pipe then releases the
// generic-reader goroutine; the bubble cannot finish with a leaked goroutine.
func TestNewPromptCancellationReturnsWithoutInput(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.Mkdir(filepath.Join(env.Dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	synctest.Test(t, func(t *testing.T) {
		input, writer := io.Pipe()
		t.Cleanup(func() {
			input.Close()
			writer.Close()
		})
		env.InputTerminal = true
		env.Stdin = input

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		result := make(chan error, 1)

		go func() {
			result <- runNew(ctx, []string{"."}, env)
		}()

		synctest.Wait()

		if !strings.Contains(env.Stderr.(*bytes.Buffer).String(), "[y/N]") {
			t.Fatal("command did not reach the confirmation prompt")
		}

		cancel()
		synctest.Wait()

		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		default:
			t.Fatal("canceled command is still waiting for stdin")
		}

		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()
	})

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 1 || entries[0].Name() != ".git" {
		t.Fatalf("canceled command changed destination: %v, %v", entries, err)
	}
}

// TestNewYesApprovesNonempty makes approval usable in noninteractive scripts
// without reading stdin. Existing unrelated contents are not overwritten.
func TestNewYesApprovesNonempty(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.Mkdir(filepath.Join(env.Dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runNew(t.Context(), []string{".", "--yes"}, env); err != nil {
		t.Fatal(err)
	}

	if env.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("--yes prompted")
	}
}

// TestNewDryRunDoesNotPromptOrWrite includes a nonempty destination to prove
// planning does not require approval and prints all effective options.
func TestNewDryRunDoesNotPromptOrWrite(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.InputTerminal = true

	if err := os.Mkdir(filepath.Join(env.Dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runNew(t.Context(), []string{".", "--dry-run", "--verbose"}, env); err != nil {
		t.Fatal(err)
	}

	output := env.Stdout.(*bytes.Buffer).String()
	fragments := []string{
		env.Dir, `"module":`, `"yes": false`, "go.mod", `"conflicts": []`,
	}

	for _, fragment := range fragments {
		if !strings.Contains(output, fragment) {
			t.Fatalf("missing %q in %s", fragment, output)
		}
	}

	if env.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("dry run prompted")
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "go.mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote go.mod: %v", err)
	}
}

// TestNewDryRunReportsCollisions returns usage failure after reporting the
// complete plan, so automation can distinguish an actionable plan from success.
func TestNewDryRunReportsCollisions(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.WriteFile(filepath.Join(env.Dir, "go.mod"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runNew(t.Context(), []string{".", "--dry-run", "--verbose"}, env))

	document := (result{stdout: env.Stdout.(*bytes.Buffer).String()}).json(t)

	if conflicts := document["conflicts"].([]any); len(conflicts) != 1 || conflicts[0] != "go.mod" {
		t.Fatalf("missing conflict plan: %s", env.Stdout)
	}
}

// TestNewRejectsSymlinkDestinations covers both a destination link and links
// traversed below it. Neither --yes nor a missing child permits traversal.
func TestNewRejectsSymlinkDestinations(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.Symlink(t.TempDir(), filepath.Join(env.Dir, "linked")); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runNew(t.Context(), []string{"linked", "--yes"}, env))
	requireNewUsage(t, runNew(t.Context(), []string{"linked/child", "--yes"}, env))
}

// TestNewRejectsSymlinkGeneratedParent detects an internal symlink during
// preflight, rather than writing through it after approval.
func TestNewRejectsSymlinkGeneratedParent(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.Symlink(t.TempDir(), filepath.Join(env.Dir, "internal")); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runNew(t.Context(), []string{".", "--yes"}, env))

	if _, err := os.Stat(filepath.Join(env.Dir, "go.mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight wrote go.mod: %v", err)
	}
}

// TestNewRejectsDanglingGeneratedSymlink proves collision detection uses Lstat,
// including links whose targets do not exist.
func TestNewRejectsDanglingGeneratedSymlink(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.Symlink("missing", filepath.Join(env.Dir, "go.mod")); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runNew(t.Context(), []string{".", "--yes"}, env))
}

// TestNewRejectsApplicationSymlink extends no-overwrite protection to the
// executable's main file, even when its link target is absent.
func TestNewRejectsApplicationSymlink(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	directory := filepath.Join(env.Dir, "cmd", "server")

	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("missing", filepath.Join(directory, "main.go")); err != nil {
		t.Fatal(err)
	}

	requireNewUsage(t, runNew(t.Context(), []string{".", "--yes"}, env))

	if _, err := os.Stat(filepath.Join(env.Dir, "go.mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight wrote go.mod: %v", err)
	}
}

// TestNewCanceledContext verifies no directory is created after cancellation.
// Generation checks cancellation before rendering and each filesystem step.
func TestNewCanceledContext(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := runNew(ctx, []string{"absent"}, env)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled command created directory: %v", err)
	}
}

// TestNewDirectHelp exercises family help through the production command route.
// Help never evaluates process settings or inspects filesystem destinations.
func TestNewDirectHelp(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"--help"}, env); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(env.Stdout.(*bytes.Buffer).String(), "arkoren new") {
		t.Fatal("missing local help")
	}
}

// TestNewGeneratedOrdinaryProject builds and tests the emitted ordinary graph
// using public versioned dependencies. It is opt-in outside short tests because
// Go module resolution is an explicit action, never part of scaffolding.
func TestNewGeneratedOrdinaryProject(t *testing.T) {
	if testing.Short() {
		t.Skip("generated-project module setup is excluded from offline short tests")
	}

	t.Parallel()

	testNewGeneratedProject(t)
}

// testNewGeneratedProject resolves the emitted versioned dependencies unchanged.
// Commands run GOWORK=off in the temporary application, never tidy the repo,
// and compile both the runnable command and every emitted package test.
func testNewGeneratedProject(t *testing.T) {
	t.Helper()

	env := scaffoldEnvironment(t)
	args := []string{"hello", "--module", "example.com/hello"}

	if err := runNew(t.Context(), args, env); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(env.Dir, "hello")
	writeNewRuntimeTest(t, destination)

	for _, args := range [][]string{
		{"mod", "tidy"}, {"vet", "./..."}, {"test", "-race", "-count=1", "./..."}, {"build", "./..."},
	} {
		command := exec.CommandContext(t.Context(), "go", args...)

		command.Dir = destination
		command.Env = append(os.Environ(), "GOWORK=off")

		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, output)
		}
	}

	testNewExecutableStartupFailure(t, destination)
}

// testNewExecutableStartupFailure runs the built command with an invalid
// listen address. The executable must propagate a real startup failure to
// exit 1 and report it once through the named application's logger.
func testNewExecutableStartupFailure(t *testing.T, destination string) {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "hello")

	command := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/server")
	command.Dir = destination
	command.Env = append(os.Environ(), "GOWORK=off")

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build executable: %v\n%s", err, output)
	}

	command = exec.CommandContext(t.Context(), binary)
	command.Env = append(os.Environ(), "HTTP_ADDR=invalid-address")

	output, err := command.CombinedOutput()

	exit, ok := errors.AsType[*exec.ExitError](err)

	if !ok || exit.ExitCode() != 1 {
		t.Fatalf("startup error = %v, want exit 1\n%s", err, output)
	}

	if strings.Count(string(output), `"msg":"app failed"`) != 1 || strings.Contains(string(output), "application failed") {
		t.Fatalf("expected one lifecycle failure log and no executable duplicate\n%s", output)
	}

	if !strings.Contains(string(output), `"service":"hello"`) {
		t.Fatalf("failure log does not identify the application\n%s", output)
	}

	if strings.Contains(string(output), `"msg":"http server started"`) {
		t.Fatalf("startup failure logged a successful HTTP address\n%s", output)
	}
}

// TestNewNonexistentDryRunLeavesDestinationAbsent checks even parent
// directories are not created by an apparently successful plan.
func TestNewNonexistentDryRunLeavesDestinationAbsent(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Stdin = io.LimitReader(strings.NewReader("yes\n"), 0)

	if err := runNew(t.Context(), []string{"absent/nested", "--dry-run"}, env); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run created parents: %v", err)
	}
}

// TestScaffoldPlanDryRunRejectsFileParent reproduces a generated go.mod file
// also being needed as the skills directory. Preview must report the conflict
// without creating the missing destination or prompting for approval.
func TestScaffoldPlanDryRunRejectsFileParent(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.InputTerminal = true

	err := runNew(t.Context(), []string{"hello", "--skills-dir", "go.mod", "--dry-run", "--verbose"}, env)

	requireNewUsage(t, err)

	document := (result{stdout: env.Stdout.(*bytes.Buffer).String()}).json(t)
	conflicts := document["conflicts"].([]any)

	if len(conflicts) != 1 || conflicts[0] != "go.mod: generated file is also required as a directory" {
		t.Fatalf("conflicts = %v", conflicts)
	}

	if env.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("preview prompted or emitted a second diagnostic")
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("preview changed destination: %v, %v", entries, err)
	}
}

// TestScaffoldPlanExecutionRejectsFileParent exercises the same invalid plan
// with explicit approval. No early-sorted template or destination directory
// may be written before the generated file/directory conflict is rejected.
func TestScaffoldPlanExecutionRejectsFileParent(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runNew(t.Context(), []string{"hello", "--skills-dir", "go.mod", "--yes"}, env)

	requireNewUsage(t, err)

	if !strings.Contains(err.Error(), "go.mod: generated file is also required as a directory") {
		t.Fatalf("missing plan conflict: %v", err)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid plan partially wrote scaffold: %v, %v", entries, err)
	}
}

// TestScaffoldPlanExecutionRejectsNestedFileParent covers a file below shared
// generated directories, rather than reserving only root template filenames.
// An existing empty destination must remain empty after rejection.
func TestScaffoldPlanExecutionRejectsNestedFileParent(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runNew(t.Context(), []string{".", "--skills-dir", "internal/bootstrap/app.go", "--yes"}, env)

	requireNewUsage(t, err)

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("nested conflict partially wrote scaffold: %v, %v", entries, err)
	}
}

// TestScaffoldPlanDryRunRejectsCaseFoldedFileParent keeps generated file
// semantics portable to case-insensitive filesystems, even when the test
// machine itself distinguishes go.mod from GO.MOD.
func TestScaffoldPlanDryRunRejectsCaseFoldedFileParent(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runNew(t.Context(), []string{"hello", "--skills-dir", "GO.MOD", "--dry-run", "--verbose"}, env)

	requireNewUsage(t, err)

	document := (result{stdout: env.Stdout.(*bytes.Buffer).String()}).json(t)
	conflicts := document["conflicts"].([]any)

	if len(conflicts) != 1 || conflicts[0] != "go.mod: generated file is also required as a directory" {
		t.Fatalf("case-folded conflicts = %v", conflicts)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("case-folded preview changed destination: %v, %v", entries, err)
	}
}

// TestScaffoldPlanExecutionRejectsCaseFoldedNestedParent folds every path
// component, not just the filename, before checking file/directory overlap.
// Approval cannot bypass this portable-plan constraint.
func TestScaffoldPlanExecutionRejectsCaseFoldedNestedParent(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runNew(t.Context(), []string{"hello", "--skills-dir", "INTERNAL/BOOTSTRAP/APP.GO", "--yes"}, env)

	requireNewUsage(t, err)

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("case-folded conflict partially wrote scaffold: %v, %v", entries, err)
	}
}

// TestScaffoldPlanAllowsSharedDirectories distinguishes directory reuse from
// file/directory overlap. Skills can live alongside generated bootstrap files.
func TestScaffoldPlanAllowsSharedDirectories(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"hello", "--skills-dir", "internal/bootstrap"}, env); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"internal/bootstrap/app.go", "internal/bootstrap/arkoren/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(env.Dir, "hello", filepath.FromSlash(path))); err != nil {
			t.Fatal(err)
		}
	}
}

// TestScaffoldPlanAllowsFileNamePrefixes requires component boundaries when
// checking ancestry. A directory named go.mod-extra does not overlap go.mod.
func TestScaffoldPlanAllowsFileNamePrefixes(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"hello", "--skills-dir", "go.mod-extra", "--dry-run"}, env); err != nil {
		t.Fatal(err)
	}
}

// TestScaffoldPlanAllowsNestedTemplateBasenames rejects real path overlap,
// not every occurrence of a generated basename in an unrelated directory.
func TestScaffoldPlanAllowsNestedTemplateBasenames(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"hello", "--skills-dir", "docs/go.mod", "--dry-run"}, env); err != nil {
		t.Fatal(err)
	}
}

// TestScaffoldPlanAllowsRootSkillsDirectory preserves the supported dot path:
// the skill's arkoren directory can be a sibling of root application files.
func TestScaffoldPlanAllowsRootSkillsDirectory(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runNew(t.Context(), []string{"hello", "--skills-dir", ".", "--dry-run"}, env); err != nil {
		t.Fatal(err)
	}
}
