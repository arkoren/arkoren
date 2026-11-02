package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/release"
)

func TestSummaryPreservesEscapedUnicodeNotes(t *testing.T) {
	version := "v0.1.1"
	result := release.Plan{SHA: "abc", Modules: []release.Entry{{Directory: "core", ModulePath: "arkoren.dev/core", NextVersion: &version, Notes: "é 😀 <tag> ' & \""}}}
	want := "<pre>é 😀 &lt;tag&gt; &#x27; &amp; &quot;</pre>"
	if !strings.Contains(release.Summary(result), want) {
		t.Fatal(release.Summary(result))
	}
}

func TestWriteRejectsInsideCheckoutAndSymlinkParents(t *testing.T) {
	root := t.TempDir()
	if err := release.Write(root, filepath.Join(root, "new"), release.Plan{}); err == nil {
		t.Fatal("inside write allowed")
	}
	outside := t.TempDir()
	if err := os.Symlink(root, filepath.Join(outside, "link")); err != nil {
		t.Fatal(err)
	}
	if err := release.Write(root, filepath.Join(outside, "link", "new"), release.Plan{}); err == nil {
		t.Fatal("symlink write allowed")
	}
}
