package release_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/release"
)

func write(t *testing.T, root, name, data string) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func commit(t *testing.T, root, message string, files map[string]string) string {
	t.Helper()
	for name, data := range files {
		write(t, root, name, data)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", message)
	return git(t, root, "rev-parse", "HEAD")
}

func policy(t *testing.T, root string, modules []string, development []string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"schema": 1, "public": modules, "development": development})
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, ".github/release-modules.json", string(data))
}

func fixture(t *testing.T) (string, []string) {
	t.Helper()
	if os.Getenv("ARKOREN_CLIFF_TEST") != "1" {
		t.Skip("real git-cliff fixtures run with make maintenance-check")
	}
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Fixture")
	git(t, root, "config", "user.email", "fixture@example.invalid")
	git(t, root, "config", "commit.gpgsign", "false")
	git(t, root, "config", "tag.gpgsign", "false")
	modules := []string{"core", "cmd/arkoren", "driver/example", "foundation/example"}
	for _, module := range modules {
		write(t, root, module+"/go.mod", "module arkoren.dev/"+module+"\n")
	}
	policy(t, root, modules, []string{})
	commit(t, root, "chore: baseline", nil)
	for _, module := range modules {
		git(t, root, "tag", module+"/v0.1.0")
	}
	return root, modules
}

func plan(t *testing.T, root string) release.Plan {
	t.Helper()
	result, err := release.Prepare(context.Background(), root, git(t, root, "rev-parse", "HEAD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func entry(t *testing.T, result release.Plan, directory string) release.Entry {
	t.Helper()
	for _, item := range result.Modules {
		if item.Directory == directory {
			return item
		}
	}
	t.Fatalf("missing module %s", directory)
	return release.Entry{}
}

func proposed(t *testing.T, result release.Plan, directory, version string) release.Entry {
	t.Helper()
	item := entry(t, result, directory)
	if item.NextVersion == nil || *item.NextVersion != version {
		t.Fatalf("unexpected proposal: %+v", item)
	}
	return item
}

func rejected(t *testing.T, root, diagnostic string, initial ...string) {
	t.Helper()
	_, err := release.Prepare(context.Background(), root, git(t, root, "rev-parse", "HEAD"), initial)
	if err == nil || !strings.Contains(err.Error(), diagnostic) {
		t.Fatalf("wanted %q, got %v", diagnostic, err)
	}
}

func TestPlanSharedFortyFourBaselineIsRepeatable(t *testing.T) {
	root, modules := fixture(t)
	for index := range 40 {
		module := fmt.Sprintf("driver/d%d", index)
		modules = append(modules, module)
		write(t, root, module+"/go.mod", "module arkoren.dev/"+module+"\n")
	}
	policy(t, root, modules, []string{})
	commit(t, root, "chore: full inventory", nil)
	for _, module := range modules[:4] {
		git(t, root, "tag", "-d", module+"/v0.1.0")
	}
	for _, module := range modules {
		git(t, root, "tag", module+"/v0.1.0")
	}
	first, second := plan(t, root), plan(t, root)
	if len(first.Modules) != 44 || !reflect.DeepEqual(first, second) {
		t.Fatal("inventory/repeatability changed")
	}
	for _, item := range first.Modules {
		if item.NextVersion != nil {
			t.Fatalf("phantom proposal: %+v", item)
		}
	}
}

func TestPlanDevelopmentAndWebsiteNeverRelease(t *testing.T) {
	root, modules := fixture(t)
	policy(t, root, modules, []string{"cmd/arkoren-ci"})
	commit(t, root, "feat: maintenance", map[string]string{"cmd/arkoren-ci/go.mod": "module arkoren.dev/cmd/arkoren-ci\n", "cmd/arkoren-ci/main.go": "package main", "website/site.ts": "web"})
	result := plan(t, root)
	if len(result.Modules) != 4 {
		t.Fatal("development entered public plan")
	}
	for _, item := range result.Modules {
		if item.NextVersion != nil {
			t.Fatal("nonpayload change released")
		}
	}
}

func TestPlanRejectsUnapprovedCommittedModule(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "feat: unapproved", map[string]string{"driver/new/go.mod": "module arkoren.dev/driver/new\n"})
	rejected(t, root, "policy", "driver/new")
}

func TestPlanAccumulatesActualMultiModuleFiles(t *testing.T) {
	root, _ := fixture(t)
	fix := commit(t, root, "fix(api): repair", map[string]string{"core/a.go": "fix", "driver/example/a.go": "fix"})
	feature := commit(t, root, "feat(api): add", map[string]string{"core/b.go": "feature"})
	result := plan(t, root)
	item := proposed(t, result, "core", "v0.2.0")
	proposed(t, result, "driver/example", "v0.1.1")
	if len(item.Commits) != 2 || item.Commits[0].ID != fix || item.Commits[1].ID != feature {
		t.Fatal("wrong accumulated commits")
	}
}

func TestPlanIndependentBreakingDoesNotLeak(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "fix!: driver contract", map[string]string{"driver/example/a.go": "breaking"})
	fix := commit(t, root, "fix: core repair", map[string]string{"core/a.go": "fix"})
	result := plan(t, root)
	item := proposed(t, result, "core", "v0.1.1")
	proposed(t, result, "driver/example", "v0.2.0")
	if len(item.Commits) != 1 || item.Commits[0].ID != fix || strings.Contains(item.Notes, "driver contract") {
		t.Fatal("cross-module leak")
	}
}

func TestPlanBreakingFooterPreservesNotes(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "fix: change\n\nBREAKING CHANGE: old callers must migrate", map[string]string{"core/a.go": "breaking"})
	item := proposed(t, plan(t, root), "core", "v0.2.0")
	if !strings.Contains(item.Notes, "BREAKING") || !strings.Contains(item.Notes, "old callers must migrate") {
		t.Fatal("breaking note lost")
	}
}

func TestPlanDocsAndTestsSkipButEmbeddedGuidancePatches(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "feat: docs", map[string]string{"core/README.md": "docs", "core/a_test.go": "test", "core/testdata/a.json": "{}", "website/a.md": "web", ".agents/a.md": "rules"})
	for _, item := range plan(t, root).Modules {
		if item.NextVersion != nil {
			t.Fatal("docs released")
		}
	}
	commit(t, root, "docs(cli): guidance", map[string]string{"cmd/arkoren/skills/arkoren/guide.md": "embedded"})
	proposed(t, plan(t, root), "cmd/arkoren", "v0.1.1")
}

func TestPlanUnknownAndRevertConservativelyPatch(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "ordinary update", map[string]string{"core/a.go": "old"})
	commit(t, root, `Revert "ordinary update"`, map[string]string{"core/a.go": "reverted"})
	item := proposed(t, plan(t, root), "core", "v0.1.1")
	if len(item.Commits) != 2 {
		t.Fatal("revert canceled history")
	}
}

func TestPlanFeatureRevertRetainsMinor(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "feat: feature", map[string]string{"core/a.go": "feature"})
	commit(t, root, "revert: remove feature", map[string]string{"core/a.go": "removed"})
	proposed(t, plan(t, root), "core", "v0.2.0")
}

func TestPlanDetachedCandidateIgnoresUnreachableFutureTag(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "fix: first", map[string]string{"core/a.go": "first"})
	git(t, root, "tag", "core/v0.1.3")
	target := commit(t, root, "fix: next", map[string]string{"core/a.go": "next"})
	git(t, root, "checkout", "-b", "future")
	commit(t, root, "feat: future", map[string]string{"core/a.go": "future"})
	git(t, root, "tag", "core/v0.9.0")
	git(t, root, "checkout", "--detach", target)
	proposed(t, plan(t, root), "core", "v0.1.4")
}

func TestPlanOrdinaryMergeDoesNotDuplicate(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "checkout", "-b", "feature")
	id := commit(t, root, "feat: feature", map[string]string{"core/a.go": "feature"})
	git(t, root, "checkout", "main")
	commit(t, root, "ci: elsewhere", map[string]string{"workflow.txt": "ci"})
	git(t, root, "merge", "--no-ff", "feature", "-m", "Merge feature")
	item := proposed(t, plan(t, root), "core", "v0.2.0")
	if len(item.Commits) != 1 || item.Commits[0].ID != id {
		t.Fatal("merge duplicated payload")
	}
}

func TestPlanMissingExistingBaselineCannotBootstrap(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "tag", "-d", "core/v0.1.0")
	rejected(t, root, "baseline")
	rejected(t, root, "existing module", "core")
}

func TestPlanNoReleaseHistoryAndDuplicateInitialFail(t *testing.T) {
	root, modules := fixture(t)
	for _, module := range modules {
		git(t, root, "tag", "-d", module+"/v0.1.0")
	}
	rejected(t, root, "baseline")
	rejected(t, root, "distinct", "core", "core")
}

func TestPlanRejectsBadModuleRootAndDeclaration(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "chore: bad inventory", map[string]string{"driver/Bad/go.mod": "module private"})
	rejected(t, root, "unreviewed")
	git(t, root, "rm", "driver/Bad/go.mod")
	commit(t, root, "chore: remove invalid", nil)
	commit(t, root, "fix: duplicate path", map[string]string{"driver/example/go.mod": "module arkoren.dev/core\n"})
	rejected(t, root, "unexpected module path")
}

func TestPlanNewModuleRequiresExplicitInitial(t *testing.T) {
	root, modules := fixture(t)
	policy(t, root, append(modules, "ark/new"), []string{})
	commit(t, root, "feat: new module", map[string]string{"ark/new/go.mod": "module arkoren.dev/ark/new\n", "ark/new/new.go": "new"})
	rejected(t, root, "baseline")
	result, err := release.Prepare(context.Background(), root, git(t, root, "rev-parse", "HEAD"), []string{"ark/new"})
	if err != nil {
		t.Fatal(err)
	}
	item := proposed(t, result, "ark/new", "v0.1.0")
	if item.CurrentVersion != nil {
		t.Fatal("initial has baseline")
	}
}

func TestPlanMalformedTagFailsClosed(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "tag", "core/v0.01.1")
	rejected(t, root, "malformed")
}

func TestPlanPrereleaseFailsClosed(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "tag", "core/v0.1.0-beta")
	rejected(t, root, "malformed")
}

func TestPlanMajorTagFailsClosed(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "tag", "core/v1.0.0")
	rejected(t, root, "v1+")
}

func TestPlanSameCommitVersionsAreAmbiguous(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "tag", "core/v0.1.1")
	rejected(t, root, "ambiguous")
}

func TestPlanIncomparableReleaseHistoriesFail(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "checkout", "-b", "left")
	commit(t, root, "fix: left", map[string]string{"core/left.go": "left"})
	git(t, root, "tag", "core/v0.1.1")
	git(t, root, "checkout", "main")
	commit(t, root, "fix: right", map[string]string{"core/right.go": "right"})
	git(t, root, "tag", "core/v0.1.2")
	git(t, root, "merge", "--no-ff", "left", "-m", "Merge left")
	rejected(t, root, "ambiguous")
}

func TestPlanCliffIgnoreCannotDropPayload(t *testing.T) {
	root, _ := fixture(t)
	id := commit(t, root, "fix: repair", map[string]string{"core/a.go": "fix"})
	commit(t, root, "ci: ignore", map[string]string{".cliffignore": id + "\n"})
	rejected(t, root, "omitted or added")
}

func TestPlanMergeResolutionRequiresExplicitCommit(t *testing.T) {
	root, _ := fixture(t)
	commit(t, root, "fix: base", map[string]string{"core/a.go": "base"})
	git(t, root, "checkout", "-b", "left")
	commit(t, root, "fix: left", map[string]string{"core/a.go": "left"})
	git(t, root, "checkout", "main")
	commit(t, root, "fix: right", map[string]string{"core/a.go": "right"})
	command := exec.Command("git", "merge", "left")
	command.Dir = root
	if err := command.Run(); err == nil {
		t.Fatal("expected conflict")
	}
	commit(t, root, "Merge resolution", map[string]string{"core/a.go": "resolution"})
	rejected(t, root, "merge-only")
}

func TestPlanIgnoredLocalManifestAndShallowBoundary(t *testing.T) {
	root, _ := fixture(t)
	write(t, root, "driver/private/go.mod", "module private\n")
	if len(plan(t, root).Modules) != 4 {
		t.Fatal("local inventory leaked")
	}
	write(t, root, ".git/shallow", git(t, root, "rev-parse", "HEAD")+"\n")
	rejected(t, root, "shallow")
}

func TestPlanRejectsRefsAndInvalidObjectIDs(t *testing.T) {
	root, _ := fixture(t)
	_, err := release.Prepare(context.Background(), root, "HEAD", nil)
	if err == nil {
		t.Fatal("ref accepted")
	}
	_, err = release.Prepare(context.Background(), root, strings.Repeat("a", 40), nil)
	if err == nil {
		t.Fatal("nonexistent object accepted")
	}
}

func TestPlanHistoricalAdapterDoesNotWeakenCurrentApproval(t *testing.T) {
	root, _ := fixture(t)
	git(t, root, "rm", ".github/release-modules.json")
	sha := commit(t, root, "chore: historical no-policy shape", nil)
	rejected(t, root, "policy")
	result, err := release.PrepareHistorical(context.Background(), root, sha, nil)
	if err != nil || len(result.Modules) != 4 {
		t.Fatalf("historical decode: %+v %v", result, err)
	}
	commit(t, root, "chore: historical array policy", map[string]string{".github/release-modules.json": `["core","cmd/arkoren","driver/example","foundation/example"]`})
	_, err = release.PrepareHistorical(context.Background(), root, git(t, root, "rev-parse", "HEAD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rejected(t, root, "decode module policy")
}

func TestPlanSignedMergeAndAnnotatedBaseline(t *testing.T) {
	root, _ := fixture(t)
	key := filepath.Join(t.TempDir(), "signer")
	command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture signer: %s %v", data, err)
	}
	git(t, root, "config", "gpg.format", "ssh")
	git(t, root, "config", "user.signingkey", key)
	git(t, root, "tag", "-d", "core/v0.1.0")
	git(t, root, "tag", "-s", "core/v0.1.0", "-m", "signed baseline")
	git(t, root, "checkout", "-b", "feature")
	id := commit(t, root, "fix: café 😀 <tag> & 'quote'", map[string]string{"core/a.go": "fix"})
	git(t, root, "checkout", "main")
	commit(t, root, "ci: elsewhere", map[string]string{"workflow.txt": "ci"})
	git(t, root, "merge", "--gpg-sign", "--no-ff", "feature", "-m", "Merge signed feature")
	item := proposed(t, plan(t, root), "core", "v0.1.1")
	if len(item.Commits) != 1 || item.Commits[0].ID != id || !strings.Contains(item.Notes, "café 😀 <tag> & 'quote'") {
		t.Fatalf("signed merge or Unicode notes changed: %+v", item)
	}
}
