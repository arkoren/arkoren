//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestCertificateWindowsPrivateACL inspects a real newly created key's protected
// DACL. Exactly one noninherited allow ACE grants full control to the current
// user, rather than inheriting a broad parent ACL or relying on mode bits.
func TestCertificateWindowsPrivateACL(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "cert.pem", "--key", "key.pem"}, env); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(env.Dir, "key.pem")
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)

	if err != nil {
		t.Fatal(err)
	}

	control, _, err := descriptor.Control()

	if err != nil {
		t.Fatal(err)
	}

	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("private DACL permits inherited access")
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()

	if err != nil {
		t.Fatal(err)
	}

	want := "D:P(A;;FA;;;" + user.User.Sid.String() + ")"

	if descriptor.String() != want {
		t.Fatalf("private ACL = %s, want %s", descriptor.String(), want)
	}

	if content, err := os.ReadFile(path); err != nil || len(content) == 0 {
		t.Fatalf("protected key unreadable: %v", err)
	}
}

// TestWindowsKeyGenerationUsesPrivateACL checks all key categories through the
// public CLI under a deliberately broad inheritable temporary-directory DACL.
// This test requires native Windows execution; Unix mode bits cannot prove it.
func TestWindowsKeyGenerationUsesPrivateACL(t *testing.T) {
	t.Parallel()

	for _, category := range []string{"shared", "signing", "exchange"} {
		env := scaffoldEnvironment(t)
		allowInheritedEveryone(t, env.Dir)

		if code := run(t.Context(), []string{"key", "generate", category, "--out", "private.key"}, env); code != 0 {
			t.Fatalf("key generation exit = %d", code)
		}

		assertPrivateWindowsFile(t, filepath.Join(env.Dir, "private.key"))
	}
}

// TestWindowsScaffoldEnvironmentUsesPrivateACL observes the generated .env
// through the public new command. A protected owner-only DACL must replace the
// broad parent inheritance at exclusive creation, before any content is written.
func TestWindowsScaffoldEnvironmentUsesPrivateACL(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	allowInheritedEveryone(t, env.Dir)

	if code := run(t.Context(), []string{"new", ".", "--no-agent-files"}, env); code != 0 {
		t.Fatalf("scaffolding exit = %d", code)
	}

	assertPrivateWindowsFile(t, filepath.Join(env.Dir, ".env"))
}

// allowInheritedEveryone grants broad inheritance only to a disposable fixture
// directory. This prevents an already-private t.TempDir parent from hiding a
// regression in the CLI's creation-time private-file protection.
func allowInheritedEveryone(t *testing.T, path string) {
	t.Helper()

	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")

	if err != nil {
		t.Fatal(err)
	}

	dacl, _, err := descriptor.DACL()

	if err != nil {
		t.Fatal(err)
	}

	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)

	if err != nil {
		t.Fatal(err)
	}
}

// assertPrivateWindowsFile inspects the native file security descriptor and
// actual persisted bytes, not source code or a mocked creation API. Exactly the
// current user may have an allow ACE, and the DACL must block inherited access.
func assertPrivateWindowsFile(t *testing.T, path string) {
	t.Helper()

	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)

	if err != nil {
		t.Fatal(err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()

	if err != nil {
		t.Fatal(err)
	}

	want := "D:P(A;;FA;;;" + user.User.Sid.String() + ")"

	if descriptor.String() != want {
		t.Fatalf("private file DACL = %s, want %s", descriptor.String(), want)
	}

	if content, err := os.ReadFile(path); err != nil || len(content) == 0 {
		t.Fatalf("private file has no readable content: %v", err)
	}
}
