//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCertificateOutputModes verifies that public certificates request 0644 and
// private keys never grant group/other access. Umask may further restrict both.
func TestCertificateOutputModes(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "cert.pem", "--key", "key.pem"}, env); err != nil {
		t.Fatal(err)
	}

	private, err := os.Stat(filepath.Join(env.Dir, "key.pem"))

	if err != nil {
		t.Fatal(err)
	}

	if private.Mode().Perm() != 0o600 {
		t.Fatalf("private mode = %o", private.Mode().Perm())
	}

	public, err := os.Stat(filepath.Join(env.Dir, "cert.pem"))

	if err != nil {
		t.Fatal(err)
	}

	if public.Mode().Perm() & ^os.FileMode(0o644) != 0 {
		t.Fatalf("public mode = %o", public.Mode().Perm())
	}
}
