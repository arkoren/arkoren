package main

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// scaffoldedProject creates a project named shop with `arkoren new --skip-tidy`
// and returns an environment whose working directory is that project, so the
// layout commands run exactly as a user would run them in the project root.
func scaffoldedProject(t *testing.T) console.Environment {
	t.Helper()

	env := scaffoldEnvironment(t)

	if err := execute(t.Context(), []string{"new", "shop", "--module", "example.com/shop", "--skip-tidy", "--no-agent-files"}, env); err != nil {
		t.Fatal(err)
	}

	env.Dir = filepath.Join(env.Dir, "shop")
	env.Stdout.(*bytes.Buffer).Reset()

	return env
}

// runLayout runs one layout command and returns the JSON it printed.
func runLayout(t *testing.T, env console.Environment, args ...string) (map[string]any, error) {
	t.Helper()

	stdout := env.Stdout.(*bytes.Buffer)
	stdout.Reset()

	err := execute(t.Context(), args, env)

	var report map[string]any

	if stdout.Len() > 0 && json.Unmarshal(stdout.Bytes(), &report) != nil {
		t.Fatalf("stdout is not JSON: %q", stdout.String())
	}

	return report, err
}

// TestAddFeatureDryRunWritesNothing reports the plan without touching files.
func TestAddFeatureDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)
	before, err := os.ReadFile(filepath.Join(env.Dir, "internal/bootstrap/server/compose.go"))

	if err != nil {
		t.Fatal(err)
	}

	report, err := runLayout(t, env, "add", "feature", "orders", "--web", "--dry-run")

	if err != nil || report["dry_run"] != true || len(report["created"].([]any)) != 6 {
		t.Fatalf("report = %v, err = %v", report, err)
	}

	edited := report["edited"].([]any)[0].(map[string]any)

	if edited["path"] != "internal/bootstrap/server/compose.go" || edited["inserted"].([]any)[0] != "ordersweb.Routes(compose)" {
		t.Fatalf("edited = %v", edited)
	}

	after, _ := os.ReadFile(filepath.Join(env.Dir, "internal/bootstrap/server/compose.go"))

	if _, err := os.Stat(filepath.Join(env.Dir, "internal/orders")); !errors.Is(err, os.ErrNotExist) || !bytes.Equal(before, after) {
		t.Fatalf("dry run changed the project: %v", err)
	}
}

// TestAddFeatureWritesFilesAndRegistration creates the feature and edits Compose.
func TestAddFeatureWritesFilesAndRegistration(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	report, err := runLayout(t, env, "add", "feature", "orders", "--web", "--jobs", "--skip-tidy")

	if err != nil || report["tidied"] != false {
		t.Fatalf("report = %v, err = %v", report, err)
	}

	for _, rel := range []string{"internal/orders/service.go", "internal/orders/web/web.go", "internal/orders/jobs/jobs.go"} {
		if _, err := os.Stat(filepath.Join(env.Dir, rel)); err != nil {
			t.Fatal(err)
		}
	}

	compose, _ := os.ReadFile(filepath.Join(env.Dir, "internal/bootstrap/server/compose.go"))

	if !strings.Contains(string(compose), "ordersjobs.Handlers(compose)") {
		t.Fatalf("compose.go:\n%s", compose)
	}
}

// TestAddRefusesExistingFile reports a usage error and keeps the file.
func TestAddRefusesExistingFile(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	if _, err := runLayout(t, env, "add", "process", "server"); err == nil {
		t.Fatal("overwrote cmd/server")
	} else {
		requireNewUsage(t, err)
	}
}

// TestAddRequiresOneName rejects missing and extra arguments.
func TestAddRequiresOneName(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	for _, args := range [][]string{{"add", "feature"}, {"add", "provider", "a", "b"}} {
		_, err := runLayout(t, env, args...)
		requireNewUsage(t, err)
	}
}

// TestLayoutCommandsRefuseOtherDirectory points at the skill and exits 2.
func TestLayoutCommandsRefuseOtherDirectory(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	for _, args := range [][]string{{"check"}, {"map"}, {"add", "provider", "cache"}} {
		_, err := runLayout(t, env, args...)

		requireNewUsage(t, err)

		if !strings.Contains(err.Error(), "arkoren-structure") {
			t.Fatalf("%v: error does not name the skill: %v", args, err)
		}
	}
}

// TestCheckCleanProjectReportsOK prints one document and exits 0.
func TestCheckCleanProjectReportsOK(t *testing.T) {
	t.Parallel()

	report, err := runLayout(t, scaffoldedProject(t), "check")

	if err != nil || report["ok"] != true || len(report["violations"].([]any)) != 0 {
		t.Fatalf("report = %v, err = %v", report, err)
	}
}

// TestCheckViolationsExitWithFailure prints the violations, then exits 1
// without a second diagnostic.
func TestCheckViolationsExitWithFailure(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	if err := os.MkdirAll(filepath.Join(env.Dir, "internal/util"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(env.Dir, "internal/util/util.go"), []byte("package util\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := runLayout(t, env, "check")
	exit, ok := errors.AsType[*console.ExitError](err)

	if !ok || exit.Code != console.ExitFailure || exit.Err != nil {
		t.Fatalf("err = %v", err)
	}

	violation := report["violations"].([]any)[0].(map[string]any)

	if report["ok"] != false || violation["rule"] != "banned-package-name" || violation["file"] != "internal/util/util.go" || violation["line"] != 1.0 {
		t.Fatalf("report = %v", report)
	}
}

// TestMapPrintsTextAndJSON covers both renderings of the inventory.
func TestMapPrintsTextAndJSON(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)
	report, err := runLayout(t, env, "map")

	if err != nil || report["module"] != "example.com/shop" {
		t.Fatalf("report = %v, err = %v", report, err)
	}

	env.Stdout.(*bytes.Buffer).Reset()

	if err := execute(t.Context(), []string{"map", "--text"}, env); err != nil {
		t.Fatal(err)
	}

	if text := env.Stdout.(*bytes.Buffer).String(); !strings.HasPrefix(text, "module example.com/shop\nprocess server: ") {
		t.Fatalf("text = %q", text)
	}
}

// TestLayoutHelpIsOffline lists the new commands without a project.
func TestLayoutHelpIsOffline(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := execute(t.Context(), []string{"add", "--help"}, env); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"feature", "process", "provider", "migration"} {
		if !strings.Contains(env.Stdout.(*bytes.Buffer).String(), want) {
			t.Fatalf("add help lacks %q:\n%s", want, env.Stdout)
		}
	}
}

// TestLayoutCommandsOnBuiltProject scaffolds a project, applies every kind of
// add, then builds it and requires `check` to stay clean. The first add imports
// foundation modules the scaffold does not require, so it must run go mod tidy;
// the rest skip it because the build below tidies against the checkout. It needs
// the Go toolchain and resolves third-party modules, so short mode skips it.
func TestLayoutCommandsOnBuiltProject(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a generated project")
	}

	env := scaffoldedProject(t)

	report, err := runLayout(t, env, "add", "feature", "catalog", "--web")

	if err != nil || report["tidied"] != true {
		t.Fatalf("add without --skip-tidy: report = %v, err = %v", report, err)
	}

	module, _ := os.ReadFile(filepath.Join(env.Dir, "go.mod"))

	if !strings.Contains(string(module), "arkoren.dev/foundation/query ") {
		t.Fatalf("go.mod after tidy lacks foundation/query:\n%s", module)
	}

	for _, args := range [][]string{
		{"add", "feature", "orders", "--web", "--jobs", "--flow", "--tools", "--events", "--schedules"},
		{"add", "feature", "billing", "--web"},
		{"add", "process", "worker"},
		{"add", "feature", "reports", "--schedules", "--process", "worker"},
		{"add", "provider", "mail-queue"},
		{"add", "migration", "create_orders"},
		{"add", "migration", "add-order-status"},
	} {
		if _, err := runLayout(t, env, append(args, "--skip-tidy")...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	useCheckoutModules(t, env.Dir)

	command := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "./...")
	command.Dir = env.Dir
	command.Env = append(os.Environ(), "GOWORK=off")

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build after add: %v\n%s", err, output)
	}

	vet := exec.CommandContext(t.Context(), "go", "vet", "./...")
	vet.Dir = env.Dir
	vet.Env = append(os.Environ(), "GOWORK=off")

	if output, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet after add: %v\n%s", err, output)
	}

	report, err = runLayout(t, env, "check")

	if err != nil || report["ok"] != true {
		t.Fatalf("check after add: %v, %v", report, err)
	}
}
