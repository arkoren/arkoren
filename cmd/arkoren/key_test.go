package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/foundation/key"
)

// runKey keeps existing operation scenarios at the real command boundary.
// Prefixing the family exercises global extraction and the static CLI tree.
func runKey(ctx context.Context, args []string, env console.Environment) error {
	return execute(ctx, append([]string{"key"}, args...), env)
}

// TestKeyGenerateFlagsBeforeType exercises urfave's non-sensitive interspersed
// flag syntax while retaining explicit private output and offline execution.
func TestKeyGenerateFlagsBeforeType(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runKey(t.Context(), []string{"generate", "--out", "shared.key", "shared"}, env); err != nil {
		t.Fatal(err)
	}

	if err := runKey(t.Context(), []string{"check", "shared", "shared.key"}, env); err != nil {
		t.Fatal(err)
	}
}

// TestKeyGenerateOffline verifies independent categories without reading settings.
func TestKeyGenerateOffline(t *testing.T) {
	t.Parallel()

	for _, purpose := range []string{"shared", "signing", "exchange"} {
		env := scaffoldEnvironment(t)

		if err := execute(t.Context(), []string{"key", "generate", purpose, "--out", "private.key"}, env); err != nil {
			t.Fatal(err)
		}

		path := filepath.Join(env.Dir, "private.key")

		if err := checkKey(purpose, path); err != nil {
			t.Fatal(err)
		}

		info, err := os.Stat(path)

		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private mode: %v, %v", info, err)
		}

		if strings.Contains(env.Stdout.(*bytes.Buffer).String(), "PRIVATE KEY") {
			t.Fatal("private PEM printed implicitly")
		}
	}
}

// TestKeyCheckRejectsNon32ByteSecrets enforces exact length for shared keys.
func TestKeyCheckRejectsNon32ByteSecrets(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	path := filepath.Join(env.Dir, "invalid.key")

	for _, size := range []int{16, 24, 31, 33, 64} {
		encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, size))

		if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		err := execute(t.Context(), []string{"key", "check", "shared", "invalid.key"}, env)

		if !errors.Is(err, key.ErrInvalid) || env.Stdout.(*bytes.Buffer).Len() != 0 {
			t.Fatalf("shared accepted %d bytes or printed success: %v", size, err)
		}
	}
}

// TestKeyRefusesOverwrite preserves existing credentials byte-for-byte.
func TestKeyRefusesOverwrite(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	path := filepath.Join(env.Dir, "private.key")

	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runKey(t.Context(), []string{"generate", "shared", "--out", path}, env); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error = %v", err)
	}

	contents, err := os.ReadFile(path)

	if err != nil || string(contents) != "existing" {
		t.Fatal("existing key changed")
	}
}

// TestKeyRefusesSymlink prevents following a destination to another credential.
func TestKeyRefusesSymlink(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	path := filepath.Join(env.Dir, "private.key")

	if err := os.Symlink("missing-target", path); err != nil {
		t.Fatal(err)
	}

	if err := runKey(t.Context(), []string{"generate", "signing", "--out", path}, env); !errors.Is(err, os.ErrExist) {
		t.Fatalf("symlink error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "missing-target")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("symlink target created")
	}
}

// TestKeyPrivateStdoutRequiresExplicitDestination guards accidental disclosure.
func TestKeyPrivateStdoutRequiresExplicitDestination(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	requireNewUsage(t, runKey(t.Context(), []string{"generate", "shared"}, env))

	if env.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatal("private material printed on usage failure")
	}

	if err := runKey(t.Context(), []string{"generate", "shared", "--out", "-"}, env); err != nil {
		t.Fatal(err)
	}

	if _, err := key.ParseShared(env.Stdout.(*bytes.Buffer).String()); err != nil {
		t.Fatal(err)
	}
}

// TestKeyPublicAndCheck report validation and export only browser-public material.
func TestKeyPublicAndCheck(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runKey(t.Context(), []string{"generate", "signing", "--out", "vapid.pem"}, env); err != nil {
		t.Fatal(err)
	}

	identity, err := key.LoadSigning(filepath.Join(env.Dir, "vapid.pem"))

	if err != nil {
		t.Fatal(err)
	}

	output := env.Stdout.(*bytes.Buffer)
	output.Reset()

	if err := execute(t.Context(), []string{"key", "public", "signing", "vapid.pem", "--format", "vapid"}, env); err != nil {
		t.Fatal(err)
	}

	if strings.TrimSpace(output.String()) != identity.VAPIDPublicKey() {
		t.Fatal("wrong public encoding")
	}

	output.Reset()

	if err := execute(t.Context(), []string{"key", "check", "signing", "vapid.pem"}, env); err != nil {
		t.Fatal(err)
	}

	document := (result{stdout: output.String()}).json(t)

	if document["category"] != "signing" || document["valid"] != true || document["path"] != filepath.Join(env.Dir, "vapid.pem") || len(document) != 3 {
		t.Fatal("validation output leaked material")
	}
}

// TestKeyPublicDefaultsSPKI verifies signing and exchange export standard
// public PEM by default, not a provider-specific point or private material.
func TestKeyPublicDefaultsSPKI(t *testing.T) {
	t.Parallel()

	for _, category := range []string{"signing", "exchange"} {
		env := scaffoldEnvironment(t)

		if err := runKey(t.Context(), []string{"generate", category, "--out", "private.pem"}, env); err != nil {
			t.Fatal(err)
		}

		output := env.Stdout.(*bytes.Buffer)
		output.Reset()

		if err := runKey(t.Context(), []string{"public", category, "private.pem"}, env); err != nil {
			t.Fatal(err)
		}

		if !strings.HasPrefix(output.String(), "-----BEGIN PUBLIC KEY-----\n") {
			t.Fatal("public command did not default to SPKI PEM")
		}

		want, err := publicKeyMaterial(category, filepath.Join(env.Dir, "private.pem"), "pem")

		if err != nil || !bytes.Equal(want, output.Bytes()) {
			t.Fatalf("wrong public export: %v", err)
		}
	}
}

// TestKeyPublicFormatBeforeFile preserves ordinary flag-first CLI syntax
// as well as the documented file-first form.
func TestKeyPublicFormatBeforeFile(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runKey(t.Context(), []string{"generate", "signing", "--out", "private.pem"}, env); err != nil {
		t.Fatal(err)
	}

	output := env.Stdout.(*bytes.Buffer)
	output.Reset()

	if err := runKey(t.Context(), []string{"public", "signing", "--format", "vapid", "private.pem"}, env); err != nil {
		t.Fatal(err)
	}

	identity, err := key.LoadSigning(filepath.Join(env.Dir, "private.pem"))

	if err != nil || output.String() != identity.VAPIDPublicKey()+"\n" {
		t.Fatalf("wrong VAPID export: %v", err)
	}
}

// TestKeyHelpOffline verifies both help entry points skip settings resolution.
func TestKeyHelpOffline(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"key", "-h"}, {"key", "--help"}} {
		env := scaffoldEnvironment(t)

		if err := execute(t.Context(), args, env); err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(env.Stdout.(*bytes.Buffer).String(), "generate") {
			t.Fatal("key help missing")
		}
	}
}

// TestKeyRejectsUsage validates command shapes before any filesystem operation.
func TestKeyRejectsUsage(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{}, {"generate"}, {"generate", "rsa", "--out", "key"}, {"generate", "shared", "--force"},
		{"generate", "shared", "--out", "key", "extra"}, {"public", "shared", "key"},
		{"check", "signing"}, {"unknown", "signing", "key"},
		{"public", "exchange", "missing", "--format", "vapid"},
		{"public", "signing", "missing", "--format", "invalid"},
		{"generate", "encryption", "--out", "key"}, {"generate", "link", "--out", "key"},
		{"generate", "cursor", "--out", "key"}, {"generate", "vapid", "--out", "key"},
	} {
		env := scaffoldEnvironment(t)

		requireNewUsage(t, runKey(t.Context(), args, env))

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 0 {
			t.Fatal("usage failure wrote a file")
		}
	}
}

// TestKeyInvalidFileDoesNotEchoContents keeps parser diagnostics secret-free.
func TestKeyInvalidFileDoesNotEchoContents(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.WriteFile(filepath.Join(env.Dir, "bad"), []byte("sensitive-invalid-content"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runKey(t.Context(), []string{"check", "shared", "bad"}, env)

	if !errors.Is(err, key.ErrInvalid) || strings.Contains(err.Error(), "sensitive-invalid-content") {
		t.Fatalf("unsafe validation error: %v", err)
	}
}

// TestKeyCanceledDoesNotWrite preserves cancellation before provisioning.
func TestKeyCanceledDoesNotWrite(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := runKey(ctx, []string{"generate", "signing", "--out", "key"}, env); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

// TestKeyStdoutFailure propagates the existing short-write output policy.
func TestKeyStdoutFailure(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Stdout = &console.Output{Writer: shortKeyWriter{}}

	err := runKey(t.Context(), []string{"generate", "shared", "--out", "-"}, env)

	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write = %v", err)
	}
}

// TestKeyPublicStdoutFailure verifies public exports share the command's
// short-write failure policy rather than reporting a successful operation.
func TestKeyPublicStdoutFailure(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runKey(t.Context(), []string{"generate", "exchange", "--out", "private.pem"}, env); err != nil {
		t.Fatal(err)
	}

	env.Stdout = &console.Output{Writer: shortKeyWriter{}}

	err := runKey(t.Context(), []string{"public", "exchange", "private.pem"}, env)

	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("public short write = %v", err)
	}
}

// shortKeyWriter simulates a pipe that accepts no complete secret output.
type shortKeyWriter struct{}

// Write reports a short write without an underlying error.
func (shortKeyWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}
