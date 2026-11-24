// Package skill owns the CLI managed consumer skill installation.
package skill

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
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

// Entry is one managed skill in a [Result]: where it lives, its provenance,
// and what an install or update did or would do to it.
type Entry struct {
	// Name is the skill's directory name under the skills parent.
	Name string `json:"name"`

	// State is "installed", "not installed" or "unmanaged" (a directory Arkoren
	// did not create).
	State string `json:"state"`

	// Directory is the skill's absolute installation directory.
	Directory string `json:"directory"`

	// InstalledRevision is the revision recorded when the skill was installed;
	// absent when it is not installed.
	InstalledRevision string `json:"installed_revision,omitzero"`

	// BundledRevision is the revision embedded in this CLI.
	BundledRevision string `json:"bundled_revision"`

	// ModifiedFileCount counts installed files edited since installation.
	ModifiedFileCount int `json:"modified_file_count"`

	// WrittenFiles counts files an install or update writes; absent for status.
	WrittenFiles *int `json:"written_files,omitzero"`

	// RemovedFiles counts files an install or update removes; absent for
	// status.
	RemovedFiles *int `json:"removed_files,omitzero"`

	// ConflictCount counts paths that block an install or update; absent for
	// status.
	ConflictCount *int `json:"conflict_count,omitzero"`

	// ModifiedFiles lists the edited files with --verbose.
	ModifiedFiles []string `json:"modified_files,omitzero"`

	// Conflicts lists the blocking paths with --verbose.
	Conflicts []string `json:"conflicts,omitzero"`
}

// Result is the JSON result of `arkoren skills install|update|status`: a
// summary, whether it was a dry run, one flat entry per managed skill, and with
// --verbose the file changes. Failure and conflicts are errors, not results.
type Result struct {
	// Summary reads "Installed 2 skills", "Would update 2 skills", "Skills are
	// up to date" or "arkoren skill is not installed".
	Summary string `json:"summary"`

	// DryRun reports that nothing was written; status never sets it.
	DryRun bool `json:"dry_run,omitzero"`

	// Skills lists every managed skill in installation order.
	Skills []Entry `json:"skills"`

	// Changes lists each file written or removed, with the path relative to the
	// skills directory, with --verbose.
	Changes []console.Change `json:"changes,omitzero"`
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
	// entry is the skill's own part of the result document.
	entry Entry
	// plan is the write plan, set for install and update only.
	plan Plan
}

// Manage operates exclusively on bundled documentation and local files.
// The CLI tree supplies a known operation and parsed options; all ownership,
// collision, and manifest-last guarantees remain in the existing skill planner.
//
// Every skill in [Names] is handled in one operation. All plans are built and
// checked first, so a conflict in any skill blocks every write. The result
// document lists each skill as one [Entry].
func Manage(ctx context.Context, options Options, env console.Environment, source fs.FS) error {
	parent, err := resolveSkillsDir(env.Dir, options.Dir)

	if err != nil {
		return err
	}

	verbose := env.Presentation.Verbose
	targets, err := loadTargets(ctx, parent, source, verbose)

	if err != nil {
		return err
	}

	if options.Operation == "status" {
		return env.Result(result(targets, verbose, statusSummary(targets), false))
	}

	conflicts, err := planTargets(ctx, options.Operation, targets, verbose)

	if err != nil {
		return err
	}

	report := result(targets, verbose, changeSummary(options, len(targets)), options.DryRun)

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

		current.markInstalled(verbose)
	}

	return env.Result(result(targets, verbose, changeSummary(options, len(targets)), false))
}

// loadTargets loads and inspects every managed skill under parent.
func loadTargets(ctx context.Context, parent string, source fs.FS, verbose bool) ([]*target, error) {
	targets := make([]*target, 0, len(Names))

	for _, name := range Names {
		bundle, err := LoadNamedBundle(ctx, source, name)

		if err != nil {
			return nil, err
		}

		folder := filepath.Join(parent, name)
		installed, err := Inspect(ctx, folder)

		if err != nil {
			return nil, err
		}

		targets = append(targets, &target{
			name: name, folder: folder, bundle: bundle, installed: installed,
			entry: skillEntry(name, folder, bundle, installed, verbose),
		})
	}

	return targets, nil
}

// planTargets builds every skill's write plan for the operation and returns the
// conflicts of all plans, each prefixed with its skill name except the first
// skill's. It records the file counts and conflicts on each entry.
func planTargets(ctx context.Context, operation string, targets []*target, verbose bool) ([]string, error) {
	var conflicts []string

	for _, current := range targets {
		planned := operation

		// A skill added after the project's first install is absent from older
		// projects: update installs it beside the managed primary skill, while
		// the primary skill still requires an existing managed installation.
		if current != targets[0] && operation == "update" && current.installed.Manifest == nil && !current.installed.unmanaged {
			planned = "install"
		}

		plan, err := NewPlan(ctx, current.folder, planned, current.bundle, current.installed)

		if err != nil {
			return nil, err
		}

		current.plan = plan
		written, removed := skillChangeCounts(plan)
		blocked := len(plan.Conflicts)

		current.entry.WrittenFiles = &written
		current.entry.RemovedFiles = &removed
		current.entry.ConflictCount = &blocked

		if verbose {
			current.entry.Conflicts = plan.Conflicts
		}

		for _, conflict := range plan.Conflicts {
			if current != targets[0] {
				conflict = current.name + ": " + conflict
			}

			conflicts = append(conflicts, conflict)
		}
	}

	return conflicts, nil
}

// markInstalled records on the entry that its plan was written.
func (current *target) markInstalled(verbose bool) {
	current.entry.InstalledRevision = current.bundle.Manifest.Revision
	current.entry.ModifiedFileCount = 0
	current.entry.State = "installed"

	if verbose {
		current.entry.ModifiedFiles = nil
	}
}

// result assembles the document: one entry per skill and, with verbose, every
// planned file change.
func result(targets []*target, verbose bool, summary string, dryRun bool) Result {
	report := Result{Summary: summary, DryRun: dryRun, Skills: make([]Entry, 0, len(targets))}

	for _, current := range targets {
		report.Skills = append(report.Skills, current.entry)

		if !verbose {
			continue
		}

		for _, change := range current.plan.Changes {
			action := console.ActionWrite

			if change.remove {
				action = console.ActionRemove
			}

			report.Changes = append(report.Changes, console.Change{Action: action, Path: path.Join(current.name, filepath.ToSlash(change.Path))})
		}
	}

	return report
}

// changeSummary words an install or update: "Installed 2 skills" or, for a dry
// run, "Would update 2 skills".
func changeSummary(options Options, count int) string {
	noun := "skills"

	if count == 1 {
		noun = "skill"
	}

	if options.DryRun {
		return fmt.Sprintf("Would %s %d %s", options.Operation, count, noun)
	}

	past := map[string]string{"install": "Installed", "update": "Updated"}[options.Operation]

	return fmt.Sprintf("%s %d %s", past, count, noun)
}

// statusSummary answers the status question in one line: the first skill's
// absence or foreign directory wins, then outdated skills, then local edits.
func statusSummary(targets []*target) string {
	first := targets[0].entry

	if first.State != "installed" {
		return first.Name + " skill is " + first.State
	}

	outdated, modified := 0, 0

	for _, current := range targets {
		if current.entry.State == "installed" && current.entry.InstalledRevision != current.entry.BundledRevision {
			outdated++
		}

		if current.entry.ModifiedFileCount > 0 {
			modified++
		}
	}

	switch {
	case outdated > 0:
		return fmt.Sprintf("%d of %d skills are out of date; run arkoren skills update", outdated, len(targets))
	case modified > 0:
		return fmt.Sprintf("%d of %d skills have locally modified files", modified, len(targets))
	}

	return "Skills are up to date"
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

// skillEntry describes installed provenance without implying API compatibility.
func skillEntry(name, folder string, bundle Bundle, installed Installation, verbose bool) Entry {
	entry := Entry{Name: name, Directory: folder, BundledRevision: bundle.Manifest.Revision}

	if installed.Manifest == nil {
		entry.State = "not installed"

		if installed.unmanaged {
			entry.State = "unmanaged"
		}

		return entry
	}

	entry.State = "installed"
	entry.InstalledRevision = installed.Manifest.Revision
	entry.ModifiedFileCount = len(installed.Modified)

	if verbose {
		entry.ModifiedFiles = installed.Modified
	}

	return entry
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
