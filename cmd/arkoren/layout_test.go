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

	if err != nil || report["dry_run"] != true || report["summary"] != "Would add feature orders" || len(changesWith(report, "create")) != 6 {
		t.Fatalf("report = %v, err = %v", report, err)
	}

	edited := changesWith(report, "edit")[0]

	if edited["path"] != "internal/bootstrap/server/compose.go" || edited["detail"] != "compose.Use(bootstrap.OrdersRoutes)" {
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

	if err != nil || report["summary"] != "Added feature orders" || len(changesWith(report, "tidy")) != 0 {
		t.Fatalf("report = %v, err = %v", report, err)
	}

	for _, rel := range []string{"internal/orders/service.go", "internal/orders/web/web.go", "internal/orders/jobs/jobs.go"} {
		if _, err := os.Stat(filepath.Join(env.Dir, rel)); err != nil {
			t.Fatal(err)
		}
	}

	compose, _ := os.ReadFile(filepath.Join(env.Dir, "internal/bootstrap/server/compose.go"))

	if !strings.Contains(string(compose), "compose.Use(bootstrap.OrdersJobs)") {
		t.Fatalf("compose.go:\n%s", compose)
	}
}

// TestAddRefusesExistingFile reports a file conflict and keeps the file.
func TestAddRefusesExistingFile(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	if _, err := runLayout(t, env, "add", "process", "server"); err == nil {
		t.Fatal("overwrote cmd/server")
	} else if exit, ok := errors.AsType[*console.ExitError](err); !ok || exit.Type != console.TypeFileConflict || exit.Code != console.ExitFailure {
		t.Fatalf("error = %v, want a file conflict", err)
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

		exit, _ := errors.AsType[*console.ExitError](err)

		if len(exit.Steps) == 0 || !strings.Contains(exit.Steps[len(exit.Steps)-1].Text, "arkoren-structure") {
			t.Fatalf("%v: steps do not name the skill: %v", args, err)
		}
	}
}

// TestCheckCleanProjectReportsOK prints one document and exits 0.
func TestCheckCleanProjectReportsOK(t *testing.T) {
	t.Parallel()

	report, err := runLayout(t, scaffoldedProject(t), "check")

	if err != nil || report["ok"] != true || report["summary"] != "No layout violations" || len(report["problems"].([]any)) != 0 {
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

	violation := report["problems"].([]any)[0].(map[string]any)

	if report["ok"] != false || report["summary"] != "1 layout violation" || violation["rule"] != "banned-package-name" || violation["path"] != "internal/util/util.go" || violation["line"] != 1.0 {
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

	for _, want := range []string{"feature", "process", "provider", "migration", "ark"} {
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

	if err != nil || len(changesWith(report, "tidy")) != 1 {
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

// iamExampleFeatures is the --with list whose output examples.md shows. The
// example also has the GitHub sign-in client, added with `add ark oauth github`
// before the identity module so that the module wires it; it leaves out google,
// whose startup performs network discovery, so the identity process test in the
// example runs offline.
const iamExampleFeatures = "reset,verification,two-factor,tokens,permissions,account"

// TestAddArkIAMDryRunReportsPlan lists the files, the Compose and .env
// insertions, the pinned modules and the steps left to the user, and writes
// nothing.
func TestAddArkIAMDryRunReportsPlan(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	report, err := runLayout(t, env, "add", "ark", "iam", "--with", iamExampleFeatures, "--dry-run")

	if err != nil || report["dry_run"] != true {
		t.Fatalf("report = %v, err = %v", report, err)
	}

	edits := changesWith(report, "edit")

	if report["summary"] != "Would add ark iam" || len(edits) != 11 || edits[0]["path"] != "internal/bootstrap/server/compose.go" ||
		edits[0]["detail"] != "compose.Use(bootstrap.Migrate)" || edits[1]["detail"] != "compose.Use(bootstrap.IAMRouter)" {
		t.Fatalf("report = %v", report)
	}

	if last := edits[len(edits)-1]; last["path"] != ".env.example" || last["detail"] != "TWO_FACTOR_KEY=file:.secrets/two-factor-key" {
		t.Fatalf("last edit = %v", last)
	}

	if requires := changesWith(report, "require"); requires[0]["path"] != "arkoren.dev/ark/iam@v0.8.0" {
		t.Fatalf("requires = %v", requires)
	}

	steps := report["next_steps"].([]any)

	if first := steps[0].(map[string]any); !strings.Contains(first["text"].(string), "LINK_KEY=file:.secrets/link-key") ||
		first["command"] != "arkoren key generate shared --out .secrets/link-key" || steps[len(steps)-1].(map[string]any)["command"] != "make run" {
		t.Fatalf("next_steps = %v", steps)
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "internal/bootstrap/iam.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote iam.go: %v", err)
	}
}

// TestAddArkArgumentsAreChecked rejects arguments iam does not take, a missing,
// repeated or unknown OAuth provider and unknown features, among them the
// sign-in providers that moved to add ark oauth.
func TestAddArkArgumentsAreChecked(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	for _, args := range [][]string{
		{"add", "ark", "iam", "extra"}, {"add", "ark", "oauth"}, {"add", "ark", "oauth", "google", "google"},
		{"add", "ark", "iam", "--with", "passkeys"}, {"add", "ark", "iam", "--with", "google"}, {"add", "ark", "oauth", "myspace"},
	} {
		_, err := runLayout(t, env, args...)
		requireNewUsage(t, err)
	}
}

// TestExamplesMatchAddArkOutput compares every identity file of the arkoren-structure
// examples with what `arkoren add ark iam` writes, so the example agents copy can
// never drift from the generator.
func TestExamplesMatchAddArkOutput(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	for _, args := range [][]string{{"add", "ark", "oauth", "github"}, {"add", "ark", "iam", "--with", iamExampleFeatures}} {
		if _, err := runLayout(t, env, append(args, "--skip-tidy")...); err != nil {
			t.Fatal(err)
		}
	}

	examples, err := skillSource.ReadFile("skills/arkoren-structure/references/examples.md")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"iam", "password_reset", "email_verification", "token_scopes", "roles", "account_view", "github", "github_sign_up",
		"hasher", "cache", "secrets", "link_signer", "two_factor_key", "oauth_key", "mailer", "migrator",
	} {
		rel := "internal/bootstrap/" + name + ".go"
		generated, err := os.ReadFile(filepath.Join(env.Dir, rel))
		if err != nil {
			t.Fatal(err)
		}

		if shown := exampleFile(t, string(examples), rel); shown != string(generated) {
			t.Fatalf("examples.md shows %s differently from `add ark iam`:\n--- example\n%s\n--- generated\n%s", rel, shown, generated)
		}
	}
}

// exampleFile returns the Go block that examples shows under the heading line
// `rel`, which is a path in backticks on a line of its own.
func exampleFile(t *testing.T, examples, rel string) string {
	t.Helper()

	_, after, found := strings.Cut(examples, "\n`"+rel+"`\n\n```go\n")
	if !found {
		t.Fatalf("examples.md has no block for %s", rel)
	}

	block, _, found := strings.Cut(after, "```\n")
	if !found {
		t.Fatalf("examples.md block for %s is not closed", rel)
	}

	return block
}

// TestAddArkIAMGeneratedProject builds the identity module's generated providers
// against this checkout's modules, vets them, requires `check` to stay clean, and
// then runs the process test of examples.md: the server applies the migrations
// at startup, the identity routes under /auth serve a registration, the identity
// job sends the verification email through a mailer the test supplies, and the
// emailed link verifies the address. It needs the Go
// toolchain and resolves third-party modules, so short mode skips it.
func TestAddArkIAMGeneratedProject(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a generated project")
	}

	env := scaffoldedProject(t)

	for _, args := range [][]string{
		{"add", "ark", "oauth", "github"},
		{"add", "ark", "iam", "--with", iamExampleFeatures},
	} {
		if _, err := runLayout(t, env, append(args, "--skip-tidy")...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	examples, err := skillSource.ReadFile("skills/arkoren-structure/references/examples.md")
	if err != nil {
		t.Fatal(err)
	}

	test := exampleFile(t, string(examples), "internal/bootstrap/server/iam_test.go")

	if err := os.WriteFile(filepath.Join(env.Dir, "internal/bootstrap/server/iam_test.go"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}

	useCheckoutModules(t, env.Dir)

	for _, args := range [][]string{{"build", "-mod=readonly", "./..."}, {"vet", "./..."}, {"test", "-race", "-count=1", "./..."}} {
		command := exec.CommandContext(t.Context(), "go", args...)
		command.Dir = env.Dir
		command.Env = append(os.Environ(), "GOWORK=off")

		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, output)
		}
	}

	report, err := runLayout(t, env, "check")

	if err != nil || report["ok"] != true {
		t.Fatalf("check after add ark: %v, %v", report, err)
	}
}

// changesWith returns the changes of one action from a decoded result.
func changesWith(report map[string]any, action string) []map[string]any {
	var matching []map[string]any

	changes, _ := report["changes"].([]any)

	for _, change := range changes {
		if fields := change.(map[string]any); fields["action"] == action {
			matching = append(matching, fields)
		}
	}

	return matching
}
