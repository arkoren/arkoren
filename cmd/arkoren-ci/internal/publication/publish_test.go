package publication

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
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
		title := "Core v0.1.1"
		if name == "driver/example/v0.1.1" {
			title = "Driver: Example v0.1.1"
		}
		if !strings.HasPrefix(strings.TrimSpace(input), "## "+title+"\n") || strings.Contains(input, "DRY RUN") {
			t.Fatalf("published notes contain preview wording: %q", input)
		}
		expected := []string{"release", "create", name, "--repo", github.Repository, "--verify-tag", "--prerelease", "--latest=false", "--title", title, "--notes-file", "-"}
		if !reflect.DeepEqual(args, expected) {
			t.Fatalf("wrong native release flags: %v", args)
		}
		if writes == 1 && fail {
			fail = false
			return nil, fmt.Errorf("lost connection")
		}
		writes++
		records[name] = Release{Tag: name, Name: title, Body: input, Prerelease: true, Assets: []any{}}
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

// TestExistingReleaseConflictNeverOverwrites ensures a pre-existing conflicting
// release fails at once: one listing, no wait and no write.
func TestExistingReleaseConflictNeverOverwrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		batch, entry := newReleaseBatch()
		fake := &releaseFake{t: t, entry: entry, body: "foreign", visible: true}
		start := time.Now()
		if err := PublishReleases(context.Background(), fake.client(), batch); err == nil {
			t.Fatal("conflicting release accepted")
		}
		if elapsed := time.Since(start); elapsed != 0 || fake.listings != 1 || fake.creates != 0 {
			t.Fatalf("elapsed %v, listings %d, creates %d", elapsed, fake.listings, fake.creates)
		}
	})
}

// releaseFake answers release listings and records `gh release create` calls
// for one entry. Before creation the listing is empty; after creation the
// release is listed with body, except as hide, duplicate or visible say.
type releaseFake struct {
	t     *testing.T
	entry release.Entry
	body  string
	// visible lists the release from the start, as if it pre-existed.
	visible bool
	// hide is the number of post-create listings that omit the release.
	hide int
	// duplicate makes the first post-create listing contain the tag twice.
	duplicate bool
	// onList runs inside every post-create listing.
	onList   func()
	creates  int
	listings int
	postList int
}

// client returns a gh client backed by the fake; any other command fails the test.
func (f *releaseFake) client() github.Client {
	return github.Client{Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[0] == "api" && strings.Contains(args[3], "/releases?") {
			f.listings++
			item := Release{Tag: TagName(f.entry), Name: ReleaseName(f.entry, 0), Body: f.body, Prerelease: true, Assets: []any{}}
			items := []Release{}
			if f.visible {
				items = append(items, item)
			} else if f.creates > 0 {
				f.postList++
				if f.onList != nil {
					f.onList()
				}
				switch {
				case f.postList <= f.hide:
				case f.duplicate && f.postList == 1:
					items = append(items, item, item)
				default:
					items = append(items, item)
				}
			}
			return json.Marshal(items)
		}
		if len(args) < 3 || args[0] != "release" || args[1] != "create" {
			f.t.Fatalf("unexpected command %v", args)
		}
		f.creates++
		return nil, nil
	}}
}

// newReleaseBatch returns a one-module batch and its entry.
func newReleaseBatch() (Batch, release.Entry) {
	version := "v0.1.1"
	entry := release.Entry{Directory: "core", NextVersion: &version, Notes: "approved"}
	return Batch{Plan: release.Plan{Modules: []release.Entry{entry}}}, entry
}

// TestCreatedReleaseConfirmationWaitsForListing covers a lagging listing that
// shows the new release only on the third post-create read.
func TestCreatedReleaseConfirmationWaitsForListing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		batch, entry := newReleaseBatch()
		fake := &releaseFake{t: t, entry: entry, body: entry.Notes, hide: 2}
		start := time.Now()
		if err := PublishReleases(context.Background(), fake.client(), batch); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 13*time.Second || fake.creates != 1 || fake.listings != 4 {
			t.Fatalf("elapsed %v, creates %d, listings %d", elapsed, fake.creates, fake.listings)
		}
		if err := PublishReleases(context.Background(), fake.client(), batch); err != nil {
			t.Fatal(err)
		}
		if fake.creates != 1 || fake.listings != 5 {
			t.Fatalf("rerun recreated or re-listed: creates %d, listings %d", fake.creates, fake.listings)
		}
	})
}

// TestCreatedReleaseNeverVisibleFailsWithoutRecreating exhausts the schedule.
func TestCreatedReleaseNeverVisibleFailsWithoutRecreating(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		batch, entry := newReleaseBatch()
		fake := &releaseFake{t: t, entry: entry, body: entry.Notes, hide: 1 << 30}
		start := time.Now()
		err := PublishReleases(context.Background(), fake.client(), batch)
		if err == nil || !strings.Contains(err.Error(), "not yet visible") {
			t.Fatalf("unexpected error: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 223*time.Second || fake.creates != 1 || fake.listings != 7 {
			t.Fatalf("elapsed %v, creates %d, listings %d", elapsed, fake.creates, fake.listings)
		}
	})
}

// TestCreatedReleaseConflictFailsAtBound ensures a persistently conflicting
// created release is never overwritten and fails when the schedule ends.
func TestCreatedReleaseConflictFailsAtBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		batch, entry := newReleaseBatch()
		fake := &releaseFake{t: t, entry: entry, body: "foreign"}
		start := time.Now()
		err := PublishReleases(context.Background(), fake.client(), batch)
		if err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("unexpected error: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 223*time.Second || fake.creates != 1 {
			t.Fatalf("elapsed %v, creates %d", elapsed, fake.creates)
		}
	})
}

// TestConfirmationRetriesDuplicatePage treats a duplicated tag in one listing as
// transient.
func TestConfirmationRetriesDuplicatePage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		batch, entry := newReleaseBatch()
		fake := &releaseFake{t: t, entry: entry, body: entry.Notes, duplicate: true}
		start := time.Now()
		if err := PublishReleases(context.Background(), fake.client(), batch); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 3*time.Second {
			t.Fatalf("elapsed %v", elapsed)
		}
	})
}

// TestReleaseConfirmationHonorsCancellation stops waiting once the context ends.
func TestReleaseConfirmationHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		batch, entry := newReleaseBatch()
		ctx, cancel := context.WithCancel(context.Background())
		fake := &releaseFake{t: t, entry: entry, body: entry.Notes, hide: 1 << 30, onList: cancel}
		start := time.Now()
		err := PublishReleases(ctx, fake.client(), batch)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 0 || fake.creates != 1 {
			t.Fatalf("elapsed %v, creates %d", elapsed, fake.creates)
		}
	})
}

// TestExistingNeutralReleaseRetainsOriginalTitle ensures recovery does not
// rename releases signed before readable display names were introduced.
func TestExistingNeutralReleaseRetainsOriginalTitle(t *testing.T) {
	version := "v0.1.1"
	entry := release.Entry{Directory: "core", NextVersion: &version, Notes: "\n## core/v0.1.1\n"}
	batch := Batch{Plan: release.Plan{SchemaVersion: 2, Modules: []release.Entry{entry}}}
	client := github.Client{Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) < 4 || args[0] != "api" {
			t.Fatalf("existing release was mutated: %v", args)
		}
		return json.Marshal([]Release{{Tag: "core/v0.1.1", Name: "core/v0.1.1", Body: entry.Notes, Prerelease: true}})
	}}
	if err := PublishReleases(t.Context(), client, batch); err != nil {
		t.Fatal(err)
	}
}

// TestReadableReleaseRejectsLegacyTitle prevents adopting an incorrectly named
// release when its original schema requires a human-readable title.
func TestReadableReleaseRejectsLegacyTitle(t *testing.T) {
	version := "v0.1.1"
	entry := release.Entry{Directory: "core", NextVersion: &version, Notes: "\n## Core v0.1.1\n"}
	item := Release{Tag: "core/v0.1.1", Name: "core/v0.1.1", Body: entry.Notes, Prerelease: true}
	if err := item.Check(entry, 3); err == nil {
		t.Fatal("raw tag title adopted for a readable batch")
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
