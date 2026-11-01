package skill

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// Installation describes ownership and deviations from installed hashes.
type Installation struct {
	// Manifest is nil when the directory has no valid ownership record.
	Manifest *Manifest
	// record preserves the original manifest bytes for pre-write validation.
	record []byte
	// Modified lists missing, edited, or unsafe managed files in lexical order.
	Modified []string
	// unmanaged reports foreign files in a directory without an ownership record.
	unmanaged bool
}

// Change is one exclusive creation, checked replacement, or checked removal.
type Change struct {
	// File supplies the relative path and desired bytes.
	files.File
	// previous is the expected old hash; empty means the path must be absent.
	previous string
	// remove marks an obsolete managed file, never a directory or unrelated file.
	remove bool
}

// Plan separates read-only inspection from filesystem mutation.
type Plan struct {
	// Changes are deterministic, with the manifest committed last.
	Changes []Change
	// Conflicts block every write, not only the conflicting action.
	Conflicts []string
	// guards retain all inspected managed hashes, including unchanged files.
	guards map[string]string
}

// Inspect validates ownership before checking installed file hashes.
func Inspect(ctx context.Context, folder string) (Installation, error) {
	content, err := readSkillFile(ctx, folder, SkillManifestName)

	if errors.Is(err, os.ErrNotExist) {
		unmanaged, err := skillDirectoryUnmanaged(ctx, folder)

		return Installation{unmanaged: unmanaged}, err
	}

	if err != nil {
		return Installation{}, err
	}

	var manifest Manifest

	if err := json.Unmarshal(content, &manifest); err != nil {
		return Installation{}, console.UsageError("read skill manifest: %v", err)
	}

	if err := ValidateSkillManifest(manifest); err != nil {
		return Installation{}, err
	}

	installed := Installation{Manifest: &manifest, record: content}

	for _, path := range slices.Sorted(maps.Keys(manifest.Files)) {
		if err := ctx.Err(); err != nil {
			return Installation{}, err
		}

		if err := checkSkillHash(ctx, folder, path, manifest.Files[path]); err != nil {
			if err := ctx.Err(); err != nil {
				return Installation{}, err
			}

			installed.Modified = append(installed.Modified, path)
		}
	}

	return installed, nil
}

// NewPlan checks ownership, local edits, and new-path collisions before writes.
func NewPlan(
	ctx context.Context,
	folder, operation string,
	bundle Bundle,
	installed Installation,
) (Plan, error) {
	plan := Plan{guards: make(map[string]string)}

	if installed.Manifest == nil {
		if installed.unmanaged {
			plan.Conflicts = []string{"unmanaged skill directory; move it aside before installing"}
		} else if operation == "update" {
			plan.Conflicts = []string{"no managed installation; run arkoren skills install"}
		}
	} else {
		plan.guards = maps.Clone(installed.Manifest.Files)
		plan.guards[SkillManifestName] = Hash(installed.record)
		plan.Conflicts = slices.Clone(installed.Modified)

		if operation == "install" && installed.Manifest.Revision != bundle.Manifest.Revision {
			plan.Conflicts = append(plan.Conflicts, "another revision is installed; run arkoren skills update")
		}
	}

	if len(plan.Conflicts) != 0 {
		return plan, nil
	}

	inventory, err := Files(bundle)

	if err != nil {
		return plan, err
	}

	for _, file := range inventory {
		previous := plan.guards[file.Path]

		if previous == Hash(file.Content) {
			continue
		}

		if previous == "" {
			if err := checkSkillHash(ctx, folder, file.Path, ""); err != nil {
				plan.Conflicts = append(plan.Conflicts, file.Path+": "+err.Error())
			}

			plan.guards[file.Path] = ""
		}

		plan.Changes = append(plan.Changes, Change{File: file, previous: previous})
	}

	if installed.Manifest != nil {
		plan = planObsoleteSkillFiles(plan, bundle, *installed.Manifest)
	}

	return plan, nil
}

// planObsoleteSkillFiles schedules only owned obsolete paths before the manifest.
func planObsoleteSkillFiles(plan Plan, bundle Bundle, installed Manifest) Plan {
	var obsolete []Change

	for _, path := range slices.Sorted(maps.Keys(installed.Files)) {
		if _, exists := bundle.Manifest.Files[path]; !exists {
			obsolete = append(obsolete, Change{
				File: files.File{Path: path}, previous: installed.Files[path], remove: true,
			})
		}
	}

	// skillFiles appends the manifest last; obsolete removals must precede it.
	if len(obsolete) != 0 {
		last := len(plan.Changes) - 1
		plan.Changes = slices.Insert(plan.Changes, last, obsolete...)
	}

	return plan
}

// checkSkillHash also rejects links and nonregular files; absence is explicit.
func checkSkillHash(ctx context.Context, folder, path, expected string) error {
	content, err := readSkillFile(ctx, folder, path)

	if errors.Is(err, os.ErrNotExist) && expected == "" {
		return nil
	}

	if err != nil {
		return err
	}

	if expected == "" {
		return fmt.Errorf("path already exists")
	}

	if Hash(content) != expected {
		return fmt.Errorf("file differs from the installed hash")
	}

	return nil
}
