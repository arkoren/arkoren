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
	// Dir names the parent directory into which every managed skill is installed.
	Dir string
	// DryRun previews change/conflict counts without writes; verbose lists paths.
	DryRun bool
}

// target is one managed skill's destination, bundle, inspected state and plan.
type target struct {
	// name is the skill's directory name under the skills parent.
	name string
	// folder is the skill's absolute installation directory.
	folder string
	// bundle is the embedded skill content and its manifest.
	bundle Bundle
	// installed is the inspected state of folder.
	installed Installation
	// report is the skill's own result document.
	report map[string]any
	// plan is the write plan, set for install and update only.
	plan Plan
}

// Manage operates exclusively on bundled documentation and local files.
// The CLI tree supplies a known operation and parsed options; all ownership,
// collision, and manifest-last guarantees remain in the existing skill planner.
//
// Every skill in [Names] is handled in one operation. All plans are built and
// checked first, so a conflict in any skill blocks every write. The result
// document describes the first skill (arkoren) at the top level and each other
// skill under "related_skills", keyed by name.
func Manage(ctx context.Context, options Options, env console.Environment, source fs.FS) error {
	parent, err := resolveSkillsDir(env.Dir, options.Dir)

	if err != nil {
		return err
	}

	targets := make([]*target, 0, len(Names))

	for _, name := range Names {
		bundle, err := LoadNamedBundle(ctx, source, name)

		if err != nil {
			return err
		}

		folder := filepath.Join(parent, name)
		installed, err := Inspect(ctx, folder)

		if err != nil {
			return err
		}

		targets = append(targets, &target{
			name: name, folder: folder, bundle: bundle, installed: installed,
			report: skillReport(folder, bundle, installed, env.Presentation.Verbose),
		})
	}

	report := targets[0].report
	related := make(map[string]any, len(targets)-1)

	for _, other := range targets[1:] {
		related[other.name] = other.report
	}

	report["related_skills"] = related

	if options.Operation == "status" {
		return env.Result(report)
	}

	var conflicts []string

	for _, current := range targets {
		operation := options.Operation

		// A skill added after the project's first install is absent from older
		// projects: update installs it beside the managed primary skill, while
		// the primary skill still requires an existing managed installation.
		if current != targets[0] && operation == "update" && current.installed.Manifest == nil && !current.installed.unmanaged {
			operation = "install"
		}

		current.plan, err = NewPlan(ctx, current.folder, operation, current.bundle, current.installed)

		if err != nil {
			return err
		}

		current.report["written_files"], current.report["removed_files"] = skillChangeCounts(current.plan)
		current.report["conflict_count"] = len(current.plan.Conflicts)

		if env.Presentation.Verbose {
			current.report["changes"], current.report["conflicts"] = skillChanges(current.plan), current.plan.Conflicts
		}

		for _, conflict := range current.plan.Conflicts {
			if current != targets[0] {
				conflict = current.name + ": " + conflict
			}

			conflicts = append(conflicts, conflict)
		}
	}

	report["operation"] = options.Operation
	report["dry_run"] = options.DryRun

	if len(conflicts) != 0 {
		if options.DryRun {
			if err := env.Result(report); err != nil {
				return err
			}

			return &console.ExitError{Code: console.ExitUsage}
		}

		return console.ConflictingPaths(options.Operation+" skill", targets[0].folder, conflicts)
	}

	if options.DryRun {
		return env.Result(report)
	}

	for _, current := range targets {
		if err := Write(ctx, current.folder, current.plan); err != nil {
			return err
		}

		current.report["installed_revision"] = current.bundle.Manifest.Revision
		current.report["modified_file_count"] = 0

		if env.Presentation.Verbose {
			current.report["modified_files"] = []string{}
		}

		current.report["state"] = "installed"
	}

	report["completed"] = true

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

// resolveSkillsDir returns the skills parent, anchoring a relative one to the
// supplied working directory; each skill lives in its own child directory.
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

	return parent, nil
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
