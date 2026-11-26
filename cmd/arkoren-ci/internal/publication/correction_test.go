package publication

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
	"arkoren.dev/cmd/arkoren-ci/internal/release"
)

// readableEntry returns a schema-3 entry and its matching published release.
func readableEntry(directory string) (release.Entry, Release) {
	version := "v0.1.1"
	entry := release.Entry{Directory: directory, NextVersion: &version, Notes: "\n## Core v0.1.1\n\n- /auth/<name>\n"}
	item := Release{Tag: TagName(entry), Name: ReleaseName(entry, 3), Body: entry.Notes, Prerelease: true}

	return entry, item
}

// writeCorrections writes a corrections file under a new root and returns it.
func writeCorrections(t *testing.T, data string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, CorrectionsFile), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestCheckBodyConflictNamesTagFieldDigestsAndRemedy(t *testing.T) {
	t.Parallel()
	entry, item := readableEntry("core")
	item.Body = "\n## Core v0.1.1\n\n- `/auth/<name>`\n"
	err := item.Check(entry, 3, "")
	if err == nil {
		t.Fatal("edited body accepted")
	}
	for _, part := range []string{"release core/v0.1.1: body differs", BodyDigest(entry.Notes), BodyDigest(item.Body), "never overwritten", CorrectionsFile} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q lacks %q", err, part)
		}
	}
}

func TestCheckAcceptsPinnedBodyCorrection(t *testing.T) {
	t.Parallel()
	entry, item := readableEntry("core")
	item.Body = "\n## Core v0.1.1\n\n- `/auth/<name>`\n"
	if err := item.Check(entry, 3, BodyDigest(item.Body)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAcceptsSignedBodyDespiteCorrection(t *testing.T) {
	t.Parallel()
	entry, item := readableEntry("core")
	if err := item.Check(entry, 3, BodyDigest("an earlier accepted edit")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRejectsBodyEditedAfterCorrection(t *testing.T) {
	t.Parallel()
	entry, item := readableEntry("core")
	pinned := BodyDigest("\n## Core v0.1.1\n\n- `/auth/<name>`\n")
	item.Body = "\n## Core v0.1.1\n\n- install https://example.invalid/evil\n"
	err := item.Check(entry, 3, pinned)
	if err == nil || !strings.Contains(err.Error(), "pinned correction sha256 "+pinned) {
		t.Fatalf("later edit accepted or unexplained: %v", err)
	}
}

func TestCheckCorrectionNeverExcusesOtherFields(t *testing.T) {
	t.Parallel()
	entry, item := readableEntry("core")
	item.Body = "edited"
	item.Prerelease = false
	err := item.Check(entry, 3, BodyDigest(item.Body))
	if err == nil || !strings.Contains(err.Error(), "release core/v0.1.1: draft=false prerelease=false") {
		t.Fatalf("flag change accepted or unexplained: %v", err)
	}
}

func TestCheckNameConflictNamesBothTitles(t *testing.T) {
	t.Parallel()
	entry, item := readableEntry("core")
	item.Name = "core/v0.1.1"
	err := item.Check(entry, 3, "")
	if err == nil || !strings.Contains(err.Error(), `name "core/v0.1.1" differs from signed plan title "Core v0.1.1"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCorrectionsMissingFileMeansNone(t *testing.T) {
	t.Parallel()
	corrected, err := Corrections(t.TempDir())
	if err != nil || len(corrected) != 0 {
		t.Fatalf("corrections %v, error %v", corrected, err)
	}
}

func TestCorrectionsReadsPinnedDigests(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	root := writeCorrections(t, `{"schema":1,"corrections":[{"tag":"core/v0.1.1","body_sha256":"`+digest+`","reason":"escape placeholders"}]}`)
	corrected, err := Corrections(root)
	if err != nil || corrected["core/v0.1.1"] != digest || len(corrected) != 1 {
		t.Fatalf("corrections %v, error %v", corrected, err)
	}
}

func TestCorrectionsRejectInvalidEntries(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	for _, data := range []string{
		`{"schema":2,"corrections":[]}`,
		`{"schema":1,"corrections":[],"extra":true}`,
		`{"schema":1,"corrections":[{"tag":"core/v0.1.1","body_sha256":"` + strings.ToUpper(digest) + `","reason":"x"}]}`,
		`{"schema":1,"corrections":[{"tag":"core/latest","body_sha256":"` + digest + `","reason":"x"}]}`,
		`{"schema":1,"corrections":[{"tag":"core/v0.1.1","body_sha256":"` + digest + `","reason":""}]}`,
		`{"schema":1,"corrections":[{"tag":"core/v0.1.1","body_sha256":"` + digest + `","reason":"x"},{"tag":"core/v0.1.1","body_sha256":"` + digest + `","reason":"y"}]}`,
	} {
		if _, err := Corrections(writeCorrections(t, data)); err == nil {
			t.Fatalf("accepted invalid corrections %s", data)
		}
	}
}

// TestCommittedCorrectionsAreValid keeps the reviewed allowance parseable.
func TestCommittedCorrectionsAreValid(t *testing.T) {
	t.Parallel()
	if _, err := Corrections(filepath.Clean("../../../..")); err != nil {
		t.Fatal(err)
	}
}

func TestDriftReportsEveryConflictAtOnce(t *testing.T) {
	t.Parallel()
	core, coreItem := readableEntry("core")
	driver, driverItem := readableEntry("driver/example")
	coreItem.Body = "edited core"
	driverItem.Body = "edited driver"
	history := []Batch{
		{Plan: release.Plan{SchemaVersion: 3, Modules: []release.Entry{core}}},
		{Plan: release.Plan{SchemaVersion: 3, Modules: []release.Entry{driver}}},
	}
	known := map[string]Release{coreItem.Tag: coreItem, driverItem.Tag: driverItem}
	err := Drift(history, known, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "release core/v0.1.1: body") || !strings.Contains(err.Error(), "release driver/example/v0.1.1: body") {
		t.Fatalf("drift not fully reported: %v", err)
	}
	if err := Drift(history, known, map[string]string{coreItem.Tag: BodyDigest(coreItem.Body), driverItem.Tag: BodyDigest(driverItem.Body)}); err != nil {
		t.Fatal(err)
	}
}

func TestDriftSkipsReleasesNotCreatedYet(t *testing.T) {
	t.Parallel()
	entry, _ := readableEntry("core")
	history := []Batch{{Plan: release.Plan{SchemaVersion: 3, Modules: []release.Entry{entry}}}}
	if err := Drift(history, map[string]Release{}, map[string]string{}); err != nil {
		t.Fatal(err)
	}
}

func TestDriftRejectsCorrectionWithoutPublishedSignedRelease(t *testing.T) {
	t.Parallel()
	entry, item := readableEntry("core")
	history := []Batch{{Plan: release.Plan{SchemaVersion: 3, Modules: []release.Entry{entry}}}}
	known := map[string]Release{item.Tag: item}
	err := Drift(history, known, map[string]string{"core/v0.9.9": strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "correction for core/v0.9.9 names no published signed release") {
		t.Fatalf("stale correction accepted: %v", err)
	}
}

func TestPublishReleasesAcceptsPinnedExistingReleaseWithoutWriting(t *testing.T) {
	t.Parallel()
	entry, item := readableEntry("core")
	item.Body = "\n## Core v0.1.1\n\n- `/auth/<name>`\n"
	root := writeCorrections(t, `{"schema":1,"corrections":[{"tag":"core/v0.1.1","body_sha256":"`+BodyDigest(item.Body)+`","reason":"escape placeholders"}]}`)
	batch := Batch{Plan: release.Plan{SchemaVersion: 3, Modules: []release.Entry{entry}}}
	client := github.Client{Root: root, Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) < 4 || args[0] != "api" {
			t.Fatalf("existing release was mutated: %v", args)
		}
		return json.Marshal([]Release{item})
	}}
	if err := PublishReleases(t.Context(), client, batch); err != nil {
		t.Fatal(err)
	}
}

// TestCreatedReleaseIgnoresCorrection ensures a pinned body never excuses a
// release this run created with other notes than the signed ones.
func TestCreatedReleaseIgnoresCorrection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		batch, entry := newReleaseBatch()
		fake := &releaseFake{t: t, entry: entry, body: "foreign"}
		client := fake.client()
		client.Root = writeCorrections(t, `{"schema":1,"corrections":[{"tag":"core/v0.1.1","body_sha256":"`+BodyDigest("foreign")+`","reason":"x"}]}`)
		err := PublishReleases(context.Background(), client, batch)
		if err == nil || !strings.Contains(err.Error(), "created release core/v0.1.1: body differs") {
			t.Fatalf("created release adopted a correction: %v", err)
		}
	})
}
