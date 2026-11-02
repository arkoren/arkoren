package process_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

func TestRunKeepsExplicitCWDAndDropsAmbientSecrets(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(t.TempDir(), "inspect")
	if err := os.WriteFile(script, []byte("#!/bin/sh\npwd\nenv\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_TOKEN", "secret-app")
	t.Setenv("SIGNING_KEY", "secret-signing")
	t.Setenv("GH_TOKEN", "secret-read")
	t.Setenv("GIT_DIR", "secret-override")
	t.Setenv("GIT_CLIFF_TOKEN", "secret-cliff")
	data, err := process.Run(context.Background(), root, script, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), root) || strings.Contains(string(data), "secret-") {
		t.Fatalf("unsafe environment: %s", data)
	}
	data, err = process.Run(context.Background(), root, script, "", true)
	if err != nil || !strings.Contains(string(data), "GH_TOKEN=secret-read") || strings.Contains(string(data), "secret-app") {
		t.Fatal("explicit read grant failed")
	}
}

func TestRunNeverExposesChildStderr(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fail")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho secret-credential >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := process.Run(context.Background(), t.TempDir(), script, "", false)
	if err == nil || strings.Contains(err.Error(), "secret") || len(data) != 0 {
		t.Fatalf("unsafe error: %s %v", data, err)
	}
}
