package files

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// TestCertificateShortFileWrite detects a writer that reports partial success.
func TestCertificateShortFileWrite(t *testing.T) {
	t.Parallel()

	err := Finish(certificateWriteFailure{written: 2}, []byte("PEM"))

	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short file write = %v", err)
	}
}

// certificateWriteFailure provides deterministic write/sync failures at the
// persistence boundary. syncErr is returned only after a complete write.
type certificateWriteFailure struct {
	// written is the byte count returned by Write, allowing short-write injection.
	written int

	// writeErr models a failed write independently of a short byte count.
	writeErr error

	// syncErr models a durability failure after all bytes have been accepted.
	syncErr error
}

// Write returns the configured result without retaining any supplied bytes.
func (failure certificateWriteFailure) Write([]byte) (int, error) {
	return failure.written, failure.writeErr
}

// Sync returns the configured durability failure without touching a filesystem.
func (failure certificateWriteFailure) Sync() error {
	return failure.syncErr
}

// TestCertificateFileWriteFailure preserves the actual write error even if a
// partial byte count is also returned, rather than masking it as a short write.
func TestCertificateFileWriteFailure(t *testing.T) {
	t.Parallel()

	err := Finish(certificateWriteFailure{writeErr: io.ErrClosedPipe}, []byte("PEM"))

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("file write error = %v", err)
	}
}

// TestCertificateFileSyncFailure propagates a failed durability check rather
// than reporting creation success merely because Write accepted all bytes.
func TestCertificateFileSyncFailure(t *testing.T) {
	t.Parallel()

	err := Finish(certificateWriteFailure{written: 3, syncErr: io.ErrClosedPipe}, []byte("PEM"))

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("file sync error = %v", err)
	}
}

// TestNewExclusiveCreate catches a file introduced after the preflight phase.
// The low-level writer must enforce no-overwrite independently of planning.
func TestNewExclusiveCreate(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	path := filepath.Join(env.Dir, "go.mod")

	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(t.Context(), path, []byte("replacement")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("error = %v, want existence collision", err)
	}
}

// scaffoldEnvironment isolates the filesystem and streams for offline tests.
// Env panics deliberately: scaffolding must never read inspector settings.
func scaffoldEnvironment(t *testing.T) console.Environment {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())

	if err != nil {
		t.Fatal(err)
	}

	return console.Environment{
		Dir:    dir,
		Stdin:  strings.NewReader(""),
		Stdout: new(bytes.Buffer),
		Stderr: new(bytes.Buffer),
		Env: func(string) (string, bool) {
			panic("new read process environment")
		},
	}
}
