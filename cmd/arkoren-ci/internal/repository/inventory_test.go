package repository_test

import (
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"core", "cmd/arkoren", "cmd/arkoren-ci", "driver/example", "foundation/example"} {
		write(t, root, directory+"/go.mod", "module arkoren.dev/"+directory+"\n\ngo 1.27\n")
	}
	write(t, root, "driver/example/compose.yaml", "services: {}\n")
	write(t, root, ".github/release-modules.json", `{"schema":1,"public":["core","cmd/arkoren","driver/example","foundation/example"],"development":["cmd/arkoren-ci"]}`)

	return root
}

func write(t *testing.T, root, name, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	data, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}

	return strings.TrimSpace(string(data))
}

func snapshot(t *testing.T, root string) string {
	t.Helper()
	git(t, root, "init", "-b", "main")
	git(t, root, "add", ".")
	git(t, root, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "chore: fixture")

	return git(t, root, "rev-parse", "HEAD")
}

func TestInventoryClassifiesDevelopmentOutsidePublicReleases(t *testing.T) {
	root := fixture(t)
	result, err := repository.Inspect(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Modules) != 5 || !slices.Equal(result.Integration, []string{"driver/example"}) {
		t.Fatalf("unexpected inventory: %+v", result)
	}
	if result.Modules[1].Directory != "cmd/arkoren-ci" || result.Modules[1].Classification != "development" {
		t.Fatalf("development boundary lost: %+v", result.Modules)
	}
}

func TestWorkingInventoryIncludesIgnoredModulesButCommitDoesNot(t *testing.T) {
	root := fixture(t)
	write(t, root, ".gitignore", "driver/local/\n")
	sha := snapshot(t, root)
	write(t, root, "driver/local/go.mod", "module arkoren.dev/driver/local\n")
	working, err := repository.Inspect(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	// Committed inspection must also ignore a broken local policy.
	write(t, root, ".github/release-modules.json", "broken")
	committed, err := repository.Inspect(context.Background(), root, sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(working.Modules) != 6 || len(committed.Modules) != 5 {
		t.Fatalf("wrong source boundary: working=%+v committed=%+v", working, committed)
	}
}

func TestCommittedNewPublicModuleRequiresPolicyReview(t *testing.T) {
	root := fixture(t)
	write(t, root, "driver/new/go.mod", "module arkoren.dev/driver/new\n")
	sha := snapshot(t, root)
	if _, err := repository.Inspect(context.Background(), root, sha); err == nil {
		t.Fatal("unapproved committed public module accepted")
	}
}

func TestUnknownCommandRequiresExplicitBoundaryReview(t *testing.T) {
	root := fixture(t)
	write(t, root, "cmd/unknown/go.mod", "module arkoren.dev/cmd/unknown\n")
	if _, err := repository.Inspect(context.Background(), root, ""); err == nil {
		t.Fatal("unknown command accepted")
	}
}

func TestLinkedModuleFailsClosed(t *testing.T) {
	root := fixture(t)
	if err := os.Symlink(filepath.Join(root, "driver/example"), filepath.Join(root, "driver/link")); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Inspect(context.Background(), root, ""); err == nil {
		t.Fatal("linked module accepted")
	}
}

func TestLinkedPolicyFailsBeforeReadingIt(t *testing.T) {
	root := fixture(t)
	policy := filepath.Join(root, ".github/release-modules.json")
	if err := os.Remove(policy); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", policy); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Inspect(context.Background(), root, ""); err == nil {
		t.Fatal("linked policy accepted")
	}
}

func TestCommittedLinkedManifestFailsClosed(t *testing.T) {
	root := fixture(t)
	name := filepath.Join(root, "core/go.mod")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../cmd/arkoren/go.mod", name); err != nil {
		t.Fatal(err)
	}
	sha := snapshot(t, root)
	if _, err := repository.Inspect(context.Background(), root, sha); err == nil {
		t.Fatal("committed linked manifest accepted")
	}
}

func TestManifestMustMatchDirectory(t *testing.T) {
	root := fixture(t)
	write(t, root, "driver/example/go.mod", "module arkoren.dev/core\n")
	if _, err := repository.Inspect(context.Background(), root, ""); err == nil {
		t.Fatal("duplicate namespace accepted")
	}
}

func TestMissingApprovedModuleFailsClosed(t *testing.T) {
	root := fixture(t)
	if err := os.Remove(filepath.Join(root, "cmd/arkoren-ci/go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Inspect(context.Background(), root, ""); err == nil {
		t.Fatal("missing development manifest accepted")
	}
}

func TestNestedCoreModuleFailsClosed(t *testing.T) {
	root := fixture(t)
	write(t, root, "core/nested/go.mod", "module private\n")
	if _, err := repository.Inspect(context.Background(), root, ""); err == nil {
		t.Fatal("nested core module accepted")
	}
}

func TestFullRepositoryUsesOnePublicPolicy(t *testing.T) {
	root := filepath.Clean("../../../..")
	data, err := os.ReadFile(filepath.Join(root, ".github/release-modules.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := repository.ParsePolicy(data)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.Inspect(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	public := []string{}
	for _, module := range result.Modules {
		if module.Classification == "public" {
			public = append(public, module.Directory)
		}
	}
	slices.Sort(policy.Public)
	if !slices.Equal(public, policy.Public) || len(public) != 44 || len(result.Modules) != 45 || len(result.Integration) != 15 {
		t.Fatalf("unexpected complete inventory: %+v", result)
	}
	// Every non-standard dependency must belong to this development module.
	dependencies, err := exec.Command("go", "-C", "../..", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./...").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dependency := range strings.Fields(string(dependencies)) {
		if dependency != "arkoren.dev/cmd/arkoren-ci" && !strings.HasPrefix(dependency, "arkoren.dev/cmd/arkoren-ci/") {
			t.Fatalf("non-standard dependency: %s", dependency)
		}
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatal(err)
	}
}
