package skill

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// readSkillFile opens only real directories and regular files within the target.
// Descriptor identity checks also reject swapped files during inspection.
func readSkillFile(ctx context.Context, folder, path string) ([]byte, error) {
	absolute := filepath.Join(folder, filepath.FromSlash(path))
	root, err := files.OpenDirectory(ctx, filepath.Dir(absolute), false)

	if err != nil {
		return nil, err
	}

	defer root.Close()
	name := filepath.Base(absolute)

	info, err := root.Lstat(name)

	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() {
		return nil, console.UsageError("refusing symlink or nonregular skill file %s", path)
	}

	file, err := root.Open(name)

	if err != nil {
		return nil, err
	}

	defer file.Close()

	opened, err := file.Stat()

	if err != nil {
		return nil, err
	}

	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("skill file changed during inspection: %s", path)
	}

	return io.ReadAll(file)
}

// Write rechecks the complete plan before mutation and each affected path
// immediately before writing. Each replacement is atomic; the whole update is
// not transactional. The last manifest write records a completed installation.
func Write(ctx context.Context, folder string, plan Plan) error {
	if len(plan.Conflicts) != 0 {
		return console.UsageError("cannot write a conflicting skill plan")
	}

	for _, path := range slices.Sorted(maps.Keys(plan.guards)) {
		if err := checkSkillHash(ctx, folder, path, plan.guards[path]); err != nil {
			return fmt.Errorf("skill changed before writing %s; no files were written: %w", path, err)
		}
	}

	for _, change := range plan.Changes {
		if err := ApplySkillChange(ctx, folder, change); err != nil {
			return fmt.Errorf("write skill %s (partial update may remain; inspect before retrying): %w", change.Path, err)
		}
	}

	return nil
}

// ApplySkillChange never replaces an unowned path or deletes a directory.
func ApplySkillChange(ctx context.Context, folder string, change Change) error {
	if err := checkSkillHash(ctx, folder, change.Path, change.previous); err != nil {
		return err
	}

	absolute := filepath.Join(folder, filepath.FromSlash(change.Path))

	if change.previous == "" {
		return files.WriteFile(ctx, absolute, change.Content)
	}

	root, err := files.OpenDirectory(ctx, filepath.Dir(absolute), false)

	if err != nil {
		return err
	}

	defer root.Close()

	if change.remove {
		return root.Remove(filepath.Base(absolute))
	}

	return replaceSkillFile(ctx, root, filepath.Base(absolute), change.Content)
}

// replaceSkillFile writes and closes a sibling temporary file before renaming.
// Failure never truncates the original; cleanup removes only this private file.
func replaceSkillFile(ctx context.Context, root *os.Root, name string, content []byte) (err error) {
	temporary := ".arkoren-skill-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)

	if err != nil {
		return err
	}

	defer func() {
		cleanup := root.Remove(temporary)

		if !errors.Is(cleanup, os.ErrNotExist) {
			err = errors.Join(err, cleanup)
		}
	}()

	_, err = file.Write(content)
	err = errors.Join(err, file.Close())

	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return root.Rename(temporary, name)
}
