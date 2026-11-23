package scaffold

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// Inspect checks the complete generated plan and every existing parent and
// final filename before approval. Planned file aliases and file/directory overlap
// are conflicts even when the destination does not exist; dangling symlinks also
// collide. Files outside the inventory, including .git, remain untouched. The
// returned conflicts feed both dry-run reports and execution's no-write refusal.
func Inspect(ctx context.Context, folder string, inventory []files.File) ([]string, error) {
	conflicts := newPlanConflicts(inventory)

	if err := files.CheckDirectory(ctx, folder); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return append(conflicts, "destination: "+err.Error()), nil
	}

	for _, file := range inventory {
		path := filepath.Join(folder, file.Path)

		if err := files.CheckDirectory(ctx, filepath.Dir(path)); err != nil {
			conflicts = append(conflicts, file.Path+": "+err.Error())
			continue
		}

		_, err := os.Lstat(path)

		if err == nil {
			conflicts = append(conflicts, file.Path)
			continue
		}

		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect %s: %w", path, err)
		}
	}

	return conflicts, ctx.Err()
}

// newResult is the JSON result of `arkoren new`, in the local action envelope.
// A dry run and a real run share it; only the summary wording, dry_run, the
// conflict fields and next_steps differ.
type newResult struct {
	// Summary reads "Created project <module> in <destination>", or "Would
	// create project <module> in <destination> (<n> files)" for a dry run.
	Summary string `json:"summary"`

	// DryRun reports that nothing was written.
	DryRun bool `json:"dry_run,omitzero"`

	// Module is the Go module path of the project.
	Module string `json:"module"`

	// Destination is the absolute project directory.
	Destination string `json:"destination"`

	// FileCount is the number of files in the project.
	FileCount int `json:"file_count"`

	// ConflictCount counts existing paths that block creation. Only a dry run
	// reports it, since a real run fails with an error instead.
	ConflictCount int `json:"conflict_count,omitzero"`

	// Conflicts lists the blocking paths with --verbose.
	Conflicts []string `json:"conflicts,omitzero"`

	// Changes lists every created file with --verbose, because a bundled skill
	// can contribute hundreds of paths.
	Changes []console.Change `json:"changes,omitzero"`

	// NextSteps lists how to run the new project.
	NextSteps []string `json:"next_steps,omitzero"`
}

// newChanges lists the inventory as create changes.
func newChanges(inventory []files.File) []console.Change {
	changes := make([]console.Change, 0, len(inventory))

	for _, path := range files.Paths(inventory) {
		changes = append(changes, console.Change{Action: console.ActionCreate, Path: path})
	}

	return changes
}

// printNewPlan summarizes effective inputs and file counts. Full filenames are
// opt-in because a bundled skill can contribute hundreds of generated paths.
func printNewPlan(env console.Environment, options Options, inventory []files.File, conflicts []string) error {
	report := newResult{
		Summary:       fmt.Sprintf("Would create project %s in %s (%d files)", options.Module, options.Folder, len(inventory)),
		DryRun:        true,
		Module:        options.Module,
		Destination:   options.Folder,
		FileCount:     len(inventory),
		ConflictCount: len(conflicts),
	}

	if env.Presentation.Verbose {
		report.Conflicts = conflicts
		report.Changes = newChanges(inventory)
	}

	return env.Result(report)
}
