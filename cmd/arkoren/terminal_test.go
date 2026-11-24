package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/remote"
)

// revisionPattern matches a bundled skill revision as the text presentation
// shortens it, which changes with the documentation and is not what these tests
// pin.
var revisionPattern = regexp.MustCompile(`\b[0-9a-f]{12}\b`)

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
		"\n  require  arkoren.dev/ark/iam@v0.6.1\n",
		"\n\nNext steps\n  1. Create the LinkSigner key, then set LINK_KEY_FILE=link.key in .env;",
		"\n  5. Start the server: ",
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
		!strings.HasPrefix(lines[0], "→ Would create project example.com/shop in shop (") ||
		lines[1] != "" ||
		lines[2] != "Module       example.com/shop" ||
		lines[3] != "Destination  shop" ||
		!strings.HasPrefix(lines[4], "File count   ") {
		t.Fatalf("got:\n%s", got)
	}
}

// TestSkillsStatusTextOutputListsSkills says whether the skills are installed
// and prints one block per skill.
func TestSkillsStatusTextOutputListsSkills(t *testing.T) {
	t.Parallel()

	got := terminalText(t, scaffoldEnvironment(t), "skills", "status")

	want := `✗ arkoren skill is not installed

Skills
  NAME               STATE          DIRECTORY                         BUNDLED REVISION  MODIFIED FILE COUNT
  arkoren            not installed  .agents/skills/arkoren            REV      0
  arkoren-structure  not installed  .agents/skills/arkoren-structure  REV      0

Problems
  FIELD              MESSAGE
  arkoren            not installed
  arkoren-structure  not installed

Next steps
  1. Install the skills
        arkoren skills install
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

Problems
  RULE                 PATH                   LINE  MESSAGE
  banned-package-name  internal/util/util.go  1     package path internal/util uses the role name util; name the package for its purpose
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

Kind  shared
Path  k.key
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

Certificate  r.pem
Key          rk.pem
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

// terminalError runs one command with stderr as an uncolored terminal and
// returns what it printed there, with the working directory written as DIR.
func terminalError(t *testing.T, env console.Environment, args ...string) string {
	t.Helper()

	env.ErrorTerminal = true
	env.Env = func(string) (string, bool) {
		return "", false
	}

	stderr := env.Stderr.(*bytes.Buffer)
	stderr.Reset()

	run(t.Context(), append([]string{"--no-color"}, args...), env)

	return strings.ReplaceAll(stderr.String(), env.Dir, "DIR")
}

// requireText fails unless got is exactly want.
func requireText(t *testing.T, got, want string) {
	t.Helper()

	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestNewTextOutputListsNextSteps pins an action's closing steps: each has a
// sentence, and the aligned pasteable command that does it.
func TestNewTextOutputListsNextSteps(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	got := terminalText(t, env, "new", "shop", "--module", "example.com/shop", "--skip-tidy", "--no-agent-files")

	requireText(t, got, `✓ Created project example.com/shop in shop

Module       example.com/shop
Destination  shop
File count   16

Next steps
  1. Enter the project
        cd DIR/shop
  2. Fetch the dependencies
        go mod tidy
  3. Start the application
        make run
`)
}

// TestNoURLErrorText gives the cause a type and the remedies as steps.
func TestNoURLErrorText(t *testing.T) {
	t.Parallel()

	got := terminalError(t, scaffoldEnvironment(t), "--env-file", "", "http:routes")

	requireText(t, got, `✗ no inspector URL is configured

Type  not_configured

Next steps
  1. Pass the endpoint with --url
  2. Or set ARKOREN_MCP_URL in your shell or ./.env
`)
}

// TestUnreachableErrorText splits the advice into the three things to try.
func TestUnreachableErrorText(t *testing.T) {
	t.Parallel()

	rg := newRig(t)
	rg.env[remote.EnvURL] = deadURL(t)

	got := rg.runWith(t.Context(), runOptions{errorTerminal: true}, "--no-color", "public:routes").stderr

	for _, want := range []string{
		"✗ cannot reach the inspector at " + rg.env[remote.EnvURL],
		"\n\nType  unreachable\n\nNext steps\n  1. Start the admin server\n  2. Check --url or ARKOREN_MCP_URL\n  3. Forward the admin port when the app runs on another machine\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output lacks %q:\n%s", want, got)
		}
	}
}

// TestUnauthorizedErrorText turns the rejected credentials into one step.
func TestUnauthorizedErrorText(t *testing.T) {
	t.Parallel()

	rg := newRig(t, func(inspector *fixtureBuilder) {
		inspector.Token("secret")
	})

	got := rg.runWith(t.Context(), runOptions{errorTerminal: true}, "--no-color", "public:routes").stderr

	requireText(t, got, `✗ unauthorized

Type    unauthorized
Status  401

Next steps
  1. Pass --token, or set ARKOREN_MCP_TOKEN in your shell or ./.env
`)
}

// TestInvalidInputErrorText ends with the help of the operation that was
// misused, as a pasteable command.
func TestInvalidInputErrorText(t *testing.T) {
	t.Parallel()

	got := newRig(t).runWith(t.Context(), runOptions{errorTerminal: true}, "--no-color", "billing:record-payment", "INV", "0").stderr

	for _, want := range []string{
		"✗ Invalid input\n\nType  invalid_input\n",
		"\nNext steps\n  1. See usage\n        arkoren billing:record-payment --help\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output lacks %q:\n%s", want, got)
		}
	}
}

// TestUsageErrorText names the argument at fault and points at the usage.
func TestUsageErrorText(t *testing.T) {
	t.Parallel()

	got := terminalError(t, scaffoldEnvironment(t), "key", "check", "k.pem")

	requireText(t, got, `✗ missing argument KIND: accepted values are shared, signing, exchange

Type  usage

Next steps
  1. See usage
        arkoren key check --help
`)
}

// TestUnknownFlagErrorNamesTheFlag lists what the command accepts.
func TestUnknownFlagErrorNamesTheFlag(t *testing.T) {
	t.Parallel()

	got := terminalError(t, scaffoldEnvironment(t), "key", "generate", "shared", "--force")

	requireText(t, got, `✗ unknown flag --force for arkoren key generate: accepted flags are --out

Type  usage

Next steps
  1. See usage
        arkoren key generate --help
`)
}

// TestUnknownFlagErrorHidesLongTokens keeps a credential pasted where a flag
// belongs out of the diagnostic.
func TestUnknownFlagErrorHidesLongTokens(t *testing.T) {
	t.Parallel()

	token := strings.Repeat("sk-live-0123456789", 3)
	got := terminalError(t, scaffoldEnvironment(t), "key", "generate", "shared", "--"+token)

	if strings.Contains(got, token) || !strings.Contains(got, "unknown flag (an unrecognized long token)") {
		t.Fatalf("got:\n%s", got)
	}
}

// TestCertificateWarningText shows a warning between the changes and the steps,
// and the one command that resolves it as a step.
func TestCertificateWarningText(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	got := terminalText(t, env, "certificate", "root", "generate", "--name", "Dev", "--cert", "r.pem", "--key", "rk.pem")

	requireText(t, got, `✓ Generated root certificate

Changes
  create  r.pem
  create  rk.pem  private key

! System trust was not changed

Next steps
  1. Distribute only the certificate and keep the private key private
  2. Trust the root on this machine
        arkoren certificate root trust DIR/r.pem
`)
}

// TestCertificateCheckFailingText marks a key that does not match the
// certificate with a red cross and the problem, not a bare error.
func TestCertificateCheckFailingText(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	terminalText(t, env, "certificate", "root", "generate", "--name", "A", "--cert", "a.pem", "--key", "a-key.pem")
	terminalText(t, env, "certificate", "root", "generate", "--name", "B", "--cert", "b.pem", "--key", "b-key.pem")

	got := terminalText(t, env, "certificate", "check", "--cert", "a.pem", "--key", "b-key.pem")

	requireText(t, got, `✗ Certificate material is invalid

Certificate  a.pem
Key          b-key.pem
Not checked  expiry, trust

Problems
  FIELD  PATH       MESSAGE
  key    b-key.pem  not a valid private key for this certificate
`)
}

// TestKeyCheckFailingText reports invalid key material as a failed check.
func TestKeyCheckFailingText(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	writeProjectFile(t, env, "bad.key", "not a key")

	got := terminalText(t, env, "key", "check", "shared", "bad.key")

	requireText(t, got, `✗ Invalid shared key

Kind  shared
Path  bad.key

Problems
  FIELD  PATH     MESSAGE
  file   bad.key  not valid shared key material
`)
}

// TestSkillsDryRunPredictsConflicts says in the summary that the real run would
// fail, lists the blocking file and exits like the real run.
func TestSkillsDryRunPredictsConflicts(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	run(t.Context(), []string{"skills", "install", "--compact"}, env)
	writeProjectFile(t, env, ".agents/skills/arkoren/SKILL.md", "mine\n")

	got := terminalText(t, env, "skills", "update", "--dry-run")

	if !strings.HasPrefix(got, "✗ Would fail: 1 skill has conflicting files\n") ||
		!strings.Contains(got, "  arkoren  .agents/skills/arkoren/SKILL.md  modified since installation\n") ||
		!strings.Contains(got, "\nNext steps\n  1. Revert or move aside the conflicting files") {
		t.Fatalf("got:\n%s", got)
	}

	if code := run(t.Context(), []string{"skills", "update", "--dry-run"}, env); code != console.ExitFailure {
		t.Fatalf("exit = %d, want %d like the real run", code, console.ExitFailure)
	}
}

// TestSkillsStatusOKTextOutput is a green headline when nothing is wrong.
func TestSkillsStatusOKTextOutput(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	run(t.Context(), []string{"skills", "install", "--compact"}, env)

	if got := terminalText(t, env, "skills", "status"); !strings.HasPrefix(got, "✓ Skills are up to date\n") || strings.Contains(got, "Problems") {
		t.Fatalf("got:\n%s", got)
	}
}

// TestBareGroupsPrintHelp prints every command group's help to stdout and
// exits 0, instead of failing with a usage error.
func TestBareGroupsPrintHelp(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"add"}, {"add", "ark"}, {"key"}, {"certificate"}, {"certificate", "root"}, {"skills"}} {
		env := scaffoldEnvironment(t)
		code := run(t.Context(), args, env)
		stdout := env.Stdout.(*bytes.Buffer).String()

		if code != console.ExitOK || env.Stderr.(*bytes.Buffer).Len() != 0 ||
			!strings.Contains(stdout, "arkoren "+strings.Join(args, " ")) || !strings.Contains(stdout, "COMMANDS:") {
			t.Fatalf("%v: exit = %d; stdout = %s; stderr = %s", args, code, stdout, env.Stderr)
		}
	}
}

// TestUnknownSubcommandIsUsageError lists what a group accepts.
func TestUnknownSubcommandIsUsageError(t *testing.T) {
	t.Parallel()

	got := terminalError(t, scaffoldEnvironment(t), "key", "rotate")

	requireText(t, got, `✗ unknown command "rotate" for arkoren key: accepted commands are generate, check, public

Type  usage

Next steps
  1. See usage
        arkoren key --help
`)
}

// TestUnknownCommandWithoutCatalogIsUsageError reports the word, not the
// missing connection, and lists how to connect and what exists locally.
func TestUnknownCommandWithoutCatalogIsUsageError(t *testing.T) {
	t.Parallel()

	got := terminalError(t, scaffoldEnvironment(t), "--env-file", "", "foo")

	requireText(t, got, `✗ unknown command "foo"

Type  usage

Next steps
  1. Pass the endpoint with --url
  2. Or set ARKOREN_MCP_URL in your shell or ./.env
  3. List the local commands
        arkoren --help
`)
}

// TestToolsNotConfiguredHoistsSteps keeps the nested error data but prints the
// steps at the top level, like every other document.
func TestToolsNotConfiguredHoistsSteps(t *testing.T) {
	t.Parallel()

	got := terminalText(t, scaffoldEnvironment(t), "--env-file", "", "tools")

	requireText(t, got, `Inspector
  State  not configured

  Error
    Message  no inspector URL is configured
    Type     not_configured

Next steps
  1. Pass the endpoint with --url
  2. Or set ARKOREN_MCP_URL in your shell or ./.env
`)
}

// TestTrustWarningText prints the stderr warning document as a marked line with
// its details as bullets.
func TestTrustWarningText(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	err := console.WriteJSON(&stderr, map[string]any{"warnings": []console.Warning{{
		Message: "System-wide root trust change.",
		Details: []string{"Action: trust", "Target: store"},
	}}}, console.Style{Text: true})

	if err != nil || stderr.String() != "! System-wide root trust change.\n  - Action: trust\n  - Target: store\n" {
		t.Fatalf("got %q: %v", stderr.String(), err)
	}
}

// jsonOutput runs one command with every stream redirected, so the documents
// are JSON, and returns its exit code with stdout and stderr.
func jsonOutput(t *testing.T, env console.Environment, args ...string) (int, string, string) {
	t.Helper()

	env.Env = func(string) (string, bool) {
		return "", false
	}

	stdout := env.Stdout.(*bytes.Buffer)
	stderr := env.Stderr.(*bytes.Buffer)

	stdout.Reset()
	stderr.Reset()

	code := run(t.Context(), append([]string{"--compact"}, args...), env)

	return code, stdout.String(), stderr.String()
}

// TestErrorDocumentJSONShape pins the stderr document: the error with its type,
// then the steps beside it, with no hint member anywhere.
func TestErrorDocumentJSONShape(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := jsonOutput(t, scaffoldEnvironment(t), "--env-file", "", "http:routes")

	want := `{"error":{"message":"no inspector URL is configured","type":"not_configured"},` +
		`"next_steps":[{"text":"Pass the endpoint with --url"},{"text":"Or set ARKOREN_MCP_URL in your shell or ./.env"}]}` + "\n"

	if code != console.ExitUsage || stdout != "" || stderr != want {
		t.Fatalf("exit = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
}

// TestUsageErrorJSONShape names the argument and offers the usage as a command.
func TestUsageErrorJSONShape(t *testing.T) {
	t.Parallel()

	code, _, stderr := jsonOutput(t, scaffoldEnvironment(t), "key", "check", "k.pem")

	want := `{"error":{"message":"missing argument KIND: accepted values are shared, signing, exchange","type":"usage"},` +
		`"next_steps":[{"text":"See usage","command":"arkoren key check --help"}]}` + "\n"

	if code != console.ExitUsage || stderr != want {
		t.Fatalf("exit = %d; stderr = %q", code, stderr)
	}
}

// TestCheckJSONAlwaysReportsOKAndProblems covers a clean and a failing layout
// check: ok and problems are present either way, and the verdict is the exit.
func TestCheckJSONAlwaysReportsOKAndProblems(t *testing.T) {
	t.Parallel()

	env := scaffoldedProject(t)

	code, stdout, _ := jsonOutput(t, env, "check")

	if code != console.ExitOK || stdout != `{"summary":"No layout violations","ok":true,"problems":[]}`+"\n" {
		t.Fatalf("clean check: exit = %d; stdout = %q", code, stdout)
	}

	writeProjectFile(t, env, "internal/util/util.go", "package util\n")

	code, stdout, _ = jsonOutput(t, env, "check")

	want := `{"summary":"1 layout violation","ok":false,"problems":[{"rule":"banned-package-name","path":"internal/util/util.go","line":1,` +
		`"message":"package path internal/util uses the role name util; name the package for its purpose"}]}` + "\n"

	if code != console.ExitFailure || stdout != want {
		t.Fatalf("failing check: exit = %d; stdout = %q", code, stdout)
	}
}

// TestKeyAndCertificateChecksJSONShape reports invalid material as ok false
// with a problem, in the same envelope as every check.
func TestKeyAndCertificateChecksJSONShape(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	writeProjectFile(t, env, "bad.key", "x")
	writeProjectFile(t, env, "bad.pem", "x")

	code, stdout, _ := jsonOutput(t, env, "key", "check", "shared", "bad.key")
	key := (result{stdout: stdout}).json(t)

	if code != console.ExitFailure || key["ok"] != false || key["kind"] != "shared" || len(key["problems"].([]any)) != 1 {
		t.Fatalf("key check: exit = %d; %s", code, stdout)
	}

	code, stdout, _ = jsonOutput(t, env, "certificate", "check", "--cert", "bad.pem")
	cert := (result{stdout: stdout}).json(t)

	if code != console.ExitFailure || cert["ok"] != false || len(cert["problems"].([]any)) != 1 || cert["not_checked"] == nil {
		t.Fatalf("certificate check: exit = %d; %s", code, stdout)
	}
}

// TestSkillsStatusJSONShape is a check: ok, problems and the step that fixes
// them, with no counters or exit-code surprises.
func TestSkillsStatusJSONShape(t *testing.T) {
	t.Parallel()

	code, stdout, _ := jsonOutput(t, scaffoldEnvironment(t), "skills", "status")
	status := (result{stdout: stdout}).json(t)

	steps := status["next_steps"].([]any)

	if code != console.ExitFailure || status["ok"] != false || len(status["problems"].([]any)) != 2 || len(steps) != 1 ||
		steps[0].(map[string]any)["command"] != "arkoren skills install" {
		t.Fatalf("exit = %d; %s", code, stdout)
	}
}

// TestSkillsInstallReportsChangesNotCounters lists the files with --verbose as
// create changes and keeps counters out of every entry.
func TestSkillsInstallReportsChangesNotCounters(t *testing.T) {
	t.Parallel()

	code, stdout, _ := jsonOutput(t, scaffoldEnvironment(t), "--verbose", "skills", "install")
	report := (result{stdout: stdout}).json(t)
	changes := report["changes"].([]any)

	if code != console.ExitOK || len(changes) == 0 || changes[0].(map[string]any)["action"] != "create" ||
		!strings.HasPrefix(changes[0].(map[string]any)["path"].(string), ".agents/skills/") {
		t.Fatalf("exit = %d; changes = %v", code, changes)
	}

	for _, counter := range []string{"written_files", "removed_files", "conflict_count"} {
		if strings.Contains(stdout, counter) {
			t.Fatalf("report still has %s", counter)
		}
	}
}

// TestCertificateJSONHasWarningsAndSteps separates what was left alone from
// what to do: trust is a warning, installing it is a step with its command.
func TestCertificateJSONHasWarningsAndSteps(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	code, stdout, _ := jsonOutput(t, env, "certificate", "root", "generate", "--name", "Dev", "--cert", "r.pem", "--key", "rk.pem")
	report := (result{stdout: stdout}).json(t)
	steps := report["next_steps"].([]any)
	warnings := report["warnings"].([]any)

	if code != console.ExitOK || len(warnings) != 1 || warnings[0].(map[string]any)["message"] != "System trust was not changed" ||
		steps[len(steps)-1].(map[string]any)["command"] != "arkoren certificate root trust "+filepath.Join(env.Dir, "r.pem") {
		t.Fatalf("exit = %d; %s", code, stdout)
	}

	for _, step := range steps {
		if strings.Contains(step.(map[string]any)["text"].(string), "System trust") {
			t.Fatalf("warning text leaked into the steps: %s", stdout)
		}
	}
}

// TestNewJSONHasStepObjects gives each step a text and, when one pasteable
// command does it, that command.
func TestNewJSONHasStepObjects(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	_, stdout, _ := jsonOutput(t, env, "new", "shop", "--module", "example.com/shop", "--skip-tidy", "--no-agent-files")
	report := (result{stdout: stdout}).json(t)
	steps := report["next_steps"].([]any)

	first := steps[0].(map[string]any)

	if len(steps) != 3 || first["text"] != "Enter the project" || first["command"] != "cd "+filepath.Join(env.Dir, "shop") {
		t.Fatalf("steps = %v", steps)
	}
}

// TestFileConflictJSONHasTypeAndSteps ends a refused overwrite with its type and
// the verbose step, which is the same command with --verbose added.
func TestFileConflictJSONHasTypeAndSteps(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	writeProjectFile(t, env, "go.mod", "original")

	code, _, stderr := jsonOutput(t, env, "new", ".", "--no-agent-files", "--skip-tidy", "--yes")
	document := (result{stdout: stderr}).json(t)
	failure := document["error"].(map[string]any)
	steps := document["next_steps"].([]any)

	if code != console.ExitFailure || failure["type"] != "file_conflict" || failure["hint"] != nil ||
		steps[len(steps)-1].(map[string]any)["command"] != "arkoren --compact new . --no-agent-files --skip-tidy --yes --verbose" {
		t.Fatalf("exit = %d; %s", code, stderr)
	}
}
