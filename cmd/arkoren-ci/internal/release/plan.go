package release

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

// Commit preserves the signed-v1 filtered payload commit shape.
type Commit struct {
	// ID is the full commit object ID.
	ID string `json:"id"`
	// Message is the stripped original commit message.
	Message string `json:"message"`
	// Files contains only this module's releasable payload paths.
	Files []string `json:"files"`
}

// Entry records one module's baseline, proposal and notes.
type Entry struct {
	// Directory is the approved public root.
	Directory string `json:"directory"`
	// ModulePath is the canonical module declaration.
	ModulePath string `json:"module_path"`
	// CurrentVersion is null for explicitly authorized new modules.
	CurrentVersion *string `json:"current_version"`
	// NextVersion is null when no version is proposed.
	NextVersion *string `json:"next_version"`
	// SHA is the immutable candidate.
	SHA string `json:"sha"`
	// BaselineTag is the latest unambiguous reachable release tag.
	BaselineTag *string `json:"baseline_tag"`
	// Commits contains payload commits in Git topological order.
	Commits []Commit `json:"commits"`
	// Notes is generated exclusively by pinned git-cliff.
	Notes string `json:"notes"`
	// SkipReason is null when a version is proposed.
	SkipReason *string `json:"skip_reason"`
}

// Plan retains the existing artifact schema, not signed canonical JSON bytes.
type Plan struct {
	// SchemaVersion identifies the artifact shape.
	SchemaVersion int `json:"schema_version"`
	// DryRun is always true.
	DryRun bool `json:"dry_run"`
	// PublicationAuthorized is always false.
	PublicationAuthorized bool `json:"publication_authorized"`
	// SHA identifies the planned tree.
	SHA string `json:"sha"`
	// GitCliff identifies the verified parser version.
	GitCliff string `json:"git_cliff"`
	// Validation states whether evidence is attached.
	Validation string `json:"validation"`
	// Modules contains only sorted public namespaces.
	Modules []Entry `json:"modules"`
	// Evidence is optional exact-SHA metadata.
	Evidence any `json:"evidence,omitempty"`
}

// revision accepts immutable object IDs only.
var revision = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Payload applies path ownership independently of commit scope and message.
func Payload(name, module string) bool {
	relative, ok := strings.CutPrefix(name, module+"/")
	if !ok {
		return false
	}
	parts := strings.Split(relative, "/")
	if module == "cmd/arkoren" && parts[0] == "skills" {
		return true
	}
	if slices.Contains([]string{".md", ".mdx", ".rst"}, strings.ToLower(path.Ext(relative))) ||
		strings.HasSuffix(relative, "_test.go") ||
		slices.Contains([]string{"AGENTS.md", "CLAUDE.md", "LICENSE", "LICENSE.txt"}, path.Base(relative)) {
		return false
	}
	for _, part := range parts {
		if slices.Contains([]string{"testdata", "tests", "docs", ".github", ".agents", ".claude"}, part) {
			return false
		}
	}

	return true
}

// git reads history with explicit cwd and no credentials.
func git(ctx context.Context, root string, args ...string) (string, error) {
	data, err := process.Run(ctx, root, "git", "", false, args...)

	return string(data), err
}

// Prepare calculates proposals from committed policy, history and native cliff.
func Prepare(ctx context.Context, root, sha string, initial []string) (Plan, error) {
	return prepare(ctx, root, sha, initial, false)
}

// PrepareHistorical recomputes signed-v1 records whose original tree predates
// schema-1 module policy. It grants no current namespace or publishing approval.
func PrepareHistorical(ctx context.Context, root, sha string, initial []string) (Plan, error) {
	return prepare(ctx, root, sha, initial, true)
}

// prepare keeps historical decoding an explicit operation, not a policy fallback.
func prepare(ctx context.Context, root, sha string, initial []string, historical bool) (Plan, error) {
	if !revision.MatchString(sha) {
		return Plan{}, fmt.Errorf("provide an exact full commit SHA")
	}
	shallow, err := git(ctx, root, "rev-parse", "--is-shallow-repository")

	if err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(shallow) != "false" {
		return Plan{}, fmt.Errorf("full tag history required; shallow repository rejected")
	}
	inspect := repository.Inspect
	if historical {
		inspect = repository.ReleaseInventory
	}
	inventory, err := inspect(ctx, root, sha)

	if err != nil {
		return Plan{}, err
	}
	modules := []repository.Module{}
	for _, module := range inventory.Modules {
		if module.Classification == "public" {
			modules = append(modules, module)
		}
	}
	seen := map[string]bool{}
	for _, directory := range initial {
		if seen[directory] || !slices.ContainsFunc(modules, func(module repository.Module) bool {
			return module.Directory == directory
		}) {
			return Plan{}, fmt.Errorf("initial-module must name distinct canonical tracked modules")
		}
		seen[directory] = true
	}
	baselines, reachable, err := selectTags(ctx, root, sha, modules)

	if err != nil {
		return Plan{}, err
	}
	parser, cleanup, err := newCliff(ctx, root)

	if err != nil {
		return Plan{}, err
	}
	defer cleanup()
	history, err := readHistory(ctx, root, sha)

	if err != nil {
		return Plan{}, err
	}
	pin, err := Tool()

	if err != nil {
		return Plan{}, err
	}
	result := Plan{SchemaVersion: 1, DryRun: true, SHA: sha, GitCliff: pin["git_cliff"],
		Validation: "local plan only; no CI evidence attached", Modules: []Entry{}}
	for _, module := range modules {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		baseline := baselines[module.Directory]
		if err := validateInitial(ctx, root, module.Directory, baseline, seen[module.Directory], reachable); err != nil {
			return Plan{}, err
		}
		entry, err := prepareEntry(ctx, root, sha, module, baseline, history, parser)

		if err != nil {
			return Plan{}, err
		}
		result.Modules = append(result.Modules, entry)
	}

	return result, nil
}

// validateInitial prevents replaying imported history as a new release baseline.
func validateInitial(ctx context.Context, root, module string, baseline *tag, initial bool, reachable []string) error {
	if baseline != nil {
		if initial {
			return fmt.Errorf("initial-module already has a baseline: %s", module)
		}

		return nil
	}
	if !initial {
		return fmt.Errorf("missing release baseline for %s; require explicit --initial-module", module)
	}
	if len(reachable) == 0 {
		return fmt.Errorf("existing module %s has no baseline; recover its release tag", module)
	}
	for _, target := range reachable {
		data, err := git(ctx, root, "ls-tree", target, "--", module+"/go.mod")

		if err != nil {
			return err
		}
		if strings.TrimSpace(data) != "" {
			return fmt.Errorf("existing module %s has no baseline; do not bootstrap import history", module)
		}
	}

	return nil
}
