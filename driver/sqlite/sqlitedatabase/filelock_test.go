package sqlitedatabase_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
)

// lockHelperEnv names the variable that turns the test binary into a lock
// holder: its value is the database file to lock.
const lockHelperEnv = "ARKOREN_SQLITE_LOCK_HELPER"

// newFileLockDatabase opens a separate pool on the SQLite file at path.
func newFileLockDatabase(t *testing.T, path string) *database.Database {
	t.Helper()

	driver, err := sqlite.New("file:" + path + "?_pragma=busy_timeout(5000)")

	if err != nil {
		t.Fatalf("new SQLite driver: %v", err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Errorf("close SQLite driver: %v", err)
		}
	})

	return database.New(driver)
}

// skipWithoutFileLock skips tests that need a cross-process lock.
func skipWithoutFileLock(t *testing.T) {
	t.Helper()

	switch runtime.GOOS {
	case "darwin", "linux", "freebsd", "netbsd", "openbsd", "dragonfly":
	default:
		t.Skip("cross-process SQLite locks need flock")
	}
}

// TestSQLiteFileLockBlocksSecondPool verifies two pools on one file exclude each other.
func TestSQLiteFileLockBlocksSecondPool(t *testing.T) {
	skipWithoutFileLock(t)
	t.Parallel()

	path := filepath.Join(t.TempDir(), "app.db")
	first := newFileLockDatabase(t, path)
	second := newFileLockDatabase(t, path)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	unlock, err := first.Lock(ctx, name)

	if err != nil {
		t.Fatalf("first Lock() error = %v", err)
	}

	waitContext, cancel := context.WithTimeout(ctx, lockTestWait)
	t.Cleanup(cancel)

	if _, err := second.Lock(waitContext, name); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock() error = %v, want %v", err, context.DeadlineExceeded)
	}

	if err := unlock(ctx); err != nil {
		t.Fatalf("unlock error = %v", err)
	}
}

// TestSQLiteFileLockReleasedByUnlock verifies the second pool acquires after unlock.
func TestSQLiteFileLockReleasedByUnlock(t *testing.T) {
	skipWithoutFileLock(t)
	t.Parallel()

	path := filepath.Join(t.TempDir(), "app.db")
	first := newFileLockDatabase(t, path)
	second := newFileLockDatabase(t, path)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	unlock, err := first.Lock(ctx, name)

	if err != nil {
		t.Fatalf("first Lock() error = %v", err)
	}

	acquired := make(chan error, 1)

	go func() {
		release, err := second.Lock(ctx, name)

		if err == nil {
			err = release(ctx)
		}

		acquired <- err
	}()

	select {
	case err := <-acquired:
		t.Fatalf("second Lock() returned before unlock: %v", err)
	case <-time.After(lockTestWait):
	}

	if err := unlock(ctx); err != nil {
		t.Fatalf("unlock error = %v", err)
	}

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second Lock() error = %v", err)
		}
	case <-time.After(sqliteTestTimeout):
		t.Fatal("second Lock() did not return after unlock")
	}
}

// TestSQLiteFileLockAllowsDifferentNames verifies unrelated names do not block.
func TestSQLiteFileLockAllowsDifferentNames(t *testing.T) {
	skipWithoutFileLock(t)
	t.Parallel()

	path := filepath.Join(t.TempDir(), "app.db")
	first := newFileLockDatabase(t, path)
	second := newFileLockDatabase(t, path)
	ctx := sqliteTestContext(t)

	unlock, err := first.Lock(ctx, "name-a")

	if err != nil {
		t.Fatalf("first Lock() error = %v", err)
	}

	t.Cleanup(func() {
		if err := unlock(ctx); err != nil {
			t.Errorf("unlock error = %v", err)
		}
	})

	waitContext, cancel := context.WithTimeout(ctx, sqliteTestTimeout)
	t.Cleanup(cancel)

	other, err := second.Lock(waitContext, "name-b")

	if err != nil {
		t.Fatalf("second Lock() error = %v", err)
	}

	if err := other(ctx); err != nil {
		t.Fatalf("second unlock error = %v", err)
	}
}

// TestSQLiteFileLockCreatesSiblingLockFile verifies the lock lives next to the database file.
func TestSQLiteFileLockCreatesSiblingLockFile(t *testing.T) {
	skipWithoutFileLock(t)
	t.Parallel()

	dir := t.TempDir()
	databaseClient := newFileLockDatabase(t, filepath.Join(dir, "app.db"))
	ctx := sqliteTestContext(t)

	unlock, err := databaseClient.Lock(ctx, lockTestName(t))

	if err != nil {
		t.Fatalf("Lock() error = %v", err)
	}

	if err := unlock(ctx); err != nil {
		t.Fatalf("unlock error = %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "app.db.*.lock"))

	if err != nil {
		t.Fatal(err)
	}

	if len(matches) != 1 {
		t.Fatalf("lock files = %v, want exactly one", matches)
	}
}

// TestSQLiteMemoryURILockCreatesNoLockFile verifies shared in-memory databases skip the file lock.
func TestSQLiteMemoryURILockCreatesNoLockFile(t *testing.T) {
	t.Parallel()

	driver, err := sqlite.New("file:memorylock?mode=memory&cache=shared")

	if err != nil {
		t.Fatalf("new SQLite driver: %v", err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Errorf("close SQLite driver: %v", err)
		}
	})

	ctx := sqliteTestContext(t)

	unlock, err := database.New(driver).Lock(ctx, lockTestName(t))

	if err != nil {
		t.Fatalf("Lock() error = %v", err)
	}

	if err := unlock(ctx); err != nil {
		t.Fatalf("unlock error = %v", err)
	}

	matches, err := filepath.Glob("*memorylock*")

	if err != nil {
		t.Fatal(err)
	}

	if len(matches) != 0 {
		t.Fatalf("unexpected files %v in the working directory", matches)
	}
}

// TestSQLiteLockHelperProcess holds a lock for the parent test. It does
// nothing unless the parent sets the helper variable.
func TestSQLiteLockHelperProcess(t *testing.T) {
	path := os.Getenv(lockHelperEnv)

	if path == "" {
		t.Skip("helper process only")
	}

	databaseClient := newFileLockDatabase(t, path)
	ctx := sqliteTestContext(t)

	unlock, err := databaseClient.Lock(ctx, "subprocess-lock")

	if err != nil {
		t.Fatalf("Lock() error = %v", err)
	}

	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatal(err)
	}

	// The parent closes stdin to release the lock.
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}

	if err := unlock(ctx); err != nil {
		t.Fatalf("unlock error = %v", err)
	}
}

// TestSQLiteFileLockBlocksAcrossProcesses verifies another process holding the lock blocks this one.
func TestSQLiteFileLockBlocksAcrossProcesses(t *testing.T) {
	skipWithoutFileLock(t)
	t.Parallel()

	path := filepath.Join(t.TempDir(), "app.db")
	ctx := sqliteTestContext(t)

	command := exec.Command(os.Args[0], "-test.run=^TestSQLiteLockHelperProcess$")
	command.Env = append(os.Environ(), lockHelperEnv+"="+path)

	stdin, err := command.StdinPipe()

	if err != nil {
		t.Fatal(err)
	}

	stdout, err := command.StdoutPipe()

	if err != nil {
		t.Fatal(err)
	}

	if err := command.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	t.Cleanup(func() {
		if err := command.Wait(); err != nil {
			t.Errorf("helper exit: %v", err)
		}
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')

	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper output = %q, error = %v, want locked", line, err)
	}

	databaseClient := newFileLockDatabase(t, path)

	waitContext, cancel := context.WithTimeout(ctx, 3*lockTestWait)
	t.Cleanup(cancel)

	if _, err := databaseClient.Lock(waitContext, "subprocess-lock"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock() error = %v, want %v while the helper holds the lock", err, context.DeadlineExceeded)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}

	unlock, err := databaseClient.Lock(ctx, "subprocess-lock")

	if err != nil {
		t.Fatalf("Lock() error = %v after the helper released it", err)
	}

	if err := unlock(ctx); err != nil {
		t.Fatalf("unlock error = %v", err)
	}
}
