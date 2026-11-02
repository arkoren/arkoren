package notes

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
)

func TestAdvancePreservesAdvancedMainAndBotHistoryWithoutForce(t *testing.T) {
	writes := 0
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == "POST" && request.URL.Path == "/"+github.API+"/git/commits" {
			var payload struct {
				Parents   []string `json:"parents"`
				Tree      string   `json:"tree"`
				Author    any      `json:"author"`
				Committer any      `json:"committer"`
			}
			if err := json.UnmarshalRead(request.Body, &payload); err != nil {
				t.Error(err)
				writer.WriteHeader(500)
				return
			}
			if !reflect.DeepEqual(payload.Parents, []string{"owned-head", "fresh-main"}) || payload.Tree != "fresh-import-tree" || payload.Author != nil || payload.Committer != nil {
				t.Errorf("wrong signed commit inputs: %+v", payload)
			}
			writes++
			writer.Write([]byte(`{"sha":"signed-head","verification":{"verified":true}}`))
			return
		}
		if request.Method == "PATCH" && request.URL.Path == "/"+github.API+"/git/refs/heads/"+Branch {
			var payload map[string]any
			if err := json.UnmarshalRead(request.Body, &payload); err != nil {
				t.Error(err)
				writer.WriteHeader(500)
				return
			}
			if !reflect.DeepEqual(payload, map[string]any{"sha": "signed-head", "force": false}) {
				t.Errorf("unsafe ref update: %+v", payload)
			}
			writes++
			writer.Write([]byte(`{}`))
			return
		}
		t.Errorf("unexpected mutation %s %s", request.Method, request.URL)
		writer.WriteHeader(500)
	})
	tip := botCommit("owned-head")
	head, err := advance(context.Background(), client, "fresh-main", "fresh-import-tree", &tip)
	if err != nil {
		t.Fatal(err)
	}
	if head != "signed-head" || writes != 2 {
		t.Fatal("signed FF advancement not confirmed")
	}
}

// lifecycle models persisted GitHub state across complete publication attempts.
// It never simulates passing checks or merges: only native auto-merge requests.
type lifecycle struct {
	tip     Commit
	base    string
	behind  int
	tree    string
	commits int
	merges  int
	closed  bool
	missing bool
}

// client serves the mutable API boundary used by publication lifecycle tests.
func (fixture *lifecycle) client(t *testing.T, root string) github.Client {
	t.Helper()
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		pull := botPull()
		pull.Head.SHA = fixture.tip.SHA
		var value any
		switch {
		case request.Method == "GET" && request.URL.Path == "/"+github.API:
			value = map[string]bool{"allow_auto_merge": true, "allow_merge_commit": true}
		case request.Method == "GET" && strings.HasSuffix(request.URL.Path, "/pulls"):
			value = []Pull{pull}
		case request.Method == "GET" && strings.Contains(request.URL.Path, "/git/matching-refs/"):
			value = []any{map[string]any{"ref": "refs/heads/" + Branch, "object": map[string]string{"sha": fixture.tip.SHA}}}
		case request.Method == "GET" && strings.HasSuffix(request.URL.Path, "/commits/main"):
			main := Commit{SHA: fixture.base}
			main.Commit.Tree.SHA = "main-tree"
			value = main
		case request.Method == "GET" && strings.HasSuffix(request.URL.Path, "/commits/"+fixture.tip.SHA):
			value = fixture.tip
		case request.Method == "GET" && strings.Contains(request.URL.Path, "/compare/"):
			files := []any{}
			if fixture.missing {
				files = append(files, map[string]string{"filename": "website/src/content/releases/core/v0.1.1.md", "status": "added"})
			}
			value = map[string]any{"total_commits": 1, "behind_by": fixture.behind, "commits": []Commit{fixture.tip}, "files": files}
		case request.Method == "POST" && strings.HasSuffix(request.URL.Path, "/git/trees"):
			value = map[string]string{"sha": fixture.tree}
		case request.Method == "POST" && strings.HasSuffix(request.URL.Path, "/git/commits"):
			var payload struct {
				Parents []string `json:"parents"`
			}
			if err := json.UnmarshalRead(request.Body, &payload); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(payload.Parents, []string{fixture.tip.SHA, fixture.base}) {
				t.Fatalf("history not preserved: %v", payload.Parents)
			}
			fixture.commits++
			value = map[string]any{"sha": "refreshed", "verification": map[string]bool{"verified": true}}
		case request.Method == "PATCH" && strings.Contains(request.URL.Path, "/git/refs/"):
			var payload struct {
				SHA   string `json:"sha"`
				Force bool   `json:"force"`
			}
			if err := json.UnmarshalRead(request.Body, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Force || payload.SHA != "refreshed" {
				t.Fatalf("unsafe ref update: %+v", payload)
			}
			fixture.tip = botCommit(payload.SHA)
			fixture.tip.Commit.Tree.SHA = fixture.tree
			fixture.behind = 0
			value = map[string]any{}
		case request.Method == "PATCH" && strings.HasSuffix(request.URL.Path, "/pulls/7"):
			var payload map[string]string
			if err := json.UnmarshalRead(request.Body, &payload); err != nil {
				t.Fatal(err)
			}
			fixture.closed = payload["state"] == "closed"
			value = pull
		default:
			t.Errorf("unexpected lifecycle operation: %s %s", request.Method, request.URL)
			writer.WriteHeader(500)
			return
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		writer.Write(data)
	})
	read := client.Command
	client.Root = root
	client.Command = func(ctx context.Context, input string, args ...string) ([]byte, error) {
		if args[0] == "api" {
			return read(ctx, input, args...)
		}
		expected := []string{"pr", "merge", "7", "--repo", github.Repository, "--auto", "--merge", "--match-head-commit", fixture.tip.SHA}
		if !reflect.DeepEqual(args, expected) {
			t.Fatalf("unsafe native merge: %v", args)
		}
		fixture.merges++
		return nil, nil
	}
	return client
}

func TestPublishRefreshesBehindBranchEvenWhenTreeIsUnchanged(t *testing.T) {
	root := checkout(t)
	record(t, root, "website/src/content/releases/core/v0.1.1.md", "confirmed")
	state, err := Prepare(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := lifecycle{tip: botCommit("head"), base: state.Base, behind: 2, tree: "tree"}
	client := fixture.client(t, root)
	if _, err := Publish(context.Background(), client, state); err != nil {
		t.Fatal(err)
	}
	if fixture.commits != 1 || fixture.merges != 1 || fixture.behind != 0 {
		t.Fatalf("behind PR not refreshed: %+v", fixture)
	}
	if _, err := Publish(context.Background(), client, state); err != nil {
		t.Fatal(err)
	}
	if fixture.commits != 1 {
		t.Fatal("reconciliation restarted CI on an already-current branch")
	}
}

func TestPublishClosesSupersededPullWithoutCreatingCommit(t *testing.T) {
	root := checkout(t)
	state, err := Prepare(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := lifecycle{tip: botCommit("head"), base: state.Base, behind: 2}
	if _, err := Publish(context.Background(), fixture.client(t, root), state); err != nil {
		t.Fatal(err)
	}
	if !fixture.closed || fixture.commits != 0 || fixture.merges != 0 {
		t.Fatalf("superseded PR not safely closed: %+v", fixture)
	}
}

func TestPublishPreservesPendingAdditionMissingFromMainAndImport(t *testing.T) {
	root := checkout(t)
	state, err := Prepare(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := lifecycle{tip: botCommit("head"), base: state.Base, behind: 2, missing: true}
	if _, err := Publish(context.Background(), fixture.client(t, root), state); err == nil {
		t.Fatal("pending publication silently discarded")
	}
	if fixture.closed || fixture.commits != 0 || fixture.merges != 0 {
		t.Fatal("unconfirmed pending records caused a write")
	}
}

func TestPublishClosesPendingAdditionAlreadyInMain(t *testing.T) {
	root := checkout(t)
	name := "website/src/content/releases/core/v0.1.1.md"
	record(t, root, name, "confirmed")
	gitTest(t, root, "add", name)
	gitTest(t, root, "-c", "commit.gpgsign=false", "commit", "-m", "notes landed elsewhere")
	state, err := Prepare(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := lifecycle{tip: botCommit("head"), base: state.Base, behind: 2, missing: true}
	if _, err := Publish(context.Background(), fixture.client(t, root), state); err != nil {
		t.Fatal(err)
	}
	if !fixture.closed || fixture.commits != 0 {
		t.Fatal("notes already in main left a redundant PR open")
	}
}

func TestPublishRejectsMainAdvancementBeforeAnyWrite(t *testing.T) {
	root := checkout(t)
	record(t, root, "website/src/content/releases/core/v0.1.1.md", "confirmed")
	state, err := Prepare(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := lifecycle{tip: botCommit("head"), base: "advanced-main", behind: 2, tree: "tree"}
	if _, err := Publish(context.Background(), fixture.client(t, root), state); err == nil {
		t.Fatal("stale import was published after main advanced")
	}
	if fixture.closed || fixture.commits != 0 || fixture.merges != 0 {
		t.Fatal("stale import caused a remote write")
	}
}

func TestUnsignedAPICreationCannotAdvanceBranch(t *testing.T) {
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != "POST" || request.URL.Path != "/"+github.API+"/git/commits" {
			t.Error("unsigned API commit advanced branch")
			writer.WriteHeader(500)
			return
		}
		writer.Write([]byte(`{"sha":"unsigned","verification":{"verified":false}}`))
	})
	if _, err := advance(context.Background(), client, "base", "tree", nil); err == nil {
		t.Fatal("unsigned API commit accepted")
	}
}

func TestAutoMergeRaceRefusesNativeMutation(t *testing.T) {
	client := boundary(t, func(writer http.ResponseWriter, request *http.Request) {
		ownershipResponse(t, writer, request, botPull(), []Commit{botCommit("head")}, 1, []any{})
	})
	if err := AutoMerge(context.Background(), client, State{Base: "base"}, botPull(), "expected-old-head"); err == nil {
		t.Fatal("moved head accepted")
	}
}
