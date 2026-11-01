// Package skill owns the CLI managed consumer skill installation.
package skill

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// Options selects one offline skill operation and its destination.
type Options struct {
	// Operation is install, update, or status.
	Operation string
	// Dir names the parent directory into which arkoren is installed.
	Dir string
	// DryRun previews change/conflict counts without writes; verbose lists paths.
	DryRun bool
}

// Manage operates exclusively on bundled documentation and local files.
// The CLI tree supplies a known operation and parsed options; all ownership,
// collision, and manifest-last guarantees remain in the existing skill planner.
func Manage(ctx context.Context, options Options, env console.Environment, source fs.FS) error {
	folder, err := resolveSkillsDir(env.Dir, options.Dir)

	if err != nil {
		return err
	}

	bundle, err := LoadBundle(ctx, source)

	if err != nil {
		return err
	}

	installed, err := Inspect(ctx, folder)

	if err != nil {
		return err
	}

	report := skillReport(folder, bundle, installed, env.Presentation.Verbose)

	if options.Operation == "status" {
		return env.Result(report)
	}

	plan, err := NewPlan(ctx, folder, options.Operation, bundle, installed)

	if err != nil {
		return err
	}

	report["operation"] = options.Operation
	report["dry_run"] = options.DryRun
	report["written_files"], report["removed_files"] = skillChangeCounts(plan)
	report["conflict_count"] = len(plan.Conflicts)

	if env.Presentation.Verbose {
		report["changes"], report["conflicts"] = skillChanges(plan), plan.Conflicts
	}

	if len(plan.Conflicts) != 0 {
		if options.DryRun {
			if err := env.Result(report); err != nil {
				return err
			}

			return &console.ExitError{Code: console.ExitUsage}
		}

		return console.ConflictingPaths(options.Operation+" skill", folder, plan.Conflicts)
	}

	if options.DryRun {
		return env.Result(report)
	}

	if err := Write(ctx, folder, plan); err != nil {
		return err
	}

	report["installed_revision"] = bundle.Manifest.Revision
	report["modified_file_count"] = 0

	if env.Presentation.Verbose {
		report["modified_files"] = []string{}
	}

	report["state"] = "installed"
	report["completed"] = true
	report["written_files"], report["removed_files"] = skillChangeCounts(plan)

	return env.Result(report)
}

// skillChangeCounts keeps ordinary receipts small. Full per-file actions are
// retained only when verbose output is explicitly requested.
func skillChangeCounts(plan Plan) (written, removed int) {
	for _, change := range plan.Changes {
		if change.remove {
			removed++
			continue
		}

		written++
	}

	return written, removed
}

// resolveSkillsDir anchors relative destinations to the supplied working directory.
// Only the working directory's system aliases are resolved; chosen links fail.
func resolveSkillsDir(dir, parent string) (string, error) {
	if parent == "" {
		return "", console.UsageError("skills directory must not be empty")
	}

	base, err := filepath.EvalSymlinks(dir)

	if err != nil {
		return "", err
	}

	base, err = filepath.Abs(base)

	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(parent) {
		parent = filepath.Join(base, parent)
	}

	return filepath.Join(parent, "arkoren"), nil
}

// skillReport describes installed provenance without implying API compatibility.
func skillReport(folder string, bundle Bundle, installed Installation, verbose bool) map[string]any {
	report := map[string]any{"directory": folder, "bundled_revision": bundle.Manifest.Revision}

	if installed.Manifest == nil {
		state := "not installed"

		if installed.unmanaged {
			state = "unmanaged"
		}

		report["state"] = state

		return report
	}

	report["state"] = "installed"
	report["installed_revision"] = installed.Manifest.Revision
	report["modified_file_count"] = len(installed.Modified)

	if verbose {
		report["modified_files"] = installed.Modified
	}

	return report
}

// skillChanges describes deterministic file actions without exposing contents.
func skillChanges(plan Plan) []map[string]string {
	changes := make([]map[string]string, 0, len(plan.Changes))

	for _, change := range plan.Changes {
		operation := "write"

		if change.remove {
			operation = "remove"
		}

		changes = append(changes, map[string]string{"operation": operation, "path": filepath.ToSlash(change.Path)})
	}

	return changes
}

// skillDirectoryUnmanaged distinguishes an empty destination from foreign data.
func skillDirectoryUnmanaged(ctx context.Context, folder string) (bool, error) {
	root, err := files.OpenDirectory(ctx, folder, false)

	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	defer root.Close()

	entries, err := fs.ReadDir(root.FS(), ".")

	return len(entries) != 0, err
}
