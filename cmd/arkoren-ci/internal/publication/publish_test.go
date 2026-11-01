package publication

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
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
	"arkoren.dev/cmd/arkoren-ci/internal/release"
)

func TestLostSuccessfulPushResponseReconcilesExactObjects(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = push ]; then\n'%s' \"$@\" >/dev/null 2>&1 || exit $?\nexit 1\nfi\nexec '%s' \"$@\"\n", real, real)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fixture.push(t, batch)
	complete, err := Reconcile(context.Background(), fixture.root, fixture.trust, batch)
	if err != nil || !complete {
		t.Fatalf("lost response not recovered: %v", err)
	}
}

func TestEmptyBatchCannotInvokeDefaultBranchPush(t *testing.T) {
	if err := PushTags(context.Background(), "unused", "unused", Batch{}, "unused", "unused"); err == nil {
		t.Fatal("empty batch permitted a default-refspec Git push")
	}
}

func TestPartialPrereleaseFailureResumesOriginalVersionsIdempotently(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	fixture.push(t, batch)
	records := map[string]Release{}
	writes := 0
	fail := true
	client := github.Client{Root: fixture.root, Token: "fixture-app", Command: func(_ context.Context, input string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[0] == "api" && strings.Contains(args[3], "/releases?") {
			items := []Release{}
			for _, item := range records {
				items = append(items, item)
			}
			return json.Marshal(items)
		}
		if len(args) < 3 || args[0] != "release" || args[1] != "create" {
			t.Fatalf("unexpected write command %v", args)
		}
		name := args[2]
		expected := []string{"release", "create", name, "--repo", github.Repository, "--verify-tag", "--prerelease", "--latest=false", "--title", name, "--notes-file", "-"}
		if !reflect.DeepEqual(args, expected) {
			t.Fatalf("wrong native release flags: %v", args)
		}
		if writes == 1 && fail {
			fail = false
			return nil, fmt.Errorf("lost connection")
		}
		writes++
		records[name] = Release{Tag: name, Name: name, Body: input, Prerelease: true, Assets: []any{}}
		return nil, nil
	}}
	if err := PublishReleases(context.Background(), client, batch); err == nil {
		t.Fatal("partial failure ignored")
	}
	history, err := Recorded(context.Background(), fixture.root, fixture.trust)
	if err != nil || len(history) != 1 {
		t.Fatalf("original batch lost: %v", err)
	}
	if err := Recompute(context.Background(), fixture.root, history[0]); err != nil {
		t.Fatal(err)
	}
	if err := PublishReleases(context.Background(), client, history[0]); err != nil {
		t.Fatal(err)
	}
	if err := PublishReleases(context.Background(), client, history[0]); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("duplicate or missing release writes: %d", writes)
	}
}

func TestExistingReleaseConflictNeverOverwrites(t *testing.T) {
	version := "v0.1.1"
	entry := release.Entry{Directory: "core", NextVersion: &version, Notes: "approved"}
	batch := Batch{Plan: release.Plan{Modules: []release.Entry{entry}}}
	client := github.Client{Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) < 4 || args[0] != "api" {
			t.Fatalf("conflicting release was mutated: %v", args)
		}
		return []byte(`[{"tag_name":"core/v0.1.1","name":"core/v0.1.1","body":"foreign","prerelease":true,"draft":false,"assets":[]}]`), nil
	}}
	if err := PublishReleases(context.Background(), client, batch); err == nil {
		t.Fatal("conflicting release accepted")
	}
}

func TestCurrentPublisherEvidenceRefusesBeforeAnyPublicationEffects(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	client := github.Client{Root: fixture.root, Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) != 4 || !strings.HasSuffix(args[3], "/git/ref/heads/main") {
			t.Fatalf("work continued after evidence refusal: %v", args)
		}
		return json.Marshal(map[string]any{"object": map[string]string{"sha": batch.Plan.SHA}})
	}}
	if _, err := Prepare(context.Background(), client, fixture.trust, batch.Plan.SHA); err == nil {
		t.Fatal("failed current-code evidence accepted")
	}
	if actual := command(t, fixture.root, "git", "tag", "--list", "core/v0.1.1"); actual != "" {
		t.Fatal("evidence refusal wrote tag")
	}
}

func TestPublicConsumerUsesExactOriginAndCredentialFreeNormalProxy(t *testing.T) {
	fixture := newFixture(t)
	writeTest(t, fixture.root, "cmd/arkoren/payload.go", "package main\nfunc main() {}\n")
	sha := fixture.commit(t, "fix: CLI payload")
	plan, err := release.Prepare(context.Background(), fixture.root, sha, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := NewBatch(plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture.push(t, batch)
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	metadata, err := json.Marshal(map[string]any{"Dir": filepath.Join(fixture.root, "cmd/arkoren"), "GoMod": filepath.Join(fixture.root, "cmd/arkoren/go.mod"), "Sum": "h1:fixture", "GoModSum": "h1:fixture", "Origin": map[string]string{"Hash": sha, "Ref": "refs/tags/cmd/arkoren/v0.1.1", "VCS": "git", "URL": "https://github.com/arkoren/arkoren", "Subdir": "cmd/arkoren"}})
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
test -z "${APP_TOKEN-}${SIGNING_KEY-}${GH_TOKEN-}${GITHUB_TOKEN-}" || exit 20
test "$GOWORK" = off && test "$GOPROXY" = https://proxy.golang.org && test "$GOSUMDB" = sum.golang.org || exit 21
test -z "$GOPRIVATE$GONOPROXY$GONOSUMDB" || exit 22
printf '%%s\n' "$*" >> '%s'
case "$1 $2" in
'mod download') printf '%%s' '%s';;
'mod edit') printf '%%s' '{"Module":{"Path":"arkoren.dev/cmd/arkoren"},"Replace":null}';;
'build ./...'|'install arkoren.dev/cmd/arkoren@v0.1.1') ;;
*) exit 23;;
esac
`, log, metadata)
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("APP_TOKEN", "never-in-public-build")
	t.Setenv("SIGNING_KEY", "never-in-public-build")
	t.Setenv("GH_TOKEN", "never-in-public-build")
	if err := PublicVerify(context.Background(), fixture.root, batch); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "install arkoren.dev/cmd/arkoren@v0.1.1") || strings.Contains(string(data), "@main") {
		t.Fatalf("wrong public install: %s", data)
	}
}

func TestPublicBuildFailureLeavesSignedBatchRecoverable(t *testing.T) {
	fixture := newFixture(t)
	batch := fixture.batch(t)
	fixture.push(t, batch)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	if err := PublicVerify(ctx, fixture.root, batch); err == nil {
		t.Fatal("public failure accepted")
	}
	history, err := Recorded(context.Background(), fixture.root, fixture.trust)
	if err != nil || len(history) != 1 || history[0].Digest != batch.Digest {
		t.Fatalf("public failure lost original signed versions: %v", err)
	}
}
