package notes

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// Publish creates signed API commits and advances only the owned branch by FF.
// The TypeScript importer remains the sole Markdown/schema validation authority.
func Publish(ctx context.Context, client github.Client, state State) (string, error) {
	current, err := Prepare(ctx, client.Root)
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(current, state) {
		return "", fmt.Errorf("import changed after preparation; rerun from main")
	}
	if err := Verify(ctx, client); err != nil {
		return "", err
	}
	tip, pull, err := Inspect(ctx, client, state)
	if err != nil {
		return "", err
	}
	main := Commit{}
	if err := client.Call(ctx, "GET", github.API+"/commits/main", nil, &main); err != nil {
		return "", err
	}
	if main.SHA != state.Base || main.Commit.Tree.SHA == "" {
		return "", fmt.Errorf("main advanced after import; rerun sync")
	}
	behind := false
	if tip != nil {
		pending, err := compare(ctx, client, state.Base, tip.SHA)
		if err != nil {
			return "", err
		}
		behind = *pending.Behind > 0
		imported := map[string]bool{}
		for _, record := range state.Records {
			imported[record.Path] = true
		}
		for _, file := range pending.Files {
			if file.Status == "added" && !imported[file.Name] {
				tracked, err := process.Run(ctx, client.Root, "git", "", false, "ls-tree", state.Base, "--", file.Name)
				if err != nil {
					return "", err
				}
				if !strings.HasPrefix(string(tracked), "100644 blob ") {
					return "", fmt.Errorf("pending publication absent from fresh import and main; preserve branch and retry")
				}
			}
		}
	}
	if len(state.Records) == 0 {
		return finish(ctx, client, state, pull, tip)
	}
	tree, err := createTree(ctx, client, state, main.Commit.Tree.SHA)
	if err != nil {
		return "", err
	}
	expected := ""
	if tip != nil {
		expected = tip.SHA
	}
	if tip == nil || tip.Commit.Tree.SHA != tree || behind {
		expected, err = advance(ctx, client, state.Base, tree, tip)
		if err != nil {
			return "", err
		}
	}
	links := []string{}
	for _, record := range state.Records {
		name := strings.TrimSuffix(strings.TrimPrefix(record.Path, "website/src/content/releases/"), ".md")
		links = append(links, fmt.Sprintf("- [%s](https://github.com/%s/releases/tag/%s)", name, github.Repository, name))
	}
	body := "Sync confirmed published GitHub notes into the website collection.\n\n" + strings.Join(links, "\n") +
		"\n\nOnly release records change. This job publishes no modules, tags or releases. " +
		"GitHub native auto-merge uses a merge commit after required CI gate and website checks " +
		"pass on an up-to-date branch. Failed, pending or canceled checks block merging. " +
		"No self-approval or main bypass is used."
	payload := map[string]any{"title": "Sync published module release notes", "body": body}
	endpoint, method := github.API+"/pulls", "POST"
	if pull == nil {
		payload["head"], payload["base"], payload["maintainer_can_modify"] = Branch, "main", false
	} else {
		endpoint, method = endpoint+"/"+strconv.Itoa(pull.Number), "PATCH"
	}
	updated := Pull{}
	if err := client.Call(ctx, method, endpoint, payload, &updated); err != nil {
		return "", err
	}
	if err := AutoMerge(ctx, client, state, updated, expected); err != nil {
		return "", err
	}

	return "GitHub green-only auto-merge requested: " + updated.URL, nil
}

// finish closes only an unchanged owned PR whose authoritative import is empty.
// Main already contains the desired records; the automation branch is retained.
func finish(ctx context.Context, client github.Client, state State, pull *Pull, tip *Commit) (string, error) {
	if pull == nil {
		return "No pending release-note PR or imported changes.", nil
	}
	currentTip, current, err := Inspect(ctx, client, state)
	if err != nil {
		return "", err
	}
	if tip == nil || currentTip == nil || current == nil || current.Number != pull.Number ||
		current.NodeID != pull.NodeID || currentTip.SHA != tip.SHA {
		return "", fmt.Errorf("PR or branch changed before closing; rerun sync")
	}
	main := Commit{}
	if err := client.Call(ctx, "GET", github.API+"/commits/main", nil, &main); err != nil {
		return "", err
	}
	if main.SHA != state.Base {
		return "", fmt.Errorf("main advanced before closing; rerun sync")
	}
	if err := client.Call(ctx, "PATCH", github.API+"/pulls/"+strconv.Itoa(current.Number),
		map[string]string{"state": "closed"}, nil); err != nil {
		return "", err
	}

	return "Closed superseded release-note PR; current main already matches confirmed publications: " + current.URL, nil
}

// createTree reconstructs current main plus only the validated imported records.
func createTree(ctx context.Context, client github.Client, state State, base string) (string, error) {
	items := []map[string]string{}
	for _, record := range state.Records {
		data, err := base64.StdEncoding.Strict().DecodeString(record.Content)
		if err != nil {
			return "", fmt.Errorf("invalid prepared record encoding")
		}
		items = append(items, map[string]string{"path": record.Path, "mode": "100644", "type": "blob", "content": string(data)})
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	if err := client.Call(ctx, "POST", github.API+"/git/trees", map[string]any{"base_tree": base, "tree": items}, &tree); err != nil {
		return "", err
	}
	if tree.SHA == "" {
		return "", fmt.Errorf("missing API tree identity")
	}

	return tree.SHA, nil
}

// advance preserves existing parents and never forces or adopts foreign history.
func advance(ctx context.Context, client github.Client, base, tree string, tip *Commit) (string, error) {
	parents := []string{base}
	if tip != nil && tip.SHA != base {
		parents = []string{tip.SHA, base}
	}
	var commit struct {
		SHA          string `json:"sha"`
		Verification struct {
			Verified bool `json:"verified"`
		} `json:"verification"`
	}
	if err := client.Call(ctx, "POST", github.API+"/git/commits", map[string]any{
		"message": "docs(website): sync published module release notes", "tree": tree, "parents": parents,
	}, &commit); err != nil {
		return "", err
	}
	if commit.SHA == "" || !commit.Verification.Verified {
		return "", fmt.Errorf("GitHub did not sign App commit; branch unchanged")
	}
	method, endpoint := "POST", github.API+"/git/refs"
	payload := map[string]any{"ref": "refs/heads/" + Branch, "sha": commit.SHA}
	if tip != nil {
		method, endpoint = "PATCH", endpoint+"/heads/"+Branch
		payload = map[string]any{"sha": commit.SHA, "force": false}
	}
	if err := client.Call(ctx, method, endpoint, payload, nil); err != nil {
		return "", err
	}

	return commit.SHA, nil
}

// AutoMerge reinspects ownership and delegates checks and merge policy to GitHub.
func AutoMerge(ctx context.Context, client github.Client, state State, pull Pull, expected string) error {
	tip, current, err := Inspect(ctx, client, state)
	if err != nil {
		return err
	}
	if tip == nil || current == nil || current.Number != pull.Number || current.NodeID != pull.NodeID || tip.SHA != expected {
		return fmt.Errorf("PR or branch changed before auto-merge; rerun sync")
	}
	if current.AutoMerge != nil {
		if current.AutoMerge.Method != "merge" {
			return fmt.Errorf("existing auto-merge must preserve signed commits with merge method")
		}
		return nil
	}
	_, err = client.Run(ctx, "", "pr", "merge", strconv.Itoa(current.Number), "--repo", github.Repository,
		"--auto", "--merge", "--match-head-commit", expected)

	return err
}
