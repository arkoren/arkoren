package filesecret_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/filesecret"
	"arkoren.dev/core/service/secret/secrettest"
)

// writeFile creates a file with data in a per-test directory and returns its path.
func writeFile(t testing.TB, name string, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// TestConformance runs the secret conformance suite for a read-only source.
func TestConformance(t *testing.T) {
	t.Parallel()

	secrettest.Run(t, func(*testing.T) secret.Driver {
		return filesecret.New()
	}, secrettest.WithReadOnly(), secrettest.WithSeed(func(t testing.TB, data []byte) string {
		return writeFile(t, "secret", data)
	}))
}

// TestGetMissingFileIsNotFound verifies a missing path wraps secret.ErrNotFound.
func TestGetMissingFileIsNotFound(t *testing.T) {
	t.Parallel()

	_, err := filesecret.New().Get(t.Context(), secret.Reference{Name: filepath.Join(t.TempDir(), "absent")})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

// TestGetEmptyPathFails verifies an empty reference name is an error, not a
// read of the working directory.
func TestGetEmptyPathFails(t *testing.T) {
	t.Parallel()

	_, err := filesecret.New().Get(t.Context(), secret.Reference{})

	if err == nil || errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get error = %v, want empty path error", err)
	}
}

// TestGetDirectoryFails verifies a directory is an error, not a secret.
func TestGetDirectoryFails(t *testing.T) {
	t.Parallel()

	if _, err := filesecret.New().Get(t.Context(), secret.Reference{Name: t.TempDir()}); err == nil {
		t.Fatal("directory read succeeded")
	}
}

// TestGetFollowsSymlinks verifies a symbolic link reads its target, as mounted
// secrets are laid out.
func TestGetFollowsSymlinks(t *testing.T) {
	t.Parallel()

	target := writeFile(t, "real", []byte("linked"))
	link := filepath.Join(t.TempDir(), "link")

	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	value, err := filesecret.New().Get(t.Context(), secret.Reference{Name: link})

	if err != nil || string(value.Data) != "linked" {
		t.Fatalf("Get = %q, %v", value.Data, err)
	}
}

// TestGetRereadsEveryTime verifies a changed file is visible on the next Get.
func TestGetRereadsEveryTime(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "secret", []byte("first"))
	driver := filesecret.New()

	if _, err := driver.Get(t.Context(), secret.Reference{Name: path}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	value, err := driver.Get(t.Context(), secret.Reference{Name: path})

	if err != nil || string(value.Data) != "second" {
		t.Fatalf("Get = %q, %v", value.Data, err)
	}
}

// TestGetEnforcesSizeCap verifies the 1 MiB cap is inclusive and one more byte
// fails.
func TestGetEnforcesSizeCap(t *testing.T) {
	t.Parallel()

	driver := filesecret.New()
	atCap := writeFile(t, "at-cap", bytes.Repeat([]byte("a"), filesecret.MaxSize))
	overCap := writeFile(t, "over-cap", bytes.Repeat([]byte("a"), filesecret.MaxSize+1))

	if value, err := driver.Get(t.Context(), secret.Reference{Name: atCap}); err != nil || len(value.Data) != filesecret.MaxSize {
		t.Fatalf("Get at cap = %d bytes, %v", len(value.Data), err)
	}

	if _, err := driver.Get(t.Context(), secret.Reference{Name: overCap}); err == nil {
		t.Fatal("Get over cap succeeded")
	}
}

// TestGetKeepsTrailingNewline verifies the driver returns contents unmodified.
func TestGetKeepsTrailingNewline(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "secret", []byte("token\n"))

	value, err := filesecret.New().Get(t.Context(), secret.Reference{Name: path})

	if err != nil || string(value.Data) != "token\n" {
		t.Fatalf("Get = %q, %v", value.Data, err)
	}
}

// ExampleNew reads a file-backed secret and decodes it as a string, which
// drops the trailing newline.
func ExampleNew() {
	dir, err := os.MkdirTemp("", "filesecret")

	if err != nil {
		fmt.Println(err)

		return
	}

	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "token")

	if err := os.WriteFile(path, []byte("fixture-token\n"), 0o600); err != nil {
		fmt.Println(err)

		return
	}

	secrets := secret.New(filesecret.New())

	token, err := secrets.Get(context.Background(), secret.NewSecret[string](path))

	fmt.Println(token, err)
	// Output: fixture-token <nil>
}
