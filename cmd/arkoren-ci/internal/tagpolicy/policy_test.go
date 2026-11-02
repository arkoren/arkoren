package tagpolicy

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
)

func TestCreationOnlyAppPolicyNeverIncludesDevelopmentModule(t *testing.T) {
	rules := creationRules([]string{"core", "cmd/arkoren"})
	if len(rules) != 2 || len(rules[0].Actors) != 0 || len(rules[1].Actors) != 1 || rules[1].Actors[0].ID != 5540931 {
		t.Fatal("App creation scope changed")
	}
	for _, rule := range rules {
		if len(rule.Rules) != 1 || rule.Rules[0].Type != "creation" {
			t.Fatal("App can bypass immutability")
		}
		for _, pattern := range rule.Conditions.Ref.Include {
			if strings.Contains(pattern, "arkoren-ci") {
				t.Fatal("development namespace approved")
			}
		}
	}
}

func lockedPolicy() Ruleset {
	value := Ruleset{Name: "immutable", Target: "tag", Enforcement: "active", Actors: []Actor{}, Rules: []Rule{{Type: "creation"}, {Type: "update"}, {Type: "deletion"}, {Type: "non_fast_forward"}}}
	value.Conditions.Ref.Include = []string{"refs/tags/v*", "refs/tags/**/v*"}
	value.Conditions.Ref.Exclude = []string{}
	return value
}

func TestImmutableRejectsAnyBypass(t *testing.T) {
	record := lockedPolicy()
	record.Actors = []Actor{{ID: 5540931, Type: "Integration", Mode: "always"}}
	if err := immutable(record, true); err == nil {
		t.Fatal("App allowed to bypass tag immutability")
	}
}

func TestImmutableRejectsNamespaceException(t *testing.T) {
	record := lockedPolicy()
	record.Conditions.Ref.Exclude = []string{"refs/tags/core/v0.*"}
	if err := immutable(record, true); err == nil {
		t.Fatal("immutability exclusion accepted")
	}
}

func TestCreationShapeRejectsUnrecognizedNestedField(t *testing.T) {
	expected := creationRules([]string{"core", "cmd/arkoren"})[1]
	data, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["conditions"].(map[string]any)["unexpected"] = true
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if matchesShape(data, expected) {
		t.Fatal("unrecognized nested condition accepted")
	}
}

func policyClient(t *testing.T, loseResponse bool) (github.Client, map[int]jsontext.Value, *int) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github/release-modules.json"), []byte(`{"schema":1,"public":["core","cmd/arkoren"],"development":["cmd/arkoren-ci"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := Ruleset{Name: "Protect main", Target: "branch", Enforcement: "active", Actors: []Actor{}, Rules: []Rule{
		{Type: "pull_request", Parameters: jsontext.Value(`{"required_approving_review_count":0}`)},
		{Type: "required_status_checks", Parameters: jsontext.Value(`{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"CI gate"},{"context":"website"}]}`)},
	}}
	main.Conditions.Ref.Include = []string{"~DEFAULT_BRANCH"}
	main.Conditions.Ref.Exclude = []string{}
	records := map[int]jsontext.Value{}
	values := map[int]Ruleset{mainID: main, immutableID: lockedPolicy(), 26325347: creationRules([]string{"core", "cmd/arkoren"})[0], 26325350: creationRules([]string{"core", "cmd/arkoren"})[1]}
	for id, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		records[id] = data
	}
	writes := 0
	client := github.Client{Root: root, Command: func(_ context.Context, input string, args ...string) ([]byte, error) {
		if len(args) < 4 || args[0] != "api" {
			return nil, fmt.Errorf("unexpected gh boundary")
		}
		method, endpoint := args[2], args[3]
		if strings.Contains(endpoint, "?") {
			if method != "GET" {
				t.Fatal("mutated ruleset inventory")
			}
			items := []map[string]any{}
			for id, value := range values {
				items = append(items, map[string]any{"id": id, "name": value.Name})
			}
			return json.Marshal(items)
		}
		id, err := strconv.Atoi(endpoint[strings.LastIndex(endpoint, "/")+1:])
		if err != nil {
			return nil, err
		}
		if method == "PUT" {
			if id != immutableID {
				t.Fatal("activation modified main or creation-only rulesets")
			}
			writes++
			records[id] = jsontext.Value(input)
			if loseResponse && writes == 1 {
				return nil, fmt.Errorf("lost update response")
			}
		} else if method != "GET" {
			t.Fatal("unexpected policy mutation")
		}
		return records[id], nil
	}}
	return client, records, &writes
}

func TestActivationRemovesOnlyCreationAndPreservesFullMainRecord(t *testing.T) {
	client, records, writes := policyClient(t, false)
	before := records[mainID].Clone()
	output := filepath.Join(t.TempDir(), "snapshot")
	if err := Activate(context.Background(), client, output); err != nil {
		t.Fatal(err)
	}
	if *writes != 1 || !sameRecord(before, records[mainID]) {
		t.Fatal("main changed or unexpected mutation")
	}
	actual := Ruleset{}
	if err := json.Unmarshal(records[immutableID], &actual); err != nil {
		t.Fatal(err)
	}
	if err := immutable(actual, false); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(actual.Rules, func(rule Rule) bool { return rule.Type == "update" }) {
		t.Fatal("tag update protection lost")
	}
}

func TestLostActivationResponseRestoresGlobalCreationLock(t *testing.T) {
	client, records, writes := policyClient(t, true)
	if err := Activate(context.Background(), client, filepath.Join(t.TempDir(), "snapshot")); err == nil {
		t.Fatal("lost response accepted")
	}
	actual := Ruleset{}
	if err := json.Unmarshal(records[immutableID], &actual); err != nil {
		t.Fatal(err)
	}
	if *writes != 2 {
		t.Fatal("global creation lock was not restored first")
	}
	if err := immutable(actual, true); err != nil {
		t.Fatal(err)
	}
}
