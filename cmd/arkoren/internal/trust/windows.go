//go:build windows

package trust

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// windowsCertificateTrust operates through Crypt32, without certutil, UAC, or
// cgo. Tests can substitute an ephemeral native memory store for LocalMachine.
type windowsCertificateTrust struct {
	// open returns one owned native store handle, closed after each operation.
	open func() (syscall.Handle, error)

	// close isolates native handle release so a cleanup failure can be tested
	// after real process-local store mutations. Nil uses CertCloseStore.
	close func(syscall.Handle, uint32) error
}

const (
	// windowsSystemStoreProvider selects the UTF-16 system-store provider.
	windowsSystemStoreProvider = 10

	// windowsLocalMachineStore selects machine-wide rather than current-user trust.
	windowsLocalMachineStore = 0x00020000

	// windowsOpenExistingStore prevents accidentally creating a misspelled store.
	windowsOpenExistingStore = 0x00004000

	// windowsAddNewCertificate refuses replacement of any existing certificate.
	windowsAddNewCertificate = 1

	// windowsCertificateNotFound is the documented end of store enumeration.
	windowsCertificateNotFound syscall.Errno = 0x80092004

	// windowsCertificateExists identifies an add-new collision or concurrent add.
	windowsCertificateExists syscall.Errno = 0x80092005
)

// NewBackend binds production commands to the native
// LocalMachine ROOT store. No handles are opened before explicit approval.
func NewBackend() Backend {
	return &windowsCertificateTrust{open: openWindowsRootStore}
}

// Target identifies the LocalMachine ROOT store without opening it. The native
// handle is acquired only by Apply after explicit approval and cancellation checks.
//
// Example:
//
//	target, err := backend.Target()
//	if err != nil {
//		return err
//	}
//	fmt.Fprintln(warning, target)
func (backend *windowsCertificateTrust) Target() (string, error) {
	return `Windows LocalMachine\Root certificate store`, nil
}

// openWindowsRootStore opens only the existing LocalMachine ROOT store.
// Crypt32 reports access denied rather than invoking an elevation prompt.
func openWindowsRootStore() (syscall.Handle, error) {
	name, err := syscall.UTF16PtrFromString("ROOT")

	if err != nil {
		return 0, err
	}

	store, err := syscall.CertOpenStore(windowsSystemStoreProvider, 0, 0,
		windowsLocalMachineStore|windowsOpenExistingStore, uintptr(unsafe.Pointer(name)))
	// CertOpenStore's uintptr parameter does not retain the UTF-16 allocation.
	runtime.KeepAlive(name)

	return store, err
}

// Apply enumerates full encodings before any operation. It neither overwrites
// same-subject certificates nor deletes by issuer/serial. Native delete consumes
// its context even on failure, while every other owned context is freed here.
// A failed handle close reports whether the action completed; cleanup never
// rolls back a completed mutation, and errors retain their native causes.
//
// Example, after approval:
//
//	if err := backend.Apply(ctx, root, "untrust"); err != nil {
//		return err // Includes permission and native cleanup failures.
//	}
func (backend *windowsCertificateTrust) Apply(
	ctx context.Context,
	root Certificate,
	action string,
) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	store, err := backend.open()

	if err != nil {
		return fmt.Errorf("open LocalMachine Root store; rerun elevated if access is denied: %w", err)
	}

	closeStore := backend.close

	if closeStore == nil {
		closeStore = syscall.CertCloseStore
	}

	defer func() {
		if e := closeStore(store, 0); e != nil {
			outcome := "native trust action completed"

			if err != nil {
				outcome = "native trust action failed; any completed changes remain"
			}

			err = errors.Join(err, fmt.Errorf("%s; close LocalMachine Root store failed: %w", outcome, e))
		}
	}()

	if action == "untrust" {
		return removeWindowsTrustRoot(ctx, store, root)
	}

	return installWindowsTrustRoot(ctx, store, root)
}

// findWindowsTrustRoot returns an owned context only for identical full DER
// and SHA-256. Enumeration releases previous contexts automatically, including
// when it reaches the end; early cancellation explicitly releases its context.
func findWindowsTrustRoot(
	ctx context.Context,
	store syscall.Handle,
	root Certificate,
) (*syscall.CertContext, error) {
	var previous *syscall.CertContext

	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, freeWindowsTrustContext(previous))
		}

		current, err := syscall.CertEnumCertificatesInStore(store, previous)

		if errors.Is(err, windowsCertificateNotFound) {
			return nil, nil
		}

		if err != nil {
			return nil, fmt.Errorf("enumerate LocalMachine Root certificates: %w", err)
		}

		encoded := unsafe.Slice(current.EncodedCert, int(current.Length))

		if bytes.Equal(encoded, root.DER) && fmt.Sprintf("%x", sha256.Sum256(encoded)) == root.Fingerprint {
			return current, nil
		}

		previous = current
	}
}

// freeWindowsTrustContext releases an optional enumerated context. Native
// free rejects nil, so an empty enumeration needs no native call.
func freeWindowsTrustContext(current *syscall.CertContext) error {
	if current == nil {
		return nil
	}

	return syscall.CertFreeCertificateContext(current)
}

// installWindowsTrustRoot treats exact presence as success and otherwise uses
// ADD_NEW. Concurrent exact installs are harmless; a nonidentical collision is
// reported, never replaced. All cryptographic material stays in owned memory.
func installWindowsTrustRoot(ctx context.Context, store syscall.Handle, root Certificate) (err error) {
	current, err := findWindowsTrustRoot(ctx, store, root)

	if err != nil {
		return err
	}

	if current != nil {
		return freeWindowsTrustContext(current)
	}

	current, err = syscall.CertCreateCertificateContext(syscall.X509_ASN_ENCODING, &root.DER[0], uint32(len(root.DER)))

	if err != nil {
		return fmt.Errorf("create native certificate context: %w", err)
	}

	defer func() {
		if e := freeWindowsTrustContext(current); e != nil {
			err = errors.Join(err, fmt.Errorf("certificate context cleanup failed; any completed trust changes remain: %w", e))
		}
	}()

	if err := ctx.Err(); err != nil {
		return err
	}

	err = syscall.CertAddCertificateContextToStore(store, current, windowsAddNewCertificate, nil)

	if errors.Is(err, windowsCertificateExists) {
		return verifyConcurrentWindowsTrustRoot(ctx, store, root)
	}

	if err != nil {
		return fmt.Errorf("add LocalMachine Root certificate; rerun elevated if access is denied: %w", err)
	}

	return nil
}

// verifyConcurrentWindowsTrustRoot checks add-new collisions without replacing
// an existing certificate. Only complete DER identity grants idempotent success.
func verifyConcurrentWindowsTrustRoot(ctx context.Context, store syscall.Handle, root Certificate) error {
	current, err := findWindowsTrustRoot(ctx, store, root)

	if err != nil {
		return err
	}

	if current == nil {
		return errors.New("LocalMachine Root contains a conflicting certificate; no certificate was overwritten")
	}

	return freeWindowsTrustContext(current)
}

// removeWindowsTrustRoot obtains an exact context, then consumes it through
// Crypt32's delete API. Absence is success; cancellation before deletion frees
// the context instead. Enumeration restarts after each deletion to remove any
// exact duplicate copies. No issuer/serial or subject lookup is used.
func removeWindowsTrustRoot(ctx context.Context, store syscall.Handle, root Certificate) error {
	removed := 0

	for {
		current, err := findWindowsTrustRoot(ctx, store, root)

		if err != nil {
			return windowsRemovalError(removed, err)
		}

		if current == nil {
			return nil
		}

		if err := ctx.Err(); err != nil {
			return windowsRemovalError(removed, errors.Join(err, freeWindowsTrustContext(current)))
		}

		if err := deleteWindowsTrustContext(current); err != nil {
			return windowsRemovalError(removed, fmt.Errorf("exact-root removal may be partial: %w", err))
		}

		removed++
	}
}

// windowsRemovalError retains the count of confirmed deletions if a later
// enumeration, cancellation, or delete fails. Zero retains the original failure;
// earlier successful deletions cannot be undone by an error or cleanup failure.
func windowsRemovalError(removed int, err error) error {
	if removed == 0 {
		return err
	}

	return fmt.Errorf("removed %d exact root certificates; remaining removal is incomplete: %w", removed, err)
}

// deleteWindowsTrustContext consumes current whether deletion succeeds or
// fails. Crypt32 is a known system DLL registered by Go's syscall package, so
// syscall.LoadDLL uses the system directory rather than the working directory.
func deleteWindowsTrustContext(current *syscall.CertContext) (err error) {
	library, err := syscall.LoadDLL("crypt32.dll")

	if err != nil {
		return errors.Join(err, freeWindowsTrustContext(current))
	}

	defer func() {
		err = errors.Join(err, library.Release())
	}()

	procedure, err := library.FindProc("CertDeleteCertificateFromStore")

	if err != nil {
		return errors.Join(err, freeWindowsTrustContext(current))
	}

	result, _, err := procedure.Call(uintptr(unsafe.Pointer(current)))

	if result == 0 {
		return fmt.Errorf("delete exact LocalMachine Root certificate; rerun elevated if access is denied: %w", err)
	}

	return nil
}
