//go:build !windows

package trust

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// macCertificateTrust changes local-admin SSL trust using only Apple's system
// utility. Removal clears admin trust settings, but deliberately retains public
// keychain certificates: deleting a keychain item is not necessary to untrust it
// and can disrupt identities owned by other tools. User/vendor stores are untouched.
type macCertificateTrust struct {
	// command supplies read-only inspection and exact-certificate mutations.
	command certificateTrustCommand

	// elevated prevents security from initiating its own authentication dialog.
	elevated func() bool

	// temporaryDirectory is a trusted staging parent. Production uses the
	// root-owned sticky /private/var/tmp, never an inherited TMPDIR that an
	// unelevated user could rename underneath a privileged security process.
	temporaryDirectory string
}

// macSystemKeychain fixes the system scope independently of login search lists.
const macSystemKeychain = "/Library/Keychains/System.keychain"

// Target names the concrete system keychain and admin trust domain before
// approval. Utility preflight is read-only and opens no native store handles.
//
// Example:
//
//	target, err := backend.Target()
//	if err != nil {
//		return err
//	}
//	fmt.Fprintln(warning, target)
func (backend *macCertificateTrust) Target() (string, error) {
	if err := checkTrustExecutable("/usr/bin/security"); err != nil {
		return "", err
	}

	return macSystemKeychain + " (local admin trust settings)", nil
}

// Apply stages canonical public material in a private temporary directory. It
// inspects full DER before use and passes only the staged snapshot to security.
// Reinstalling replaces this certificate's admin trust policy with SSL trust;
// removing clears all admin trust settings for this exact root, not user trust.
// Staging-cleanup failures report whether the native change was not attempted,
// possibly partial, or completed; a cleanup error never implies rollback.
//
// Example, after approval:
//
//	if err := backend.Apply(ctx, root, "untrust"); err != nil {
//		return err // Do not claim successful removal on failure.
//	}
func (backend *macCertificateTrust) Apply(
	ctx context.Context,
	root Certificate,
	action string,
) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	if !backend.elevated() {
		return errors.New("system keychain changes require root; rerun this command elevated; no elevation was attempted")
	}

	directory, err := os.MkdirTemp(backend.temporaryDirectory, "arkoren-trust-")

	if err != nil {
		return fmt.Errorf("stage public certificate: %w", err)
	}

	changeStatus := "no trust change was attempted"
	defer func() {
		if e := os.RemoveAll(directory); e != nil {
			err = errors.Join(err, fmt.Errorf("%s; staging cleanup failed at %s: %w", changeStatus, directory, e))
		}
	}()

	path := filepath.Join(directory, "root.pem")

	if err := os.WriteFile(path, root.pem, 0o600); err != nil {
		return fmt.Errorf("stage public certificate: %w", err)
	}

	_, err = backend.inspect(ctx, root)

	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	changeStatus = "native trust change may be partial"

	if err := backend.change(ctx, path, action); err != nil {
		return err
	}

	changeStatus = "native trust command completed"

	if action == "untrust" {
		return nil
	}

	return backend.verifyInstalled(ctx, root)
}

// inspect parses the System keychain's public certificate inventory and checks
// full DER equality. Matching subjects and serials never select an operation;
// even an absent item is removed from admin trust using the staged exact DER,
// since trust settings can exist independently of a keychain certificate.
func (backend *macCertificateTrust) inspect(ctx context.Context, root Certificate) (bool, error) {
	encoded, err := backend.command(ctx, "/usr/bin/security", "find-certificate", "-a", "-p", macSystemKeychain)

	if err != nil {
		return false, fmt.Errorf("inspect System keychain: %w", err)
	}

	return containsTrustCertificate(encoded, root.DER)
}

// verifyInstalled checks the exact post-install keychain entry. A failed
// inspection is explicitly partial: native trust settings may already exist.
func (backend *macCertificateTrust) verifyInstalled(ctx context.Context, root Certificate) error {
	found, err := backend.inspect(ctx, root)

	if err != nil {
		return fmt.Errorf("trust settings may have changed; cannot verify installed root: %w", err)
	}

	if !found {
		return errors.New("trust settings may have changed, but the exact root is absent from the System keychain")
	}

	return nil
}

// containsTrustCertificate validates a public PEM inventory and compares the
// whole DER certificate. Malformed inventory is an error, not evidence of absence.
func containsTrustCertificate(encoded, expected []byte) (bool, error) {
	found := false

	for len(bytes.TrimSpace(encoded)) != 0 {
		block, rest := pem.Decode(encoded)

		if block == nil || block.Type != "CERTIFICATE" {
			return false, errors.New("native certificate inventory is not public certificate PEM")
		}

		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return false, fmt.Errorf("parse native certificate inventory: %w", err)
		}

		found = found || bytes.Equal(block.Bytes, expected)
		encoded = rest
	}

	return found, nil
}

// change uses security's exact certificate input rather than a name/hash delete.
// Known absence diagnostics make removal idempotent; unfamiliar diagnostics,
// including localized ones, fail closed. Installation can partially add the
// keychain item before a trust-settings failure, which the error makes explicit.
func (backend *macCertificateTrust) change(ctx context.Context, path, action string) error {
	args := []string{"remove-trusted-cert", "-d", path}

	if action == "trust" {
		args = []string{"add-trusted-cert", "-d", "-r", "trustRoot", "-p", "ssl", "-k", macSystemKeychain, path}
	}

	output, err := backend.command(ctx, "/usr/bin/security", args...)

	if err == nil {
		return nil
	}

	if ctx.Err() != nil {
		return fmt.Errorf("system trust change canceled; keychain or trust settings may have changed: %w", ctx.Err())
	}

	if action == "untrust" && absentMacTrustSettings(output) {
		return nil
	}

	return fmt.Errorf("system keychain %s failed; a keychain item or trust settings may have changed; "+
		"rerun elevated to retry; no automatic elevation was attempted: %w", action, err)
}

// absentMacTrustSettings recognizes only Apple's documented missing-record
// diagnostic from the removal API, not an arbitrary utility or permission error.
func absentMacTrustSettings(output []byte) bool {
	text := strings.TrimSpace(string(output))

	return text == "security: SecTrustSettingsRemoveTrustSettings: No Trust Settings were found." ||
		text == "security: SecTrustSettingsRemoveTrustSettings: The specified item could not be found in the keychain."
}
