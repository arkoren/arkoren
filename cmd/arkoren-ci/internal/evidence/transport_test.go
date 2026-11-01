package evidence_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/evidence"
)

func TestGitHubTransportUsesPaginatedReadOnlyMetadataAndSafeEnvironment(t *testing.T) {
	bin, root := t.TempDir(), t.TempDir()
	script := `#!/bin/sh
test "$1" = api || exit 2
test "$2" = --method || exit 2
test "$3" = GET || exit 2
test "$4" = --paginate || exit 2
test "$5" = --slurp || exit 2
test "$6" = repos/arkoren/arkoren/actions/workflows/ci.yml/runs || exit 2
test "$GH_TOKEN" = read-only-test || exit 2
test -z "$APP_TOKEN$SIGNING_KEY$GIT_CLIFF_TOKEN" || exit 2
printf '[{"workflow_runs":[]},{"workflow_runs":[]}]'
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "read-only-test")
	t.Setenv("APP_TOKEN", "must-not-inherit")
	t.Setenv("SIGNING_KEY", "must-not-inherit")
	data, err := evidence.GitHub(root)(context.Background(), "repos/arkoren/arkoren/actions/workflows/ci.yml/runs")
	if err != nil || string(data) != `[{"workflow_runs":[]},{"workflow_runs":[]}]` {
		t.Fatalf("transport: %s %v", data, err)
	}
}

func TestValidateCheckoutRequiresExactSHAAndFetchedBranch(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		command := exec.Command("git", args...)
		command.Dir = root
		data, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", data, err)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "-b", "main")
	git("-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "chore: fixture")
	selected := target()
	selected.SHA = git("rev-parse", "HEAD")
	if err := evidence.ValidateCheckout(context.Background(), root, selected); err == nil {
		t.Fatal("missing fetched branch accepted")
	}
	git("update-ref", "refs/remotes/origin/main", selected.SHA)
	if err := evidence.ValidateCheckout(context.Background(), root, selected); err != nil {
		t.Fatal(err)
	}
	selected.SHA = strings.Repeat("b", 40)
	if err := evidence.ValidateCheckout(context.Background(), root, selected); err == nil {
		t.Fatal("wrong checkout accepted")
	}
}
