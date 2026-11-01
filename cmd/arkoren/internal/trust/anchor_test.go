//go:build !windows

package trust

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// trustTestAnchorFile models persistence failures without writing to a native
// trust directory. Its call sequence verifies sync and close ordering.
type trustTestAnchorFile struct {
	// written is the Write result, including deliberately short writes.
	written int

	// writeErr, syncErr, and closeErr inject independently inspectable causes.
	writeErr, syncErr, closeErr error

	// calls records persistence operations in their actual order.
	calls []string
}

// Write records the write attempt and returns its configured result.
// For example, a fixture with written: 0 simulates a short successful write.
func (file *trustTestAnchorFile) Write([]byte) (int, error) {
	file.calls = append(file.calls, "write")

	return file.written, file.writeErr
}

// Sync records the durability attempt after a successful full write.
// For example, syncErr: io.ErrClosedPipe injects a deterministic sync failure.
func (file *trustTestAnchorFile) Sync() error {
	file.calls = append(file.calls, "sync")

	return file.syncErr
}

// Close records cleanup without removing any pathname.
// For example, closeErr: io.ErrClosedPipe verifies joined close-error causes.
func (file *trustTestAnchorFile) Close() error {
	file.calls = append(file.calls, "close")

	return file.closeErr
}

// trustTestAnchorReader observes the bounded read and supplies a close failure
// without depending on host filesystem behavior or native system-store handles.
type trustTestAnchorReader struct {
	// reader supplies deterministic content, possibly larger than the limit.
	reader io.Reader

	// read counts delivered bytes so tests can assert the exact read bound.
	read int

	// closeErr is returned after the content comparison completes.
	closeErr error

	// readErr injects a content read failure independently of Close.
	readErr error
}

// Read counts only the bytes actually delivered by the underlying reader.
// For example, matchLinuxTrustAnchor reads at most len(expected)+1 bytes.
func (file *trustTestAnchorReader) Read(data []byte) (int, error) {
	if file.readErr != nil {
		return 0, file.readErr
	}

	written, err := file.reader.Read(data)
	file.read += written

	return written, err
}

// Close returns the configured cleanup cause without touching real files.
// For example, closeErr: io.ErrClosedPipe keeps the close failure inspectable.
func (file *trustTestAnchorReader) Close() error {
	return file.closeErr
}

// linuxTrustTestBackend binds anchor I/O to a test directory and refresh to a
// fake executable file. The injected command must never execute that file.
func linuxTrustTestBackend(t *testing.T, command certificateTrustCommand) *linuxCertificateTrust {
	t.Helper()

	directory := t.TempDir()
	executable := filepath.Join(t.TempDir(), "refresh")

	if err := os.WriteFile(executable, []byte("not a runnable program"), 0o700); err != nil {
		t.Fatal(err)
	}

	return &linuxCertificateTrust{
		layouts: []linuxTrustLayout{{directory: directory, command: executable, extension: ".crt"}},
		command: command,
		open:    os.OpenRoot,
	}
}

// checkLinuxTrustAnchor checks the public bytes installed by either supported
// layout, independently of filename and native refresh invocation assertions.
func checkLinuxTrustAnchor(t *testing.T, path string, expected []byte) {
	t.Helper()

	actual, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(actual, expected) {
		t.Fatal("installed anchor differs from canonical certificate material")
	}
}

// TestCertificateTrustLinuxInstallAndIdempotency writes one exact canonical
// anchor and refreshes on repeated installs without replacing its inode.
func TestCertificateTrustLinuxInstallAndIdempotency(t *testing.T) {
	t.Parallel()

	calls := 0
	backend := linuxTrustTestBackend(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++

		if len(args) != 0 {
			t.Fatalf("unexpected Debian refresh args: %q", args)
		}

		return nil, nil
	})

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	if err := backend.Apply(context.Background(), root, "trust"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(backend.layouts[0].directory, "arkoren-"+root.Fingerprint+".crt")
	before, err := os.Stat(path)

	if err != nil {
		t.Fatal(err)
	}

	if err := backend.Apply(context.Background(), root, "trust"); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)

	if err != nil {
		t.Fatal(err)
	}

	checkLinuxTrustAnchor(t, path, root.pem)

	if calls != 2 || !os.SameFile(before, after) {
		t.Fatalf("repeated install changed identity or inode: calls=%d", calls)
	}
}

// TestCertificateTrustLinuxFedoraRefresh checks the exact extract argument and
// PEM extension without touching the host distribution's actual paths.
func TestCertificateTrustLinuxFedoraRefresh(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !slices.Equal(args, []string{"extract"}) {
			t.Fatalf("Fedora refresh args = %q", args)
		}

		return nil, nil
	})
	backend.layouts[0].extension = ".pem"
	backend.layouts[0].args = []string{"extract"}

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	if err := backend.Apply(context.Background(), root, "trust"); err != nil {
		t.Fatal(err)
	}

	checkLinuxTrustAnchor(t,
		filepath.Join(backend.layouts[0].directory, "arkoren-"+root.Fingerprint+".pem"), root.pem)
}

// TestCertificateTrustLinuxRemovalAndIdempotency removes only its owned anchor
// and refreshes again when absent, repairing earlier partial refresh failures.
func TestCertificateTrustLinuxRemovalAndIdempotency(t *testing.T) {
	t.Parallel()

	calls := 0
	backend := linuxTrustTestBackend(t, func(context.Context, string, ...string) ([]byte, error) {
		calls++

		return nil, nil
	})

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(backend.layouts[0].directory, "arkoren-"+root.Fingerprint+".crt")

	if err := os.WriteFile(path, root.pem, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := backend.Apply(context.Background(), root, "untrust"); err != nil {
		t.Fatal(err)
	}

	if err := backend.Apply(context.Background(), root, "untrust"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("anchor remains: %v", err)
	}

	if calls != 2 {
		t.Fatalf("refresh calls = %d", calls)
	}
}

// TestCertificateTrustLinuxRefusesTamperedAnchor protects a digest-named file
// whose contents were replaced; neither removal nor installation may overwrite it.
func TestCertificateTrustLinuxRefusesTamperedAnchor(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("refresh must not run for a tampered anchor")

		return nil, nil
	})

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(backend.layouts[0].directory, "arkoren-"+root.Fingerprint+".crt")

	if err := os.WriteFile(path, []byte("unrelated file"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"trust", "untrust"} {
		if err := backend.Apply(context.Background(), root, action); err == nil {
			t.Fatalf("%s accepted a tampered anchor", action)
		}
	}

	actual, err := os.ReadFile(path)

	if err != nil || string(actual) != "unrelated file" {
		t.Fatalf("tampered anchor was modified: %q, %v", actual, err)
	}
}

// TestCertificateTrustLinuxRefusesSymlink protects unrelated files even when
// the link points inside the otherwise trusted anchor directory.
func TestCertificateTrustLinuxRefusesSymlink(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("refresh must not run for a linked anchor")

		return nil, nil
	})

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	directory := backend.layouts[0].directory

	if err := os.WriteFile(filepath.Join(directory, "other.pem"), root.pem, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("other.pem", filepath.Join(directory, "arkoren-"+root.Fingerprint+".crt")); err != nil {
		t.Fatal(err)
	}

	if err := backend.Apply(context.Background(), root, "untrust"); err == nil {
		t.Fatal("symlink anchor accepted")
	}
}

// TestCertificateTrustLinuxRefreshFailureReportsPartialInstall preserves the
// installed anchor and the refresh error so rerunning can repair native state.
func TestCertificateTrustLinuxRefreshFailureReportsPartialInstall(t *testing.T) {
	t.Parallel()

	failure := errors.New("refresh failed")
	backend := linuxTrustTestBackend(t, func(context.Context, string, ...string) ([]byte, error) {
		return nil, failure
	})

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	err = backend.Apply(context.Background(), root, "trust")

	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "trust may be stale") {
		t.Fatalf("expected partial refresh error, got %v", err)
	}

	if !strings.Contains(err.Error(), backend.layouts[0].command) {
		t.Fatalf("partial-install error omitted the native refresh executable: %v", err)
	}

	if _, err := os.Stat(filepath.Join(backend.layouts[0].directory, "arkoren-"+root.Fingerprint+".crt")); err != nil {
		t.Fatalf("installed anchor missing: %v", err)
	}
}

// TestCertificateTrustLinuxMissingRuntimePreventsWrites fails preflight when
// the refresh utility is unavailable instead of leaving a new anchor behind.
func TestCertificateTrustLinuxMissingRuntimePreventsWrites(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, nil)

	if err := os.Remove(backend.layouts[0].command); err != nil {
		t.Fatal(err)
	}

	if err := backend.Apply(context.Background(), Certificate{}, "trust"); err == nil {
		t.Fatal("missing runtime accepted")
	}

	entries, err := os.ReadDir(backend.layouts[0].directory)

	if err != nil || len(entries) != 0 {
		t.Fatalf("preflight wrote anchors: %v, %v", entries, err)
	}
}

// TestCertificateTrustLinuxUnsupportedLayoutNeverCreatesDirectories rejects
// absent distribution paths instead of fabricating an arbitrary trust store.
func TestCertificateTrustLinuxUnsupportedLayoutNeverCreatesDirectories(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "absent")

	_, err := findLinuxTrustLayout([]linuxTrustLayout{{directory: path}})

	if err == nil || !strings.Contains(err.Error(), "unsupported Linux") {
		t.Fatalf("expected unsupported layout, got %v", err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported directory was created: %v", err)
	}
}

// TestCertificateTrustLinuxCancellationPreventsWrites checks the platform
// boundary independently from the shared CLI's cancellation checks.
func TestCertificateTrustLinuxCancellationPreventsWrites(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := backend.Apply(ctx, Certificate{}, "trust"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

// TestCertificateTrustLinuxPermissionFailurePreservesCause uses an injected
// directory opener so even elevated test processes exercise access denial safely.
func TestCertificateTrustLinuxPermissionFailurePreservesCause(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, nil)
	backend.open = func(string) (*os.Root, error) {
		return nil, os.ErrPermission
	}

	err := backend.Apply(context.Background(), Certificate{}, "trust")

	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "rerun elevated") {
		t.Fatalf("permission error = %v", err)
	}
}

// TestCertificateTrustLinuxExclusiveCreation refuses to replace a file created
// between inspection and open, including one with unrelated noncertificate data.
func TestCertificateTrustLinuxExclusiveCreation(t *testing.T) {
	t.Parallel()

	directory, err := os.OpenRoot(t.TempDir())

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := directory.Close(); err != nil {
			t.Error(err)
		}
	})

	if err := directory.WriteFile("root.crt", []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeLinuxTrustAnchor(directory, "root.crt", []byte("replacement")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected exclusive-create failure, got %v", err)
	}

	actual, err := directory.ReadFile("root.crt")

	if err != nil || string(actual) != "unrelated" {
		t.Fatalf("existing file changed: %q, %v", actual, err)
	}
}

// TestCertificateTrustLinuxRefreshFailureReportsPartialRemoval leaves the
// owned anchor absent and warns that the generated native trust may be stale.
func TestCertificateTrustLinuxRefreshFailureReportsPartialRemoval(t *testing.T) {
	t.Parallel()

	failure := errors.New("refresh denied")
	backend := linuxTrustTestBackend(t, func(context.Context, string, ...string) ([]byte, error) {
		return nil, failure
	})
	backend.layouts[0].args = []string{"extract"}

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(backend.layouts[0].directory, "arkoren-"+root.Fingerprint+".crt")

	if err := os.WriteFile(path, root.pem, 0o644); err != nil {
		t.Fatal(err)
	}

	err = backend.Apply(context.Background(), root, "untrust")

	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "trust may be stale") {
		t.Fatalf("expected partial-removal error, got %v", err)
	}

	if !strings.Contains(err.Error(), backend.layouts[0].command) || !strings.Contains(err.Error(), "extract") {
		t.Fatalf("partial-removal error omitted the native refresh command or arguments: %v", err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed anchor remains: %v", err)
	}
}

// TestCertificateTrustLinuxSelectsCompleteLayout ignores a leftover directory
// for another distribution when only the next supported layout has its utility.
func TestCertificateTrustLinuxSelectsCompleteLayout(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, nil)
	complete := backend.layouts[0]
	incomplete := linuxTrustLayout{
		directory: t.TempDir(),
		command:   filepath.Join(t.TempDir(), "missing-refresh"),
	}

	layout, err := findLinuxTrustLayout([]linuxTrustLayout{incomplete, complete})

	if err != nil {
		t.Fatal(err)
	}

	if layout.directory != complete.directory {
		t.Fatalf("selected incomplete layout: %+v", layout)
	}
}

// TestCertificateTrustLinuxTargetSelectionIsPinned prevents preflight from
// naming one distribution store and Apply later selecting another directory.
func TestCertificateTrustLinuxTargetSelectionIsPinned(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !slices.Equal(args, []string{"extract"}) {
			t.Fatalf("approved refresh arguments changed: %q", args)
		}

		return nil, nil
	})
	backend.layouts[0].args = []string{"extract"}
	selected := backend.layouts[0].directory

	target, err := backend.Target()

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(target, selected) {
		t.Fatalf("target does not identify selected directory: %q", target)
	}

	backend.layouts[0].directory = t.TempDir()
	backend.layouts[0].args[0] = "unapproved"

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	if err := backend.Apply(context.Background(), root, "trust"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(selected, "arkoren-"+root.Fingerprint+".crt")); err != nil {
		t.Fatalf("approved target was not used: %v", err)
	}

	entries, err := os.ReadDir(backend.layouts[0].directory)

	if err != nil || len(entries) != 0 {
		t.Fatalf("unapproved store was modified: %v, %v", entries, err)
	}
}

// TestCertificateTrustLinuxPreflightNeverOpensStore verifies that selecting a
// target does not acquire the privileged directory handle before approval.
func TestCertificateTrustLinuxPreflightNeverOpensStore(t *testing.T) {
	t.Parallel()

	backend := linuxTrustTestBackend(t, nil)
	backend.open = func(string) (*os.Root, error) {
		t.Fatal("native directory handle opened before approval")

		return nil, nil
	}
	var warning bytes.Buffer

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust",
	}, console.Environment{Stdout: io.Discard, Stderr: &warning}, backend)

	if err == nil {
		t.Fatal("noninteractive operation without --yes was approved")
	}

	if !strings.Contains(warning.String(), backend.layouts[0].directory) {
		t.Fatalf("warning omitted preflight target: %q", warning.String())
	}
}

// TestCertificateTrustLinuxExistingAnchorReadIsBounded rejects extra content
// after reading only the expected byte count plus one, not the entire file.
func TestCertificateTrustLinuxExistingAnchorReadIsBounded(t *testing.T) {
	t.Parallel()

	expected := []byte("canonical certificate")
	file := &trustTestAnchorReader{reader: bytes.NewReader(bytes.Repeat(expected, 1000))}

	if err := matchLinuxTrustAnchor(file, expected); err == nil {
		t.Fatal("oversized anchor accepted")
	}

	if file.read != len(expected)+1 {
		t.Fatalf("read %d bytes; wanted %d", file.read, len(expected)+1)
	}
}

// TestCertificateTrustLinuxExistingAnchorCloseFailurePreservesCause retains a
// close failure even when all canonical certificate bytes matched.
func TestCertificateTrustLinuxExistingAnchorCloseFailurePreservesCause(t *testing.T) {
	t.Parallel()

	expected := []byte("canonical certificate")
	failure := errors.New("close failed")
	file := &trustTestAnchorReader{reader: bytes.NewReader(expected), closeErr: failure}

	if err := matchLinuxTrustAnchor(file, expected); !errors.Is(err, failure) {
		t.Fatalf("close cause was lost: %v", err)
	}
}

// TestCertificateTrustLinuxExistingAnchorReadAndCloseFailuresAreJoined keeps
// both causes visible when a bounded read fails before comparison completes.
func TestCertificateTrustLinuxExistingAnchorReadAndCloseFailuresAreJoined(t *testing.T) {
	t.Parallel()

	readFailure := errors.New("read failed")
	closeFailure := errors.New("close failed")
	file := &trustTestAnchorReader{readErr: readFailure, closeErr: closeFailure}

	err := matchLinuxTrustAnchor(file, []byte("certificate"))

	if !errors.Is(err, readFailure) || !errors.Is(err, closeFailure) {
		t.Fatalf("read or close cause was lost: %v", err)
	}
}

// TestCertificateTrustLinuxSuccessfulAnchorWriteSyncsAndCloses checks that
// native refresh cannot follow a successful write before durability and close.
func TestCertificateTrustLinuxSuccessfulAnchorWriteSyncsAndCloses(t *testing.T) {
	t.Parallel()

	encoded := []byte("canonical certificate")
	file := &trustTestAnchorFile{written: len(encoded)}

	if err := persistLinuxTrustAnchor(file, "/isolated/root.crt", encoded); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(file.calls, []string{"write", "sync", "close"}) {
		t.Fatalf("persistence order = %q", file.calls)
	}
}

// TestCertificateTrustLinuxShortWriteReportsManualRecovery treats a nil-error
// short write as partial state, closes the file, and does not attempt sync.
func TestCertificateTrustLinuxShortWriteReportsManualRecovery(t *testing.T) {
	t.Parallel()

	file := &trustTestAnchorFile{written: 1}

	err := persistLinuxTrustAnchor(file, "/isolated/root.crt", []byte("certificate"))

	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write was not preserved: %v", err)
	}

	if !strings.Contains(err.Error(), "/isolated/root.crt") ||
		!strings.Contains(err.Error(), "incomplete and not deployed") {
		t.Fatalf("partial-state recovery guidance missing: %v", err)
	}

	if !slices.Equal(file.calls, []string{"write", "close"}) {
		t.Fatalf("short-write cleanup order = %q", file.calls)
	}
}

// TestCertificateTrustLinuxWriteAndCloseFailuresAreJoined retains independent
// write and close causes instead of dropping the cleanup failure.
func TestCertificateTrustLinuxWriteAndCloseFailuresAreJoined(t *testing.T) {
	t.Parallel()

	writeFailure := errors.New("write failed")
	closeFailure := errors.New("close failed")
	file := &trustTestAnchorFile{writeErr: writeFailure, closeErr: closeFailure}

	err := persistLinuxTrustAnchor(file, "/isolated/root.crt", []byte("certificate"))

	if !errors.Is(err, writeFailure) || !errors.Is(err, closeFailure) {
		t.Fatalf("persistence causes were lost: %v", err)
	}
}

// TestCertificateTrustLinuxSyncAndCloseFailuresAreJoined reports uncertain
// durability and close failures after all certificate bytes were written.
func TestCertificateTrustLinuxSyncAndCloseFailuresAreJoined(t *testing.T) {
	t.Parallel()

	encoded := []byte("canonical certificate")
	syncFailure := errors.New("sync failed")
	closeFailure := errors.New("close failed")
	file := &trustTestAnchorFile{written: len(encoded), syncErr: syncFailure, closeErr: closeFailure}

	err := persistLinuxTrustAnchor(file, "/isolated/root.crt", encoded)

	if !errors.Is(err, syncFailure) || !errors.Is(err, closeFailure) {
		t.Fatalf("persistence causes were lost: %v", err)
	}

	if !strings.Contains(err.Error(), "refresh was not attempted") {
		t.Fatalf("partial-state error omitted refresh state: %v", err)
	}
}

// TestCertificateTrustLinuxFailedPersistenceNeverDeletesReplacement checks an
// uncertain pathname independently of the failing file handle, as happens if
// another process replaces the newly created anchor before its write fails.
func TestCertificateTrustLinuxFailedPersistenceNeverDeletesReplacement(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "root.crt")

	if err := os.WriteFile(path, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}

	failure := errors.New("old handle write failed")
	file := &trustTestAnchorFile{writeErr: failure}

	if err := persistLinuxTrustAnchor(file, path, []byte("certificate")); !errors.Is(err, failure) {
		t.Fatalf("write failure was lost: %v", err)
	}

	actual, err := os.ReadFile(path)

	if err != nil || string(actual) != "replacement" {
		t.Fatalf("uncertain replacement pathname was modified: %q, %v", actual, err)
	}
}
