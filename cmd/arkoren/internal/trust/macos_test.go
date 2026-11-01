//go:build !windows

package trust

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// checkStagedTrustRoot verifies the public snapshot passed to a fake native
// utility. Both install and removal must use byte-identical canonical material.
func checkStagedTrustRoot(t *testing.T, path string, expected []byte) {
	t.Helper()

	actual, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(actual, expected) {
		t.Fatal("native utility received different certificate material")
	}
}

// TestCertificateTrustMacInstallArgv verifies system/admin SSL scope and that
// the command receives canonical staged PEM, never the user-controlled path.
func TestCertificateTrustMacInstallArgv(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, nil)
	root, err := Load(path, "trust")

	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	staged := ""
	backend := &macCertificateTrust{
		temporaryDirectory: t.TempDir(),
		elevated: func() bool {
			return true
		},
		command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls++

			if name != "/usr/bin/security" {
				t.Fatalf("unexpected utility %q", name)
			}

			if calls != 2 {
				if !slices.Equal(args, []string{"find-certificate", "-a", "-p", macSystemKeychain}) {
					t.Fatalf("inventory argv = %q", args)
				}

				return root.pem, nil
			}

			if !slices.Equal(args[:len(args)-1],
				[]string{"add-trusted-cert", "-d", "-r", "trustRoot", "-p", "ssl", "-k", macSystemKeychain}) {
				t.Fatalf("install argv = %q", args)
			}

			staged = args[len(args)-1]
			checkStagedTrustRoot(t, staged, root.pem)

			if staged == path {
				t.Fatal("native utility received user-controlled path")
			}

			return nil, nil
		},
	}

	if err := backend.Apply(context.Background(), root, "trust"); err != nil {
		t.Fatal(err)
	}

	if calls != 3 {
		t.Fatalf("expected before/add/after calls, got %d", calls)
	}

	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file not cleaned up: %v", err)
	}
}

// TestCertificateTrustMacRemoveArgv ensures removal consumes exact staged DER
// and never issues delete-certificate or deletes by subject/serial/hash.
func TestCertificateTrustMacRemoveArgv(t *testing.T) {
	t.Parallel()

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	backend := &macCertificateTrust{
		temporaryDirectory: t.TempDir(),
		elevated: func() bool {
			return true
		},
		command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			calls++

			if calls == 1 {
				return root.pem, nil
			}

			if len(args) != 3 || !slices.Equal(args[:2], []string{"remove-trusted-cert", "-d"}) {
				t.Fatalf("remove argv = %q", args)
			}

			checkStagedTrustRoot(t, args[2], root.pem)

			return nil, nil
		},
	}

	if err := backend.Apply(context.Background(), root, "untrust"); err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Fatalf("native calls = %d", calls)
	}
}

// TestCertificateTrustMacRejectsUnelevatedUse prevents security from opening
// an administrator authentication dialog even when --yes was supplied.
func TestCertificateTrustMacRejectsUnelevatedUse(t *testing.T) {
	t.Parallel()

	backend := &macCertificateTrust{
		elevated: func() bool {
			return false
		},
		command: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("native utility must not run without existing elevation")

			return nil, nil
		},
	}

	err := backend.Apply(context.Background(), Certificate{}, "trust")

	if err == nil || !strings.Contains(err.Error(), "rerun this command elevated") {
		t.Fatalf("expected elevation guidance, got %v", err)
	}
}

// TestCertificateTrustMacRemovalIsIdempotent accepts only the native missing
// trust-settings diagnostic from exact-certificate removal.
func TestCertificateTrustMacRemovalIsIdempotent(t *testing.T) {
	t.Parallel()

	backend := &macCertificateTrust{
		command: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("security: SecTrustSettingsRemoveTrustSettings: No Trust Settings were found.\n"),
				errors.New("exit status 1")
		},
	}

	if err := backend.change(context.Background(), "/fake/staged.pem", "untrust"); err != nil {
		t.Fatal(err)
	}
}

// TestCertificateTrustMacPermissionFailurePreservesCause makes native access
// errors actionable without retrying with sudo or swallowing the failure.
func TestCertificateTrustMacPermissionFailurePreservesCause(t *testing.T) {
	t.Parallel()

	backend := &macCertificateTrust{
		command: func(context.Context, string, ...string) ([]byte, error) {
			return nil, os.ErrPermission
		},
	}

	err := backend.change(context.Background(), "/fake/staged.pem", "untrust")

	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "rerun elevated") {
		t.Fatalf("permission failure = %v", err)
	}
}

// TestCertificateTrustMacUnknownRemovalFailureIsNotAbsence refuses to convert
// arbitrary or localized utility failures into a successful removal.
func TestCertificateTrustMacUnknownRemovalFailureIsNotAbsence(t *testing.T) {
	t.Parallel()

	failure := errors.New("unrecognized native error")
	backend := &macCertificateTrust{
		command: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("unknown trust-store failure"), failure
		},
	}

	if err := backend.change(context.Background(), "/fake/staged.pem", "untrust"); !errors.Is(err, failure) {
		t.Fatalf("native failure was hidden: %v", err)
	}
}

// TestCertificateTrustMacCancellationPreservesCause does not convert a canceled
// removal into success even if the child also printed an absence diagnostic.
func TestCertificateTrustMacCancellationPreservesCause(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend := &macCertificateTrust{
		command: func(context.Context, string, ...string) ([]byte, error) {
			cancel()

			return []byte("security: SecTrustSettingsRemoveTrustSettings: No Trust Settings were found."),
				context.Canceled
		},
	}

	if err := backend.change(ctx, "/fake/staged.pem", "untrust"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

// TestCertificateTrustInventoryUsesFullDER distinguishes different certificates
// with the same subject and serial, rather than conflating their identities.
func TestCertificateTrustInventoryUsesFullDER(t *testing.T) {
	t.Parallel()

	first, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	second, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	found, err := containsTrustCertificate(first.pem, second.DER)

	if err != nil || found {
		t.Fatalf("different DER matched: found=%t, err=%v", found, err)
	}

	found, err = containsTrustCertificate(append(first.pem, second.pem...), second.DER)

	if err != nil || !found {
		t.Fatalf("exact DER did not match: found=%t, err=%v", found, err)
	}
}

// TestCertificateTrustInventoryRejectsMalformedPEM fails closed on invalid output.
func TestCertificateTrustInventoryRejectsMalformedPEM(t *testing.T) {
	t.Parallel()

	if _, err := containsTrustCertificate([]byte("not PEM"), nil); err == nil {
		t.Fatal("malformed inventory accepted")
	}
}

// TestTrustCleanupFailureReportsCompletedNativeChange uses a fake native command
// and real temporary-directory permissions. A completed isolated removal is not
// undone when deleting its public staging snapshot subsequently fails.
func TestTrustCleanupFailureReportsCompletedNativeChange(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions enforced for a non-root owner")
	}

	parent := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	state := filepath.Join(t.TempDir(), "trust-state")
	root, err := Load(trustTestCertificate(t, nil), "untrust")

	if err != nil {
		t.Fatal(err)
	}

	backend := &macCertificateTrust{
		temporaryDirectory: parent,
		elevated: func() bool {
			return true // Only the injected command can run; no OS store is used.
		},
		command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "find-certificate" {
				return root.pem, nil
			}

			if err := os.WriteFile(state, []byte("untrusted"), 0o600); err != nil {
				return nil, err
			}

			return nil, os.Chmod(parent, 0o500)
		},
	}

	err = backend.Apply(t.Context(), root, "untrust")

	if err == nil || !strings.Contains(err.Error(), "native trust command completed") ||
		!strings.Contains(err.Error(), "staging cleanup failed") {
		t.Fatalf("completed mutation lost its outcome during cleanup: %v", err)
	}

	if data, err := os.ReadFile(state); err != nil || string(data) != "untrusted" {
		t.Fatalf("isolated native mutation did not complete: %q, %v", data, err)
	}
}

// TestTrustCleanupFailurePreservesPartialNativeChange retains both an isolated
// command failure and its earlier filesystem mutation. Cleanup must not turn
// that result into either successful completion or a claim that nothing changed.
func TestTrustCleanupFailurePreservesPartialNativeChange(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions enforced for a non-root owner")
	}

	parent := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	state := filepath.Join(t.TempDir(), "trust-state")
	root, err := Load(trustTestCertificate(t, nil), "untrust")

	if err != nil {
		t.Fatal(err)
	}

	failure := errors.New("isolated native change failed")
	backend := &macCertificateTrust{
		temporaryDirectory: parent,
		elevated: func() bool {
			return true
		},
		command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "find-certificate" {
				return root.pem, nil
			}

			if err := os.WriteFile(state, []byte("partially changed"), 0o600); err != nil {
				return nil, err
			}

			if err := os.Chmod(parent, 0o500); err != nil {
				return nil, err
			}

			return nil, failure
		},
	}

	err = backend.Apply(t.Context(), root, "untrust")

	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "native trust change may be partial") ||
		!strings.Contains(err.Error(), "staging cleanup failed") {
		t.Fatalf("partial native outcome or cause was lost: %v", err)
	}

	if data, err := os.ReadFile(state); err != nil || string(data) != "partially changed" {
		t.Fatalf("isolated partial mutation is absent: %q, %v", data, err)
	}
}
