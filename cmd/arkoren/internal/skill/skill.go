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
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// Options selects one offline skill operation and its destination.
type Options struct {
	// Operation is install, update, or status.
	Operation string
	// Dir names the parent directory into which every managed skill is installed.
	Dir string
	// DryRun previews the changes and conflicts without writes.
	DryRun bool
}

// Entry is one managed skill in a [Result] or [StatusResult]: where it lives
// and its provenance. What an install or update does to its files is reported
// by the result's changes, never by counters here.
type Entry struct {
	// Name is the skill's directory name under the skills parent.
	Name string `json:"name"`

	// State is the lifecycle of the skill's directory: "installed", "not
	// installed" or "unmanaged" (a directory Arkoren did not create).
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

	// ModifiedFiles lists the edited files with --verbose.
	ModifiedFiles []string `json:"modified_files,omitzero"`
}

// Result is the JSON result of `arkoren skills install|update`, in the local
// action envelope: a summary, whether it was a dry run, one flat entry per
// managed skill and, with --verbose, the file changes. A real run that meets
// conflicts is an error. A dry run that would is a report with ok false, the
// conflicts as problems and the steps that resolve them, ending with the exit
// status the real run would have.
type Result struct {
	// Summary reads "Installed 2 skills (14 files)", "Would update 2 skills (3
	// files)", "Skills are already up to date" or, for a dry run that would
	// fail, "Would fail: 1 skill has conflicting files".
	Summary string `json:"summary"`

	// DryRun reports that nothing was written.
	DryRun bool `json:"dry_run,omitzero"`

	// OK is false, and only then present, for a dry run that would fail.
	OK *bool `json:"ok,omitzero"`

	// Skills lists every managed skill in installation order.
	Skills []Entry `json:"skills"`

	// Problems lists each conflict that would block the real run, naming its
	// skill; absent when there is none.
	Problems []console.Problem `json:"problems,omitzero"`

	// Changes lists each file created, written or removed, with the path
	// relative to the project when the skills directory is inside it, with
	// --verbose.
	Changes []console.Change `json:"changes,omitzero"`

	// NextSteps resolves the conflicts of a dry run that would fail.
	NextSteps []console.Step `json:"next_steps,omitzero"`
}

// StatusResult is the JSON result of `arkoren skills status`, in the local check
// envelope: ok is true only when every skill is installed, current and
// unmodified, and problems say which is not.
type StatusResult struct {
	// Summary answers the status question in one line, such as "Skills are up
	// to date" or "arkoren skill is not installed".
	Summary string `json:"summary"`

	// OK is false when any skill is not installed, out of date or modified.
	OK bool `json:"ok"`

	// Skills lists every managed skill in installation order.
	Skills []Entry `json:"skills"`

	// Problems names each skill that is not ok and why; an empty array when OK.
	Problems []console.Problem `json:"problems"`

	// NextSteps says how to bring the skills up to date.
	NextSteps []console.Step `json:"next_steps,omitzero"`
}

// target is one managed skill's destination, bundle, inspected state and plan.
type target struct {
	// name is the skill's directory name under the skills parent.
	name string
	// folder is the skill's absolute installation directory.
	folder string
	// shown is folder as results name it: project-relative inside the project.
	shown string
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
	parent, base, err := resolveSkillsDir(env.Dir, options.Dir)

	if err != nil {
		return err
	}

	verbose := env.Presentation.Verbose
	targets, err := loadTargets(ctx, parent, base, source, verbose)

	if err != nil {
		return err
	}

	if options.Operation == "status" {
		return reportStatus(env, options, targets)
	}

	conflicts, err := planTargets(ctx, options.Operation, targets)

	if err != nil {
		return err
	}

	report := result(targets, verbose, changeSummary(options, targets), options.DryRun)

	if len(conflicts) != 0 {
		return refuse(env, report, targets, options, conflicts)
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

	return env.Result(result(targets, verbose, changeSummary(options, targets), false))
}

// refuse ends an install or update that met conflicts. The real run fails with
// a file conflict. A preview prints the report of that failure, with ok false,
// the conflicts as problems and the steps that resolve them, then repeats only
// the exit status the real run would end with.
func refuse(env console.Environment, report Result, targets []*target, options Options, conflicts []string) error {
	if !options.DryRun {
		return console.ConflictingPaths(options.Operation+" skill", targets[0].folder, conflicts)
	}

	if err := env.Result(blocked(report, targets, options)); err != nil {
		return err
	}

	return &console.ExitError{Code: console.ExitCodeFor(console.TypeFileConflict), Type: console.TypeFileConflict}
}

// reportStatus prints the status of every skill and ends with exit status 1
// when any is not installed, out of date or modified.
func reportStatus(env console.Environment, options Options, targets []*target) error {
	report := StatusResult{
		Summary:  statusSummary(targets),
		Skills:   make([]Entry, 0, len(targets)),
		Problems: []console.Problem{},
	}

	for _, current := range targets {
		report.Skills = append(report.Skills, current.entry)
		problems, steps := statusProblems(current, options)
		report.Problems = append(report.Problems, problems...)

		for _, step := range steps {
			if !slices.Contains(report.NextSteps, step) {
				report.NextSteps = append(report.NextSteps, step)
			}
		}
	}

	report.OK = len(report.Problems) == 0

	if err := env.Result(report); err != nil {
		return err
	}

	if !report.OK {
		return &console.ExitError{Code: console.ExitFailure}
	}

	return nil
}

// statusProblems says what is wrong with one skill, with the steps that fix it:
// a missing skill is installed, an out-of-date one is updated, and locally
// modified files are the reader's to review.
func statusProblems(current *target, options Options) ([]console.Problem, []console.Step) {
	entry := current.entry

	if entry.State != "installed" {
		return []console.Problem{{Field: entry.Name, Message: entry.State}},
			[]console.Step{{Text: "Install the skills", Command: skillsCommand("install", options.Dir)}}
	}

	var problems []console.Problem
	var steps []console.Step

	if entry.InstalledRevision != entry.BundledRevision {
		problems = append(problems, console.Problem{Field: entry.Name, Message: "out of date: the installed revision differs from the one bundled with this CLI"})
		steps = append(steps, console.Step{Text: "Update the skills", Command: skillsCommand("update", options.Dir)})
	}

	if entry.ModifiedFileCount > 0 {
		problems = append(problems, console.Problem{Field: entry.Name, Message: modifiedMessage(entry.ModifiedFileCount)})
		steps = append(steps, console.Step{Text: "Revert or move aside the locally modified files; an update refuses to overwrite them"})
	}

	return problems, steps
}

// modifiedMessage words the count of locally modified files.
func modifiedMessage(count int) string {
	if count == 1 {
		return "1 file modified locally"
	}

	return fmt.Sprintf("%d files modified locally", count)
}

// skillsCommand renders `arkoren skills <operation>` with --dir when the skills
// directory is not the default, so the step works from the same place.
func skillsCommand(operation, dir string) string {
	command := "arkoren skills " + operation

	if dir != DefaultSkillsDir {
		command += " --dir " + console.Quote(dir)
	}

	return command
}

// blocked turns the report of a dry run that met conflicts into the report of
// the failure the real run would end with: ok false, a summary that says it
// would fail, one problem per conflict and the steps that resolve them.
func blocked(report Result, targets []*target, options Options) Result {
	skills := 0

	for _, current := range targets {
		if len(current.plan.Conflicts) == 0 {
			continue
		}

		skills++

		for _, conflict := range current.plan.Conflicts {
			report.Problems = append(report.Problems, conflictProblem(current, conflict))

			if step := conflictStep(conflict, options); !slices.Contains(report.NextSteps, step) {
				report.NextSteps = append(report.NextSteps, step)
			}
		}
	}

	failing := false
	report.OK = &failing
	report.Summary = "Would fail: 1 skill has conflicting files"

	if skills != 1 {
		report.Summary = fmt.Sprintf("Would fail: %d skills have conflicting files", skills)
	}

	return report
}

// conflictProblem describes one plan conflict of a skill. The conflicts that are
// not about a file read as they are; a file conflict names the file, relative
// to the project when the skills directory is inside it, and why: the plan's
// reason, or that the file was modified since installation.
func conflictProblem(current *target, conflict string) console.Problem {
	switch conflict {
	case conflictUnmanaged, conflictNoInstallation, conflictOtherRevision:
		return console.Problem{Field: current.name, Message: conflict}
	}

	file, reason, found := strings.Cut(conflict, ": ")

	if !found {
		reason = "modified since installation"
	}

	return console.Problem{Field: current.name, Path: path.Join(current.shown, filepath.ToSlash(file)), Message: reason}
}

// conflictStep is the step that resolves one kind of plan conflict.
func conflictStep(conflict string, options Options) console.Step {
	switch conflict {
	case conflictNoInstallation:
		return console.Step{Text: "Install the skills first", Command: skillsCommand("install", options.Dir)}
	case conflictOtherRevision:
		return console.Step{Text: "Update the skills instead", Command: skillsCommand("update", options.Dir)}
	case conflictUnmanaged:
		return console.Step{Text: "Move the unmanaged skill directory aside"}
	}

	return console.Step{Text: "Revert or move aside the conflicting files; existing files are never overwritten"}
}

// loadTargets loads and inspects every managed skill under parent, a directory
// of the project at base or elsewhere.
func loadTargets(ctx context.Context, parent, base string, source fs.FS, verbose bool) ([]*target, error) {
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
			name: name, folder: folder, shown: shownPath(base, folder), bundle: bundle, installed: installed,
			entry: skillEntry(name, folder, bundle, installed, verbose),
		})
	}

	return targets, nil
}

// planTargets builds every skill's write plan for the operation and returns the
// conflicts of all plans, each prefixed with its skill name except the first
// skill's. Each plan keeps its own conflicts for the dry-run report.
func planTargets(ctx context.Context, operation string, targets []*target) ([]string, error) {
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
// planned file change. A planned path that is new is a create, one that
// replaces an installed version is a write, and an obsolete one is a remove.
func result(targets []*target, verbose bool, summary string, dryRun bool) Result {
	report := Result{Summary: summary, DryRun: dryRun, Skills: make([]Entry, 0, len(targets))}

	for _, current := range targets {
		report.Skills = append(report.Skills, current.entry)

		if !verbose {
			continue
		}

		for _, change := range current.plan.Changes {
			action := console.ActionWrite

			switch {
			case change.remove:
				action = console.ActionRemove
			case change.previous == "":
				action = console.ActionCreate
			}

			report.Changes = append(report.Changes, console.Change{Action: action, Path: path.Join(current.shown, filepath.ToSlash(change.Path))})
		}
	}

	return report
}

// changeSummary words an install or update: "Installed 2 skills (14 files)" or,
// for a dry run, "Would update 2 skills (3 files)". When no file would change
// it says the skills are already up to date.
func changeSummary(options Options, targets []*target) string {
	files := 0

	for _, current := range targets {
		files += len(current.plan.Changes)
	}

	if files == 0 {
		return "Skills are already up to date"
	}

	noun := "skills"

	if len(targets) == 1 {
		noun = "skill"
	}

	verb := map[string]string{"install": "Installed", "update": "Updated"}[options.Operation]

	if options.DryRun {
		verb = "Would " + options.Operation
	}

	return fmt.Sprintf("%s %d %s (%d files)", verb, len(targets), noun, files)
}

// statusSummary answers the status question in one line: the first skill's
// absence or foreign directory wins, then missing, outdated and locally
// modified skills. The steps that fix them are the report's next_steps.
func statusSummary(targets []*target) string {
	first := targets[0].entry

	if first.State != "installed" {
		return first.Name + " skill is " + first.State
	}

	missing, outdated, modified := 0, 0, 0

	for _, current := range targets {
		if current.entry.State != "installed" {
			missing++

			continue
		}

		if current.entry.InstalledRevision != current.entry.BundledRevision {
			outdated++
		}

		if current.entry.ModifiedFileCount > 0 {
			modified++
		}
	}

	switch {
	case missing > 0:
		return fmt.Sprintf("%d of %d skills are not installed", missing, len(targets))
	case outdated > 0:
		return fmt.Sprintf("%d of %d skills are out of date", outdated, len(targets))
	case modified > 0:
		return fmt.Sprintf("%d of %d skills have locally modified files", modified, len(targets))
	}

	return "Skills are up to date"
}

// resolveSkillsDir returns the skills parent, anchoring a relative one to the
// supplied working directory, and that working directory with its aliases
// resolved; each skill lives in its own child directory. Only the working
// directory's system aliases are resolved; chosen links fail.
func resolveSkillsDir(dir, parent string) (string, string, error) {
	if parent == "" {
		return "", "", console.UsageError("invalid value for flag --dir: the skills directory must not be empty")
	}

	base, err := filepath.EvalSymlinks(dir)

	if err != nil {
		return "", "", err
	}

	base, err = filepath.Abs(base)

	if err != nil {
		return "", "", err
	}

	if !filepath.IsAbs(parent) {
		parent = filepath.Join(base, parent)
	}

	return parent, base, nil
}

// shownPath is how a result names a directory: relative to the project
// directory base when it lies inside it, and absolute otherwise.
func shownPath(base, directory string) string {
	relative, err := filepath.Rel(base, directory)

	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return directory
	}

	return filepath.ToSlash(relative)
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
