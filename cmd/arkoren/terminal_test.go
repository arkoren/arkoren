package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// revisionPattern matches a bundled skill revision, which changes with the
// documentation and is not what these tests pin.
var revisionPattern = regexp.MustCompile(`[0-9a-f]{64}`)

// terminalText runs one command with stdout as an uncolored terminal and
// returns what it printed, with the working directory written as DIR and
// skill revisions as REV, so tests can pin the text a person reads.
func terminalText(t *testing.T, env console.Environment, args ...string) string {
	t.Helper()

	env.IsTerminal = true
	env.Env = func(string) (string, bool) {
		return "", false
	}

	stdout := env.Stdout.(*bytes.Buffer)
	stdout.Reset()

	run(t.Context(), append([]string{"-no-color"}, args...), env)

	text := stdout.String()

	if resolved, err := filepath.EvalSymlinks(env.Dir); err == nil {
		text = strings.ReplaceAll(text, resolved, "DIR")
	}

	return revisionPattern.ReplaceAllString(strings.ReplaceAll(text, env.Dir, "DIR"), "REV")
}

// writeProjectFile creates a file, and its directory, under the test project.
func writeProjectFile(t *testing.T, env console.Environment, path, content string) {
	t.Helper()

	full := filepath.Join(env.Dir, path)

	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAddTextOutputListsChanges reads a feature dry run as a headline and
// aligned changes, with the inserted line as dim detail.
func TestAddTextOutputListsChanges(t *testing.T) {
	t.Parallel()

	got := terminalText(t, scaffoldedProject(t), "add", "feature", "orders", "--web", "--dry-run")

	want := `→ Would add feature orders

Changes
  create  internal/orders/orders.go
  create  internal/orders/service.go
  create  internal/orders/store.go
  create  internal/bootstrap/orders.go
  create  internal/orders/web/web.go
  create  internal/orders/web/problems.go
  edit    internal/bootstrap/server/compose.go  compose.Use(bootstrap.OrdersRoutes)
`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestAddArkTextOutputNumbersNextSteps shows the pinned modules as require
// changes and the steps left to the user as a numbered list.
func TestAddArkTextOutputNumbersNextSteps(t *testing.T) {
	t.Parallel()

	got := terminalText(t, scaffoldedProject(t), "add", "ark", "iam", "--with", iamExampleFeatures, "--dry-run")

	for _, fragment := range []string{
		"→ Would add ark iam\n\nChanges\n  create ",
		"\n  require  arkoren.dev/ark/iam@v0.5.0\n",
		"\n\nNext steps\n   1. add compose.Require(",
		"\n  10. set GITHUB_CLIENT_ID",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("output lacks %q:\n%s", fragment, got)
		}
	}
}

// TestNewTextOutputSummarizesPreview puts the destination and counts under a
// headline that says nothing was written.
func TestNewTextOutputSummarizesPreview(t *testing.T) {
	t.Parallel()

	got := terminalText(t, scaffoldEnvironment(t), "new", "shop", "--module", "example.com/shop", "--dry-run", "--no-agent-files")
	lines := strings.Split(got, "\n")

	if len(lines) != 6 ||
		!strings.HasPrefix(lines[0], "→ Would create project example.com/shop in DIR/shop (") ||
		lines[1] != "" ||
		lines[2] != "Module       example.com/shop" ||
		lines[3] != "Destination  DIR/shop" ||
		!strings.HasPrefix(lines[4], "File count   ") {
		t.Fatalf("got:\n%s", got)
	}
}

// TestSkillsStatusTextOutputListsSkills says whether the skills are installed
// and prints one block per skill.
func TestSkillsStatusTextOutputListsSkills(t *testing.T) {
	t.Parallel()

	got := terminalText(t, scaffoldEnvironment(t), "skills", "status")

	want := `✓ arkoren skill is not installed

Skills
  Name                 arkoren
  State                not installed
  Directory            DIR/.agents/skills/arkoren
  Bundled revision     REV
  Modified file count  0

  Name                 arkoren-structure
  State                not installed
  Directory            DIR/.agents/skills/arkoren-structure
  Bundled revision     REV
  Modified file count  0
`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestCheckTextOutputTabulatesViolations marks the verdict with a red cross and
// prints each violation as a table row.
func TestCheckTextOutputTabulatesViolations(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)
	writeProjectFile(t, env, "internal/util/util.go", "package util\n")

	got := terminalText(t, env, "check")

	want := `✗ 1 layout violation

Violations
  FILE                   LINE  RULE                 MESSAGE
  internal/util/util.go  1     banned-package-name  package path internal/util uses the role name util; name the package for its purpose
`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestCheckTextOutputForACleanProject is one green line.
func TestCheckTextOutputForACleanProject(t *testing.T) {
	t.Parallel()

	if got := terminalText(t, scaffoldedProject(t), "check"); got != "✓ No layout violations\n" {
		t.Fatalf("got %q", got)
	}
}

// TestKeyCheckTextOutputNamesTheKey keeps the category and path under the
// headline.
func TestKeyCheckTextOutputNamesTheKey(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	terminalText(t, env, "key", "generate", "shared", "--out", "k.key")

	got := terminalText(t, env, "key", "check", "shared", "k.key")

	want := `✓ Valid shared key

Category  shared
Path      DIR/k.key
`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestCertificateCheckTextOutputSaysWhatIsNotChecked ends with the verdicts the
// command does not give.
func TestCertificateCheckTextOutputSaysWhatIsNotChecked(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	terminalText(t, env, "certificate", "root", "generate", "--name", "Dev", "--cert", "r.pem", "--key", "rk.pem")

	got := terminalText(t, env, "certificate", "check", "--cert", "r.pem", "--key", "rk.pem")

	want := `✓ Certificate parsed; key matches

Certificate  DIR/r.pem
Key          DIR/rk.pem
Key matched  true
Not checked  expiry, trust
`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestStatusTextOutputNamesTheApplication leads with the connected
// application and flattens where the URL and token came from.
func TestStatusTextOutputNamesTheApplication(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	got := rg.runWith(t.Context(), runOptions{terminal: true}, "-no-color", "status").expectOK(t).stdout

	want := `✓ Connected to shop 1.2.3 (development)

App
  Environment  development
  Name         shop
  State        starting
  Version      1.2.3

Health
  Check count    0
  Failing count  0
  Pending count  0
  Status         ok

Protocol    2026-07-28
Arkoren     test
Extensions  4
URL         ` + rg.url + `
URL source  env
Token set   false
`

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestToolsTextOutputIsATable lists discovered tools in one table even though
// the last column holds long descriptions.
func TestToolsTextOutputIsATable(t *testing.T) {
	t.Parallel()

	got := newRig(t).runWith(t.Context(), runOptions{terminal: true}, "-no-color", "tools").expectOK(t).stdout

	for _, fragment := range []string{
		"Tools\n  COMMAND ",
		"  app:events  ",
		"Recent event names, payload types, and trace IDs; never payload values\n",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("output lacks %q:\n%s", fragment, got)
		}
	}
}
