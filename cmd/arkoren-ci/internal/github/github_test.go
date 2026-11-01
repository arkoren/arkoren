package github_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
)

func TestPagesReadsPastFirstFullPage(t *testing.T) {
	client := github.Client{Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[3], "page=1") {
			return []byte("[" + strings.Repeat(`{"id":1},`, 99) + `{"id":1}]`), nil
		}
		if strings.HasSuffix(args[3], "page=2") {
			return []byte(`[{"id":2}]`), nil
		}
		return nil, fmt.Errorf("unexpected page")
	}}
	items, err := github.Pages[struct {
		ID int `json:"id"`
	}](context.Background(), client, "endpoint?state=open")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 101 || items[100].ID != 2 {
		t.Fatal("truncated pagination")
	}
}

func TestPagesRejectsNonArrayResponse(t *testing.T) {
	client := github.Client{Command: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"message":"not a list"}`), nil
	}}
	if _, err := github.Pages[string](context.Background(), client, "endpoint"); err == nil {
		t.Fatal("invalid pagination response accepted")
	}
}

func TestScopedTokenNeverEntersArgvOrDiagnostics(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
test "$GH_TOKEN" = scoped-token || exit 2
test -z "${SIGNING_KEY-}${APP_TOKEN-}${GITHUB_TOKEN-}" || exit 3
case "$*" in *scoped-token*|*secret-signing*) exit 4;; esac
echo 'remote credential: scoped-token secret-signing' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SIGNING_KEY", "secret-signing")
	t.Setenv("APP_TOKEN", "ambient-app")
	t.Setenv("GITHUB_TOKEN", "ambient-builtin")
	client := github.Client{Token: "scoped-token"}
	err := client.Call(context.Background(), "GET", "endpoint", nil, nil)
	if err == nil || strings.Contains(err.Error(), "scoped-token") || strings.Contains(err.Error(), "secret-signing") {
		t.Fatalf("unsafe diagnostic: %v", err)
	}
}

func TestVerifyAppRejectsBroaderRepositoryScope(t *testing.T) {
	client := github.Client{Command: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"total_count":2,"repositories":[{"full_name":"arkoren/arkoren"}]}`), nil
	}}
	if err := client.VerifyApp(context.Background(), "arkoren-releases", "176874315"); err == nil {
		t.Fatal("broad App scope accepted")
	}
}
