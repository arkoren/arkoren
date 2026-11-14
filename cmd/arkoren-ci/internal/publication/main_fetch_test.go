package publication

import (
	"context"
	"encoding/json/v2"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
)

// mainClient answers only the remote main ref read, reporting sha.
func mainClient(t *testing.T, root, sha string) github.Client {
	t.Helper()
	return github.Client{Root: root, Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) != 4 || !strings.HasSuffix(args[3], "/git/ref/heads/main") {
			t.Fatalf("unexpected API call: %v", args)
		}
		return json.Marshal(map[string]any{"object": map[string]string{"sha": sha}})
	}}
}

// advanceRemoteMain publishes a commit on top of the batch commit from a
// separate clone, so the publisher checkout never receives its objects.
func advanceRemoteMain(t *testing.T, fixture fixture) string {
	t.Helper()
	command(t, fixture.root, "git", "push", "origin", "main")
	clone := filepath.Join(t.TempDir(), "advance")
	command(t, fixture.root, "git", "clone", fixture.remote, clone)
	command(t, clone, "git", "config", "user.name", "Automation")
	command(t, clone, "git", "config", "user.email", "automation@example.invalid")
	command(t, clone, "git", "config", "commit.gpgsign", "false")
	writeTest(t, clone, "scripts/notes.sh", "# release notes automation")
	command(t, clone, "git", "add", ".")
	command(t, clone, "git", "commit", "-m", "chore: release notes")
	command(t, clone, "git", "push", "origin", "HEAD:main")
	return command(t, clone, "git", "rev-parse", "HEAD")
}

func TestAdvancedMainMissingLocallyIsFetchedAndSignedBatchValidates(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	fixture.push(t, batch)
	advanced := advanceRemoteMain(t, fixture)
	if err := exec.Command("git", "-C", fixture.root, "cat-file", "-e", advanced+"^{commit}").Run(); err == nil {
		t.Fatal("advanced main already local; fixture proves nothing")
	}
	if err := Validate(context.Background(), fixture.root, fixture.trust, advanced, batch); err == nil || strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("missing main must be an operational error, not untrusted history: %v", err)
	}
	main, err := mainSHA(context.Background(), mainClient(t, fixture.root, advanced))
	if err != nil || main != advanced {
		t.Fatalf("mainSHA = %q, %v", main, err)
	}
	if err := Validate(context.Background(), fixture.root, fixture.trust, main, batch); err != nil {
		t.Fatalf("complete signed batch refused after main advanced: %v", err)
	}
}

func TestNonAncestorStillFailsAsUntrustedHistory(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	older := command(t, fixture.root, "git", "rev-parse", "HEAD~1")
	err := Validate(context.Background(), fixture.root, fixture.trust, older, batch)
	if err == nil || err.Error() != "publication SHA is not trusted main history" {
		t.Fatalf("true non-ancestor error = %v", err)
	}
}

func TestMainFetchFailureIsDistinctFromUntrustedHistory(t *testing.T) {
	fixture := newFixture(t)
	unknown := strings.Repeat("a", 40)
	_, err := mainSHA(context.Background(), mainClient(t, fixture.root, unknown))
	if err == nil || !strings.Contains(err.Error(), "cannot fetch remote main "+unknown) || strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("fetch failure error = %v", err)
	}
}
