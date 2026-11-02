package release

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

// change adds merge detection to the artifact's public commit shape.
type change struct {
	// commit retains original payload paths before per-module filtering.
	commit Commit
	// merge marks combined-diff resolutions that require explicit review.
	merge bool
}

// readHistory inspects actual commits; ordinary merges have no combined payload.
func readHistory(ctx context.Context, root, sha string) ([]change, error) {
	data, err := git(ctx, root, "rev-list", "--reverse", "--topo-order", sha)

	if err != nil {
		return nil, err
	}
	history := []change{}
	for _, id := range strings.Fields(data) {
		parents, err := git(ctx, root, "show", "-s", "--format=%P", id)

		if err != nil {
			return nil, err
		}
		merge := len(strings.Fields(parents)) > 1
		args := []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "--no-renames"}
		if merge {
			args = append(args, "--cc")
		}
		files, err := git(ctx, root, append(args, "-z", id)...)

		if err != nil {
			return nil, err
		}
		message, err := git(ctx, root, "show", "-s", "--format=%B", id)

		if err != nil {
			return nil, err
		}
		paths := strings.Split(files, "\x00")
		paths = slices.DeleteFunc(paths, func(name string) bool { return name == "" })
		history = append(history, change{commit: Commit{ID: id, Message: strings.TrimSpace(message), Files: paths}, merge: merge})
	}

	return history, nil
}

// prepareEntry filters actual paths before asking git-cliff to parse or bump.
func prepareEntry(ctx context.Context, root, sha string, module repository.Module, baseline *tag, history []change, parser cliff) (Entry, error) {
	skip := "no releasable payload changes since this module's tag"
	entry := Entry{Directory: module.Directory, ModulePath: module.ModulePath, SHA: sha,
		Commits: []Commit{}, SkipReason: &skip}
	rangeSpec := sha
	if baseline != nil {
		entry.CurrentVersion = &baseline.version
		entry.BaselineTag = &baseline.name
		rangeSpec = baseline.target + ".." + sha
	}
	data, err := git(ctx, root, "rev-list", rangeSpec)

	if err != nil {
		return Entry{}, err
	}
	included := strings.Fields(data)
	for _, item := range history {
		if !slices.Contains(included, item.commit.ID) {
			continue
		}
		files := []string{}
		for _, name := range item.commit.Files {
			if Payload(name, module.Directory) {
				files = append(files, name)
			}
		}
		if len(files) == 0 {
			continue
		}
		if item.merge {
			return Entry{}, fmt.Errorf("merge-only payload changes in %s at %s; record a non-merge commit", module.Directory, item.commit.ID)
		}
		entry.Commits = append(entry.Commits, Commit{ID: item.commit.ID, Message: item.commit.Message, Files: files})
	}
	if len(entry.Commits) == 0 {
		return entry, nil
	}
	pattern := "^$"
	if baseline != nil {
		pattern = "^" + regexp.QuoteMeta(baseline.name) + "$"
	}
	args := []string{"--tag-pattern", pattern, rangeSpec}
	slices.Sort(included)
	for _, id := range included {
		if !slices.ContainsFunc(entry.Commits, func(item Commit) bool { return item.ID == id }) {
			args = append(args, "--skip-commit", id)
		}
	}
	contextJSON, err := parser.run(ctx, "", append(args, "--context")...)

	if err != nil {
		return Entry{}, err
	}
	if err := validateContext(contextJSON, entry.Commits, baseline); err != nil {
		return Entry{}, err
	}
	proposal, err := parser.run(ctx, contextJSON, "--from-context", "-", "--bumped-version")

	if err != nil {
		return Entry{}, err
	}
	next := strings.TrimPrefix(strings.TrimSpace(proposal), module.Directory+"/")
	numbers, err := versionNumbers(next)

	if err != nil {
		return Entry{}, err
	}
	if baseline != nil && compareVersions(numbers, baseline.numbers) < 0 {
		return Entry{}, fmt.Errorf("regressive proposal for %s", module.Directory)
	}
	if baseline == nil || next != baseline.version {
		entry.NextVersion = &next
		entry.SkipReason = nil
		entry.Notes, err = parser.run(ctx, contextJSON, "--from-context", "-", "--bump")

		if err != nil {
			return Entry{}, err
		}
	}

	return entry, nil
}

// validateContext rejects ignore files, filters or tag-map collisions that change
// the exact set of payload commits supplied to git-cliff.
func validateContext(data string, commits []Commit, baseline *tag) error {
	contexts := []struct {
		Commits []struct {
			ID string `json:"id"`
		} `json:"commits"`
		Previous *struct {
			Version string `json:"version"`
		} `json:"previous"`
	}{}
	if err := json.Unmarshal([]byte(data), &contexts); err != nil {
		return fmt.Errorf("invalid git-cliff context: %w", err)
	}
	actual := []string{}
	for _, item := range contexts {
		for _, commit := range item.Commits {
			actual = append(actual, commit.ID)
		}
	}
	want := make([]string, 0, len(commits))
	for _, commit := range commits {
		want = append(want, commit.ID)
	}
	slices.Sort(actual)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		return fmt.Errorf("git-cliff omitted or added payload commits; inspect ignore files and filters")
	}
	if baseline != nil && (len(contexts) != 1 || contexts[0].Previous == nil || contexts[0].Previous.Version != baseline.name) {
		return fmt.Errorf("git-cliff baseline mismatch")
	}

	return nil
}
