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

// printNewPlan summarizes effective inputs and file counts. Full filenames are
// opt-in because a bundled skill can contribute hundreds of generated paths.
func printNewPlan(env console.Environment, options Options, inventory []files.File, conflicts []string) error {
	report := map[string]any{
		"destination": options.Folder, "module": options.Module, "dry_run": true,
		"yes": options.Yes, "file_count": len(inventory), "conflict_count": len(conflicts),
	}

	if env.Presentation.Verbose {
		report["files"], report["conflicts"] = files.Paths(inventory), conflicts
	}

	return env.Result(report)
}
