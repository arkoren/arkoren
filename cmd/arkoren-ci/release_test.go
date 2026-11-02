package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseToolReportsOneJSONDocument(t *testing.T) {
	stdout := &bytes.Buffer{}
	if err := run(context.Background(), []string{"release", "tool"}, stdout); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != `{"git_cliff":"2.14.2"}`+"\n" {
		t.Fatal(stdout.String())
	}
}

func TestReleaseEvidenceRejectsUntrustedEventBeforeChildCredentialUse(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "called")
	if err := os.WriteFile(filepath.Join(directory, "gh"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := filepath.Join(directory, "event.json")
	if err := os.WriteFile(event, []byte(`{"repository":{"full_name":"fork/repo"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GITHUB_EVENT_PATH", event)
	t.Setenv("GITHUB_EVENT_NAME", "workflow_run")
	t.Setenv("GITHUB_REPOSITORY", "arkoren/arkoren")
	t.Setenv("GH_TOKEN", "secret-must-not-be-used")
	stdout := &bytes.Buffer{}
	err := run(context.Background(), []string{"release", "evidence", "--output", filepath.Join(directory, "result.json")}, stdout)
	if err == nil || !strings.Contains(err.Error(), "repository mismatch") || stdout.Len() != 0 {
		t.Fatalf("unexpected outcome: %s %v", stdout, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("untrusted event invoked gh")
	}
}
