//go:build !windows

package trust

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// linuxTrustLayout names a supported distribution's existing anchor directory
// and native refresh utility. No arbitrary system directories are created.
type linuxTrustLayout struct {
	// directory is the absolute, distribution-provided local anchor directory.
	directory string

	// command is the absolute refresh executable, never resolved through PATH.
	command string

	// args are the distribution's refresh arguments, if any.
	args []string

	// extension is .crt for Debian-family systems and .pem for Fedora-family.
	extension string
}

// linuxCertificateTrust manages only digest-named Arkoren anchors. It does not
// remove matching certificates installed elsewhere by users or administrators.
// Administrators must serialize anchor edits; refresh is not transactional.
type linuxCertificateTrust struct {
	// layouts are the supported directories probed in order before any writes.
	layouts []linuxTrustLayout

	// command isolates native utility execution for deterministic tests.
	command certificateTrustCommand

	// open isolates opening the privileged directory; tests inject permission
	// failures regardless of the user running the test process.
	open func(string) (*os.Root, error)

	// selected pins the read-only preflight result through approval and Apply.
	// Reprobing could select a different store after the user confirms a target.
	selected *linuxTrustLayout
}

// trustAnchorFile exposes only the persistence operations needed after an
// exclusive create. It deliberately provides no removal operation: a failed
// write cannot establish that the pathname still identifies the created file.
type trustAnchorFile interface {
	io.WriteCloser

	// Sync makes successful writes durable before the native store is refreshed.
	// For example, persistLinuxTrustAnchor invokes file.Sync() after a full write
	// and returns any error together with the eventual Close error.
	Sync() error
}

// supportedLinuxTrustLayouts returns the two initially supported layouts.
// Executable discovery stays independent of the caller's potentially unsafe PATH.
func supportedLinuxTrustLayouts() []linuxTrustLayout {
	return []linuxTrustLayout{
		{
			directory: "/usr/local/share/ca-certificates",
			command:   "/usr/sbin/update-ca-certificates",
			extension: ".crt",
		},
		{
			directory: "/etc/pki/ca-trust/source/anchors",
			command:   "/usr/bin/update-ca-trust",
			args:      []string{"extract"},
			extension: ".pem",
		},
	}
}

// findLinuxTrustLayout requires both an existing anchor directory and an
// executable refresh tool. A missing utility fails before an anchor is changed.
func findLinuxTrustLayout(layouts []linuxTrustLayout) (linuxTrustLayout, error) {
	var unavailable error

	for _, layout := range layouts {
		info, err := os.Stat(layout.directory)

		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			return linuxTrustLayout{}, fmt.Errorf("inspect system trust directory: %w", err)
		}

		if !info.IsDir() {
			continue
		}

		if err := checkTrustExecutable(layout.command); err != nil {
			unavailable = errors.Join(unavailable, err)
			continue
		}

		return layout, nil
	}

	if unavailable != nil {
		return linuxTrustLayout{}, unavailable
	}

	return linuxTrustLayout{}, errors.New("unsupported Linux system trust layout; expected Debian/Ubuntu or Fedora/RHEL")
}

// checkTrustExecutable rejects missing and non-executable refresh utilities.
// Checking before file mutation avoids a predictable partial installation.
func checkTrustExecutable(path string) error {
	info, err := os.Stat(path)

	if err != nil {
		return fmt.Errorf("system trust refresh utility %s is unavailable: %w", path, err)
	}

	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("system trust refresh utility %s is not executable", path)
	}

	return nil
}

// Target selects an existing supported anchor directory and refresh utility,
// without opening a store handle or creating files. The selection is retained
// so Apply cannot silently switch distributions after the user approves it.
//
// Example:
//
//	target, err := backend.Target()
//	if err != nil {
//		return err
//	}
//	fmt.Fprintln(warning, target)
func (backend *linuxCertificateTrust) Target() (string, error) {
	if backend.selected == nil {
		layout, err := findLinuxTrustLayout(backend.layouts)

		if err != nil {
			return "", err
		}

		layout.args = slices.Clone(layout.args)
		backend.selected = &layout
	}

	return "Linux system trust anchors: " + backend.selected.directory, nil
}

// Apply opens only the supported anchor directory, verifies digest ownership,
// then refreshes the native store. Repeated operations refresh too, allowing a
// caller to repair a previous refresh failure without replacing any files.
//
// Example, after approval:
//
//	if err := backend.Apply(ctx, root, "untrust"); err != nil {
//		return err // The error can describe partial work.
//	}
func (backend *linuxCertificateTrust) Apply(
	ctx context.Context,
	root Certificate,
	action string,
) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	if _, err := backend.Target(); err != nil {
		return err
	}

	layout := *backend.selected
	directory, err := backend.open(layout.directory)

	if err != nil {
		return fmt.Errorf("open system trust directory; rerun elevated if permission is denied: %w", err)
	}

	defer func() {
		err = errors.Join(err, directory.Close())
	}()

	name := "arkoren-" + root.Fingerprint + layout.extension

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := changeLinuxTrustAnchor(directory, name, root, action); err != nil {
		return fmt.Errorf("change system trust anchor; rerun elevated if permission is denied: %w", err)
	}

	if _, err := backend.command(ctx, layout.command, layout.args...); err != nil {
		return fmt.Errorf("anchor %s is now in the requested %s state, but native trust refresh %q "+
			"with arguments %q failed; trust may be stale; rerun this command elevated to retry refresh: %w",
			filepath.Join(layout.directory, name), action, layout.command, layout.args, err)
	}

	return nil
}

// changeLinuxTrustAnchor refuses links and files that do not contain the exact
// canonical certificate expected at the SHA-256-derived path. An absent removal
// is harmless; an install is exclusive and never overwrites an existing anchor.
func changeLinuxTrustAnchor(
	directory *os.Root,
	name string,
	root Certificate,
	action string,
) error {
	info, err := directory.Lstat(name)

	if errors.Is(err, os.ErrNotExist) {
		if action == "untrust" {
			return nil
		}

		return writeLinuxTrustAnchor(directory, name, root.pem)
	}

	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return errors.New("refusing a non-regular or symlink trust anchor")
	}

	if err := verifyLinuxTrustAnchor(directory, name, root.pem); err != nil {
		return err
	}

	if action == "untrust" {
		return directory.Remove(name)
	}

	return nil
}

// verifyLinuxTrustAnchor requires exact canonical PEM, not merely the expected
// filename. This protects unrelated files from removal and detects local edits.
func verifyLinuxTrustAnchor(directory *os.Root, name string, expected []byte) error {
	file, err := directory.Open(name)

	if err != nil {
		return err
	}

	return matchLinuxTrustAnchor(file, expected)
}

// matchLinuxTrustAnchor checks exact canonical ownership using at most one
// byte beyond the expected encoding. Oversized files are rejected without
// reading their remaining contents. The reader is always closed, and close
// failures remain inspectable alongside any read or ownership error.
func matchLinuxTrustAnchor(file io.ReadCloser, expected []byte) (err error) {
	defer func() {
		err = errors.Join(err, file.Close())
	}()

	actual, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))

	if err != nil {
		return err
	}

	if !bytes.Equal(actual, expected) {
		return errors.New("trust anchor contents differ from the selected SHA-256 identity; refusing changes")
	}

	return nil
}

// writeLinuxTrustAnchor exclusively creates a public, readable anchor. Any
// persistence failure leaves the possibly incomplete file for manual inspection;
// deleting its pathname could remove a replacement created by another process.
func writeLinuxTrustAnchor(directory *os.Root, name string, encoded []byte) error {
	file, err := directory.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)

	if err != nil {
		return err
	}

	return persistLinuxTrustAnchor(file, filepath.Join(directory.Name(), name), encoded)
}

// persistLinuxTrustAnchor writes, syncs, and closes a newly created anchor.
// Short writes are errors even without an underlying error. Sync runs only
// after a successful complete write, and Close runs on every path. Failures
// retain their causes and explicitly describe the uncertain on-disk state;
// this function cannot delete or rename the possibly replaced pathname.
func persistLinuxTrustAnchor(file trustAnchorFile, path string, encoded []byte) error {
	written, err := file.Write(encoded)

	if written != len(encoded) && err == nil {
		err = io.ErrShortWrite
	}

	if err == nil {
		err = file.Sync()
	}

	err = errors.Join(err, file.Close())

	if err != nil {
		return fmt.Errorf("a possibly incomplete trust anchor may remain at %s; refresh was not attempted; "+
			"inspect it and remove it only after confirming it is incomplete and not deployed: %w", path, err)
	}

	return nil
}
