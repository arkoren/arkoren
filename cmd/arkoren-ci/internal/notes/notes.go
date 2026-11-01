// Package notes confines release imports and preserves signed App branch history.
package notes

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// Branch is the only writable automation branch.
const Branch = "automation/release-notes"

// bot is the expected signed commit author and committer.
const bot = "arkoren-releases[bot]"

// recordPath accepts only the importer's canonical public Markdown paths.
var recordPath = regexp.MustCompile(`^website/src/content/releases/(core|cmd/arkoren|(driver|foundation|ark)/[a-z0-9-]+)/v0\.[0-9]+\.[0-9]+\.md$`)

// Record stores the original import bytes, independently of the Git index.
type Record struct {
	// Path is a canonical repository-relative release record.
	Path string `json:"path"`
	// Content is base64 encoded UTF-8 Markdown.
	Content string `json:"content"`
}

// State binds a bounded fresh import to its exact main base.
type State struct {
	// Base is the checkout HEAD used by the TypeScript importer.
	Base string `json:"base"`
	// Records contains additions and modifications, never deletions.
	Records []Record `json:"records"`
}

// Prepare inspects staged, unstaged and untracked changes before App access.
func Prepare(ctx context.Context, root string) (State, error) {
	remote, err := process.Run(ctx, root, "git", "", false, "remote", "get-url", "origin")
	if err != nil || strings.TrimSpace(string(remote)) != "https://github.com/arkoren/arkoren" {
		return State{}, fmt.Errorf("unexpected checkout repository")
	}
	head, err := process.Run(ctx, root, "git", "", false, "rev-parse", "HEAD")
	if err != nil {
		return State{}, err
	}
	status, err := process.Run(ctx, root, "git", "", false, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return State{}, err
	}
	state := State{Base: strings.TrimSpace(string(head)), Records: []Record{}}
	for entry := range strings.SplitSeq(string(status), "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) < 4 || !slices.Contains([]string{" M", "M ", "MM", "A ", "AM", "??"}, entry[:2]) {
			return State{}, fmt.Errorf("only release record additions and modifications permitted")
		}
		name := entry[3:]
		if !recordPath.MatchString(name) {
			return State{}, fmt.Errorf("change outside managed release records")
		}
		// Check every component: a symlinked directory is not an ordinary file.
		for current := name; current != "."; current = filepath.Dir(current) {
			info, err := os.Lstat(filepath.Join(root, current))
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return State{}, fmt.Errorf("release record contains a symlink or missing component")
			}
		}
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() {
			return State{}, fmt.Errorf("release record must be an ordinary file")
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return State{}, err
		}
		if !utf8.Valid(data) {
			return State{}, fmt.Errorf("release record must contain UTF-8")
		}
		state.Records = append(state.Records, Record{Path: name, Content: base64.StdEncoding.EncodeToString(data)})
	}
	slices.SortFunc(state.Records, func(left, right Record) int { return strings.Compare(left.Path, right.Path) })

	return state, nil
}

// Commit is the GitHub ownership and tree metadata used before any branch write.
type Commit struct {
	// SHA identifies the exact inspected head.
	SHA string `json:"sha"`
	// Author and Committer must both identify the release App.
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Committer struct {
		Login string `json:"login"`
	} `json:"committer"`
	// Commit carries GitHub's signature verification and tree identity.
	Commit struct {
		Verification struct {
			Verified bool `json:"verified"`
		} `json:"verification"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	} `json:"commit"`
}

// Pull is the ownership subset of a GitHub PR.
type Pull struct {
	// Number and NodeID bind reinspection to one PR identity.
	Number int    `json:"number"`
	NodeID string `json:"node_id"`
	// URL is returned for the workflow summary.
	URL string `json:"html_url"`
	// User identifies the PR creator.
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	// Base and Head bind the PR to the expected same-repository branches.
	Base pullRef `json:"base"`
	Head pullRef `json:"head"`
	// AutoMerge is nil until native auto-merge is requested.
	AutoMerge *struct {
		Method string `json:"merge_method"`
	} `json:"auto_merge"`
}

// pullRef carries the same-repository branch identity.
type pullRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo struct {
		Name string `json:"full_name"`
	} `json:"repo"`
}

// comparison must be complete; truncated commits or files prohibit writes.
type comparison struct {
	Total   int      `json:"total_commits"`
	Commits []Commit `json:"commits"`
	Files   []struct {
		Name   string `json:"filename"`
		Status string `json:"status"`
	} `json:"files"`
}

// verifiedBot rejects unsigned or foreign commits, including abandoned branches.
func verifiedBot(commit Commit) error {
	if commit.SHA == "" || commit.Author.Login != bot || commit.Committer.Login != bot || !commit.Commit.Verification.Verified {
		return fmt.Errorf("foreign or unsigned commit occupies automation branch")
	}

	return nil
}

// Inspect validates the entire visible branch history and PR before credentials.
func Inspect(ctx context.Context, client github.Client, state State) (*Commit, *Pull, error) {
	pulls, err := github.Pages[Pull](ctx, client, github.API+"/pulls?state=open&head=arkoren:"+Branch)
	if err != nil {
		return nil, nil, err
	}
	if len(pulls) > 1 {
		return nil, nil, fmt.Errorf("multiple release-note PRs require recovery")
	}
	var pull *Pull
	if len(pulls) == 1 {
		pull = &pulls[0]
		if pull.User.Login != bot || pull.Base.Ref != "main" || pull.Base.Repo.Name != github.Repository ||
			pull.Head.Ref != Branch || pull.Head.Repo.Name != github.Repository || pull.Number <= 0 || pull.NodeID == "" {
			return nil, nil, fmt.Errorf("foreign PR occupies automation branch")
		}
	}
	var refs []struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := client.Call(ctx, "GET", github.API+"/git/matching-refs/heads/"+Branch, nil, &refs); err != nil {
		return nil, nil, err
	}
	head := ""
	for _, ref := range refs {
		if ref.Ref == "refs/heads/"+Branch {
			if head != "" {
				return nil, nil, fmt.Errorf("duplicate automation ref")
			}
			head = ref.Object.SHA
		}
	}
	if head == "" {
		if pull != nil {
			return nil, nil, fmt.Errorf("PR branch disappeared")
		}
		return nil, nil, nil
	}
	tip := Commit{}
	if err := client.Call(ctx, "GET", github.API+"/commits/"+head, nil, &tip); err != nil {
		return nil, nil, err
	}
	if tip.SHA != head {
		return nil, nil, fmt.Errorf("branch head response mismatch")
	}
	if err := verifiedBot(tip); err != nil {
		return nil, nil, err
	}
	compared, err := compare(ctx, client, state.Base, head)
	if err != nil {
		return nil, nil, err
	}
	for _, commit := range compared.Commits {
		if err := verifiedBot(commit); err != nil {
			return nil, nil, err
		}
	}
	if pull != nil && pull.Head.SHA != head {
		return nil, nil, fmt.Errorf("branch changed during inspection")
	}

	return &tip, pull, nil
}

// compare rejects truncated responses and non-record edits.
func compare(ctx context.Context, client github.Client, base, head string) (comparison, error) {
	result := comparison{}
	if err := client.Call(ctx, "GET", github.API+"/compare/"+base+"..."+head+"?per_page=100", nil, &result); err != nil {
		return result, err
	}
	if result.Commits == nil || result.Files == nil || result.Total != len(result.Commits) || len(result.Files) >= 300 {
		return result, fmt.Errorf("branch history or diff exceeds ownership inspection limit")
	}
	for _, file := range result.Files {
		if !recordPath.MatchString(file.Name) || !slices.Contains([]string{"added", "modified"}, file.Status) {
			return result, fmt.Errorf("automation branch changes unmanaged paths or removes history")
		}
	}

	return result, nil
}

// Verify confirms native merge settings without changing repository protection.
func Verify(ctx context.Context, client github.Client) error {
	var repository struct {
		AutoMerge   bool `json:"allow_auto_merge"`
		MergeCommit bool `json:"allow_merge_commit"`
	}
	if err := client.Call(ctx, "GET", github.API, nil, &repository); err != nil {
		return err
	}
	if !repository.AutoMerge || !repository.MergeCommit {
		return fmt.Errorf("repository must permit native auto-merge and merge commits; retain main protections")
	}

	return nil
}
