//go:build windows

package trust

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// windowsTrustTestStore creates only a process-local memory store. These tests
// never open, add to, or remove from any Windows system certificate store.
func windowsTrustTestStore(t *testing.T) syscall.Handle {
	t.Helper()

	store, err := syscall.CertOpenStore(syscall.CERT_STORE_PROV_MEMORY, 0, 0, 0, 0)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := syscall.CertCloseStore(store, 0); err != nil {
			t.Error(err)
		}
	})

	return store
}

// TestCertificateTrustWindowsMemoryInstallIsIdempotent exercises real Crypt32
// add-new and exact enumeration against an ephemeral, non-system store.
func TestCertificateTrustWindowsMemoryInstallIsIdempotent(t *testing.T) {
	t.Parallel()

	store := windowsTrustTestStore(t)
	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	if err := installWindowsTrustRoot(context.Background(), store, root); err != nil {
		t.Fatal(err)
	}

	if err := installWindowsTrustRoot(context.Background(), store, root); err != nil {
		t.Fatal(err)
	}

	current, err := findWindowsTrustRoot(context.Background(), store, root)

	if err != nil || current == nil {
		t.Fatalf("installed root absent: %v", err)
	}

	if err := freeWindowsTrustContext(current); err != nil {
		t.Fatal(err)
	}
}

// TestCertificateTrustWindowsMemoryRemovalIsExact leaves a same-subject,
// same-serial certificate untouched when removing another DER identity.
func TestCertificateTrustWindowsMemoryRemovalIsExact(t *testing.T) {
	t.Parallel()

	store := windowsTrustTestStore(t)
	first, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	second, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	if err := installWindowsTrustRoot(context.Background(), store, first); err != nil {
		t.Fatal(err)
	}

	if err := removeWindowsTrustRoot(context.Background(), store, second); err != nil {
		t.Fatal(err)
	}

	current, err := findWindowsTrustRoot(context.Background(), store, first)

	if err != nil || current == nil {
		t.Fatalf("unrelated same-subject root was removed: %v", err)
	}

	if err := freeWindowsTrustContext(current); err != nil {
		t.Fatal(err)
	}

	if err := removeWindowsTrustRoot(context.Background(), store, first); err != nil {
		t.Fatal(err)
	}

	if err := removeWindowsTrustRoot(context.Background(), store, first); err != nil {
		t.Fatal(err)
	}
}

// TestCertificateTrustWindowsCancellationNeverOpensStore checks cancellation
// before the injected open boundary without requiring administrator privileges.
func TestCertificateTrustWindowsCancellationNeverOpensStore(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	backend := &windowsCertificateTrust{open: func() (syscall.Handle, error) {
		t.Fatal("canceled operation opened a native store")

		return 0, nil
	}}

	if err := backend.Apply(ctx, Certificate{}, "trust"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

// TestCertificateTrustWindowsPermissionFailurePreservesCause injects access
// denial without opening any native machine-wide store or requesting UAC.
func TestCertificateTrustWindowsPermissionFailurePreservesCause(t *testing.T) {
	t.Parallel()

	backend := &windowsCertificateTrust{open: func() (syscall.Handle, error) {
		return 0, syscall.ERROR_ACCESS_DENIED
	}}

	if err := backend.Apply(context.Background(), Certificate{}, "trust"); !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("expected native access denial, got %v", err)
	}
}

// TestCertificateTrustWindowsConcurrentCollisionNeverOverwrites checks that
// add-new collision recovery accepts only exact DER already present in memory.
func TestCertificateTrustWindowsConcurrentCollisionNeverOverwrites(t *testing.T) {
	t.Parallel()

	store := windowsTrustTestStore(t)
	first, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	second, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	if err := installWindowsTrustRoot(context.Background(), store, first); err != nil {
		t.Fatal(err)
	}

	if err := verifyConcurrentWindowsTrustRoot(context.Background(), store, first); err != nil {
		t.Fatal(err)
	}

	if err := verifyConcurrentWindowsTrustRoot(context.Background(), store, second); err == nil {
		t.Fatal("nonidentical add-new collision accepted")
	}

	current, err := findWindowsTrustRoot(context.Background(), store, first)

	if err != nil || current == nil {
		t.Fatalf("existing root was overwritten: %v", err)
	}

	if err := freeWindowsTrustContext(current); err != nil {
		t.Fatal(err)
	}
}

// TestCertificateTrustWindowsTargetNeverOpensStore identifies the machine-wide
// ROOT store before approval without acquiring its native handle or invoking UAC.
func TestCertificateTrustWindowsTargetNeverOpensStore(t *testing.T) {
	t.Parallel()

	backend := &windowsCertificateTrust{open: func() (syscall.Handle, error) {
		t.Fatal("target preflight opened a native store handle")

		return 0, nil
	}}

	target, err := backend.Target()

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(target, `LocalMachine\Root`) {
		t.Fatalf("target omitted concrete store: %q", target)
	}
}

// TestWindowsTrustCloseFailureReportsCompletedMutation uses only a process-local
// native memory store and the backend's public Apply boundary. The injected
// closer fails after installation; inspecting native DER proves that the failed
// cleanup did not undo the completed action. Native Windows execution is required.
func TestWindowsTrustCloseFailureReportsCompletedMutation(t *testing.T) {
	t.Parallel()

	store := windowsTrustTestStore(t)
	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	failure := errors.New("isolated store close failed")
	backend := &windowsCertificateTrust{
		open: func() (syscall.Handle, error) {
			return store, nil
		},
		close: func(syscall.Handle, uint32) error {
			return failure // The fixture retains the handle for inspection and cleanup.
		},
	}

	err = backend.Apply(t.Context(), root, "trust")

	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "native trust action completed") {
		t.Fatalf("completed mutation lost its outcome: %v", err)
	}

	current, err := syscall.CertEnumCertificatesInStore(store, nil)

	if err != nil {
		t.Fatal(err)
	}

	defer syscall.CertFreeCertificateContext(current)

	if !bytes.Equal(unsafe.Slice(current.EncodedCert, int(current.Length)), root.DER) {
		t.Fatal("the isolated native store does not contain the completed installation")
	}
}
