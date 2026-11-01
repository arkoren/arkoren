package skill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// TestSkillSourceIncludesHiddenFiles checks the complete-file semantics needed
// by all: embedding, including dot and underscore directory descendants.
func TestSkillSourceIncludesHiddenFiles(t *testing.T) {
	t.Parallel()

	source := fstest.MapFS{
		"SKILL.md":         {Data: []byte("skill")},
		".hidden/page.md":  {Data: []byte("dot directory")},
		"_private/page.md": {Data: []byte("underscore directory")},
		".metadata":        {Data: []byte("dot file")},
		"_index.md":        {Data: []byte("underscore file")},
	}

	bundle, err := LoadSource(t.Context(), source)

	if err != nil {
		t.Fatal(err)
	}

	want := []string{".hidden/page.md", ".metadata", "SKILL.md", "_index.md", "_private/page.md"}
	paths := make([]string, 0, len(bundle.Files))

	for _, file := range bundle.Files {
		paths = append(paths, file.Path)
	}

	if !slices.Equal(paths, want) {
		t.Fatalf("paths=%v, want %v", paths, want)
	}
}

// TestSkillSourceDeterministicManifest checks stable ordering and the existing
// path-NUL-hash-newline revision format without using skillRevision as oracle.
func TestSkillSourceDeterministicManifest(t *testing.T) {
	t.Parallel()

	source := fstest.MapFS{
		"z/page.md": {Data: []byte("guide")},
		"SKILL.md":  {Data: []byte("skill")},
	}

	first, err := LoadSource(t.Context(), source)

	if err != nil {
		t.Fatal(err)
	}

	second, err := LoadSource(t.Context(), source)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated loads produced different bundles")
	}

	if first.Files[0].Path != "SKILL.md" || first.Files[1].Path != "z/page.md" {
		t.Fatalf("unsorted files: %+v", first.Files)
	}

	record := fmt.Sprintf("SKILL.md\x00%x\nz/page.md\x00%x\n",
		sha256.Sum256([]byte("skill")), sha256.Sum256([]byte("guide")))
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(record)))

	if first.Manifest.Schema != 1 || first.Manifest.Name != "arkoren" || first.Manifest.Revision != want {
		t.Fatalf("manifest=%+v, want revision %s", first.Manifest, want)
	}
}

// TestSkillSourceRequiresSkill rejects a tree that cannot be discovered as an
// agent skill, even when it contains otherwise valid reference files.
func TestSkillSourceRequiresSkill(t *testing.T) {
	t.Parallel()

	source := fstest.MapFS{"references/page.md": {Data: []byte("guide")}}

	_, err := LoadSource(t.Context(), source)

	if err == nil || !strings.Contains(err.Error(), "lacks SKILL.md") {
		t.Fatalf("missing SKILL.md error=%v", err)
	}
}

// TestSkillSourceRejectsManifest reserves the ownership record for installation
// state, rather than allowing authored content to overwrite its hashes.
func TestSkillSourceRejectsManifest(t *testing.T) {
	t.Parallel()

	source := fstest.MapFS{
		"SKILL.md":            {Data: []byte("skill")},
		".arkoren-skill.json": {Data: []byte("{}")},
	}

	_, err := LoadSource(t.Context(), source)

	if err == nil || !strings.Contains(err.Error(), "invalid bundled skill path") {
		t.Fatalf("reserved manifest error=%v", err)
	}
}

// TestSkillSourceRejectsUnsafePaths prevents platform-specific aliases from
// bypassing the same path validation used by installation manifests.
func TestSkillSourceRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	source := fstest.MapFS{
		"SKILL.md": {Data: []byte("skill")},
		"C:page":   {Data: []byte("unsafe")},
	}

	_, err := LoadSource(t.Context(), source)

	if err == nil || !strings.Contains(err.Error(), "invalid bundled skill path") {
		t.Fatalf("unsafe path error=%v", err)
	}
}

// TestSkillSourceRejectsSymlinks keeps unsupported entries from becoming
// ordinary installed files. The fake filesystem needs no host symlink support.
func TestSkillSourceRejectsSymlinks(t *testing.T) {
	t.Parallel()

	source := fstest.MapFS{
		"SKILL.md": {Data: []byte("skill")},
		"linked":   {Data: []byte("SKILL.md"), Mode: fs.ModeSymlink},
	}

	_, err := LoadSource(t.Context(), source)

	if err == nil || !strings.Contains(err.Error(), "invalid bundled skill path") {
		t.Fatalf("symlink error=%v", err)
	}
}

// TestSkillSourceCanceledDuringWalk ensures cancellation discards partial
// results rather than returning a truncated but apparently valid skill.
func TestSkillSourceCanceledDuringWalk(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	source := cancelSkillSource{
		FS: fstest.MapFS{
			"SKILL.md": {Data: []byte("skill")},
			"page.md":  {Data: []byte("guide")},
		},
		cancel: cancel,
	}

	bundle, err := LoadSource(ctx, source)

	if !errors.Is(err, context.Canceled) || len(bundle.Files) != 0 {
		t.Fatalf("bundle=%+v error=%v", bundle, err)
	}
}

// cancelSkillSource cancels after its first read so the next walk callback
// tests in-flight cancellation deterministically, without sleeps or goroutines.
type cancelSkillSource struct {
	// FS supplies normal directory enumeration and file content.
	fs.FS
	// cancel expires the loader's context after a file has been read.
	cancel context.CancelFunc
}

// ReadFile returns the underlying bytes and then cancels the active load.
// Read failures remain unchanged so the wrapper does not mask source errors.
func (source cancelSkillSource) ReadFile(path string) ([]byte, error) {
	content, err := fs.ReadFile(source.FS, path)
	source.cancel()

	return content, err
}

// TestSkillSourceCanceledDuringFinalRead checks cancellation when there is no
// next walk callback to observe it. Even a complete one-file skill is discarded.
func TestSkillSourceCanceledDuringFinalRead(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	source := cancelSkillSource{
		FS: fstest.MapFS{
			"SKILL.md": {Data: []byte("skill")},
		},
		cancel: cancel,
	}

	bundle, err := LoadSource(ctx, source)

	if !errors.Is(err, context.Canceled) || len(bundle.Files) != 0 {
		t.Fatalf("bundle=%+v error=%v", bundle, err)
	}
}

// TestSkillManifestRejectsUnsafePaths checks manifest content before filesystem use.
func TestSkillManifestRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"../outside", "/absolute", `a\b`, "a/../b", ".", SkillManifestName, "C:/escape"} {
		manifest := Manifest{Schema: 1, Name: "arkoren", Files: map[string]string{"SKILL.md": Hash(nil), path: Hash(nil)}}
		manifest.Revision = Revision(manifest.Files)

		requireNewUsage(t, ValidateSkillManifest(manifest))
	}
}

// TestSkillManifestRejectsCorruption detects changed hashes without a new revision.
func TestSkillManifestRejectsCorruption(t *testing.T) {
	t.Parallel()

	bundle := testSkillBundle(t, map[string]string{"SKILL.md": "old"})
	bundle.Manifest.Files["SKILL.md"] = Hash([]byte("corrupt"))

	requireNewUsage(t, ValidateSkillManifest(bundle.Manifest))
}

// TestSkillManifestEncodingIsDeterministic keeps identical installs byte-identical.
func TestSkillManifestEncodingIsDeterministic(t *testing.T) {
	t.Parallel()

	bundle := testSkillBundle(t, map[string]string{"SKILL.md": "skill", "a.md": "page"})

	inventory, err := Files(bundle)

	if err != nil {
		t.Fatal(err)
	}

	var manifest Manifest

	if err := json.Unmarshal(inventory[len(inventory)-1].Content, &manifest); err != nil {
		t.Fatal(err)
	}

	if err := ValidateSkillManifest(manifest); err != nil {
		t.Fatal(err)
	}

	other, err := Files(bundle)

	if err != nil || !bytes.Equal(inventory[len(inventory)-1].Content, other[len(other)-1].Content) {
		t.Fatalf("nondeterministic manifest: %v", err)
	}
}
