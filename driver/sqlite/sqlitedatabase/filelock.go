package sqlitedatabase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"arkoren.dev/core/service/database"
	"arkoren.dev/driver/sqlite/sqlitedatabase/internal/filelock"
)

// lock takes the named lock. With wait it blocks until the lock is held or
// ctx ends; without it, it returns acquired false at once when another holder
// has the lock. The in-process lock is taken first, then the file lock; a file
// lock that is refused releases the in-process lock again.
func (driver *Driver) lock(ctx context.Context, name string, wait bool) (unlock func(context.Context) error, acquired bool, err error) {
	if name == "" {
		return nil, false, database.ErrEmptyLockName
	}

	unlockLocal, acquired, err := lockLocal(ctx, name, wait)

	if err != nil || !acquired {
		return nil, false, err
	}

	file, err := driver.databaseFile(ctx)

	if err != nil {
		return nil, false, errors.Join(err, unlockLocal(ctx))
	}

	if file == "" {
		return unlockLocal, true, nil
	}

	// One lock file per name: a single shared file would make a process that
	// holds one name deadlock itself when it takes another.
	sum := sha256.Sum256([]byte(name))
	path := file + "." + hex.EncodeToString(sum[:8]) + ".lock"

	unlockFile, acquired, err := filelock.Acquire(ctx, path, wait)

	if err != nil {
		return nil, false, errors.Join(fmt.Errorf("acquire lock %q: %w", name, err), unlockLocal(ctx))
	}

	if !acquired {
		return nil, false, unlockLocal(ctx)
	}

	if unlockFile == nil {
		return unlockLocal, true, nil
	}

	return onceUnlock(func(ctx context.Context) error {
		return errors.Join(unlockFile(), unlockLocal(ctx))
	}), true, nil
}

// databaseListRow is one row of SQLite's PRAGMA database_list.
type databaseListRow struct {
	// Seq is the schema position.
	Seq int `db:"seq"`

	// Name is the schema name, such as "main".
	Name string `db:"name"`

	// File is the database file path, empty for in-memory and temporary
	// databases.
	File string `db:"file"`
}

// databaseFile returns the file behind the main SQLite schema, or an empty
// string for an in-memory or temporary database. Asking SQLite avoids parsing
// the DSN and works for pools the driver did not open.
func (driver *Driver) databaseFile(ctx context.Context) (string, error) {
	var rows []databaseListRow

	if err := driver.Select(ctx, "PRAGMA database_list", &rows); err != nil {
		return "", fmt.Errorf("resolve database file: %w", err)
	}

	for _, row := range rows {
		if row.Name == "main" {
			return row.File, nil
		}
	}

	return "", nil
}
