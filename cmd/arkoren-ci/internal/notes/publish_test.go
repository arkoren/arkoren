package notes

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"reflect"
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
