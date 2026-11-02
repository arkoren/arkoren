package notes

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
)

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func checkout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	gitTest(t, root, "config", "user.name", "Fixture")
	gitTest(t, root, "config", "user.email", "fixture@example.invalid")
	gitTest(t, root, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "baseline")
	gitTest(t, root, "remote", "add", "origin", "https://github.com/arkoren/arkoren")
	return root
}

func record(t *testing.T, root, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareReadsWorkingBytesForStagedUnstagedAndUntracked(t *testing.T) {
	root := checkout(t)
	name := "website/src/content/releases/core/v0.1.1.md"
	record(t, root, name, "staged")
	gitTest(t, root, "add", name)
	record(t, root, name, "working bytes")
	record(t, root, "website/src/content/releases/driver/redis/v0.2.0.md", "new")
	state, err := Prepare(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Records) != 2 || state.Records[0].Content != "d29ya2luZyBieXRlcw==" {
		t.Fatalf("wrong import: %+v", state)
	}
}

func TestPrepareRejectsUnrelatedUntrackedFile(t *testing.T) {
	root := checkout(t)
	record(t, root, "unexpected.txt", "human")
	if _, err := Prepare(context.Background(), root); err == nil {
		t.Fatal("untracked path accepted")
	}
}

func TestPrepareRejectsDeletion(t *testing.T) {
	root := checkout(t)
	name := "website/src/content/releases/core/v0.1.1.md"
	record(t, root, name, "release")
	gitTest(t, root, "add", name)
	gitTest(t, root, "-c", "commit.gpgsign=false", "commit", "-m", "notes")
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), root); err == nil {
		t.Fatal("deletion accepted")
	}
}

func TestPrepareRejectsSymlink(t *testing.T) {
	root := checkout(t)
	name := "website/src/content/releases/core/v0.1.1.md"
	record(t, root, name, "release")
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), root); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestNoopPublishUsesNoGitHubAccess(t *testing.T) {
	root := checkout(t)
	state, err := Prepare(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	client := github.Client{Root: root, Command: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("no-op requested GitHub access")
		return nil, nil
	}}
	if _, err := Publish(context.Background(), client, state); err != nil {
		t.Fatal(err)
	}
}

func TestPublishRejectsChangedImportBeforeGitHubAccess(t *testing.T) {
	root := checkout(t)
	record(t, root, "website/src/content/releases/core/v0.1.1.md", "original")
	state, err := Prepare(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	record(t, root, "website/src/content/releases/core/v0.1.1.md", "changed")
	client := github.Client{Root: root, Command: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("stale import requested GitHub access")
		return nil, nil
	}}
	if _, err := Publish(context.Background(), client, state); err == nil {
		t.Fatal("stale import accepted")
	}
}

// boundary runs a real HTTP server; it models response data, not GitHub checks.
func boundary(t *testing.T, handler http.HandlerFunc) github.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return github.Client{Command: func(ctx context.Context, input string, args ...string) ([]byte, error) {
		if len(args) < 4 || args[0] != "api" || args[1] != "--method" {
			return nil, fmt.Errorf("unexpected native command %v", args)
		}
		request, err := http.NewRequestWithContext(ctx, args[2], server.URL+"/"+args[3], strings.NewReader(input))
		if err != nil {
			return nil, err
		}
		response, err := server.Client().Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode >= 400 {
			return nil, fmt.Errorf("HTTP failure")
		}
		return io.ReadAll(response.Body)
	}}
}

func botCommit(sha string) Commit {
	commit := Commit{SHA: sha}
	commit.Author.Login, commit.Committer.Login = bot, bot
	commit.Commit.Verification.Verified = true
	commit.Commit.Tree.SHA = "tree"
	return commit
}

func botPull() Pull {
	pull := Pull{Number: 7, NodeID: "owned-pr", URL: "https://github.com/arkoren/arkoren/pull/7"}
	pull.User.Login = bot
	pull.Base.Ref, pull.Base.Repo.Name = "main", github.Repository
	pull.Head.Ref, pull.Head.Repo.Name, pull.Head.SHA = Branch, github.Repository, "head"
	return pull
}

// ownershipResponse returns only read fixtures; unexpected writes fail immediately.
func ownershipResponse(t *testing.T, writer http.ResponseWriter, request *http.Request, pull Pull, commits []Commit, total int, files any) {
	t.Helper()
	if request.Method != "GET" {
		t.Errorf("unexpected mutation: %s", request.URL)
		writer.WriteHeader(500)
		return
	}
	var value any
	switch request.URL.Path {
	case "/" + github.API + "/pulls":
		value = []Pull{pull}
	case "/" + github.API + "/git/matching-refs/heads/" + Branch:
		value = []any{map[string]any{"ref": "refs/heads/" + Branch, "object": map[string]string{"sha": "head"}}}
	case "/" + github.API + "/commits/head":
		value = botCommit("head")
	case "/" + github.API + "/compare/base...head":
		value = map[string]any{"total_commits": total, "commits": commits, "files": files}
	default:
		t.Errorf("unexpected API path %s", request.URL)
		writer.WriteHeader(500)
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Error(err)
	}
	writer.Write(data)
}

func TestInspectRejectsForeignHistoricalCommit(t *testing.T) {
	foreign := botCommit("old")
	foreign.Author.Login = "human"
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		ownershipResponse(t, writer, request, botPull(), []Commit{foreign, botCommit("head")}, 2, []any{})
	})
	if _, _, err := Inspect(context.Background(), client, State{Base: "base"}); err == nil {
		t.Fatal("foreign historical commit accepted")
	}
}

func TestInspectRejectsTruncatedHistory(t *testing.T) {
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		ownershipResponse(t, writer, request, botPull(), []Commit{botCommit("head")}, 101, []any{})
	})
	if _, _, err := Inspect(context.Background(), client, State{Base: "base"}); err == nil {
		t.Fatal("truncated history accepted")
	}
}

func TestInspectRejectsHeadMovement(t *testing.T) {
	pull := botPull()
	pull.Head.SHA = "concurrent"
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		ownershipResponse(t, writer, request, pull, []Commit{botCommit("head")}, 1, []any{})
	})
	if _, _, err := Inspect(context.Background(), client, State{Base: "base"}); err == nil {
		t.Fatal("race accepted")
	}
}

func TestInspectRejectsUnmanagedDiff(t *testing.T) {
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		ownershipResponse(t, writer, request, botPull(), []Commit{botCommit("head")}, 1, []any{map[string]string{"filename": "README.md", "status": "modified"}})
	})
	if _, _, err := Inspect(context.Background(), client, State{Base: "base"}); err == nil {
		t.Fatal("unmanaged branch diff accepted")
	}
}

func TestInspectRejectsUnsignedTip(t *testing.T) {
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/commits/head") {
			commit := botCommit("head")
			commit.Commit.Verification.Verified = false
			data, _ := json.Marshal(commit)
			writer.Write(data)
			return
		}
		ownershipResponse(t, writer, request, botPull(), []Commit{botCommit("head")}, 1, []any{})
	})
	if _, _, err := Inspect(context.Background(), client, State{Base: "base"}); err == nil {
		t.Fatal("unsigned tip accepted")
	}
}

func TestAutoMergeUsesNativeExactHeadMergeWithoutBypass(t *testing.T) {
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		ownershipResponse(t, writer, request, botPull(), []Commit{botCommit("head")}, 1, []any{})
	})
	read := client.Command
	calls := 0
	client.Command = func(ctx context.Context, input string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "api" {
			return read(ctx, input, args...)
		}
		calls++
		expected := []string{"pr", "merge", "7", "--repo", github.Repository, "--auto", "--merge", "--match-head-commit", "head"}
		if !reflect.DeepEqual(args, expected) || input != "" {
			t.Fatalf("unexpected merge command: %v", args)
		}
		return nil, nil
	}
	if err := AutoMerge(context.Background(), client, State{Base: "base"}, botPull(), "head"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("native auto-merge not requested")
	}
}
