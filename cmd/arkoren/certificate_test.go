package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/core/http/client"
	"arkoren.dev/core/http/server"
	"arkoren.dev/foundation/certificate"
)

// runCertificate keeps provisioning scenarios at the real command boundary.
// Prefixing the family exercises globals and typed arguments, not an argv shim
// in production operations.
func runCertificate(ctx context.Context, args []string, env console.Environment) error {
	return execute(ctx, append([]string{"certificate"}, args...), env)
}

// TestCertificateHelpOffline checks both help spellings, including nested help,
// before invalid endpoint settings, missing env files, or environment lookup.
func TestCertificateHelpOffline(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"certificate", "-h"}, {"certificate", "--help"},
		{"certificate", "root", "generate", "--help"},
		{"certificate", "root", "trust", "--help"},
	} {
		env := scaffoldEnvironment(t)
		args = append(args, "--url", "invalid", "--env-file", "missing")

		if code := run(t.Context(), args, env); code != console.ExitOK {
			t.Fatalf("help exit %d: %s", code, env.Stderr)
		}

		contains(t, env.Stdout.(*bytes.Buffer).String(), "arkoren certificate", "--help")

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 0 {
			t.Fatalf("help wrote files: %v", err)
		}
	}
}

// TestCertificateGenerateOfflineRoundTrip verifies explicit root/server output,
// repeated DNS/IP SANs and issuer signatures without automatic trust changes.
func TestCertificateGenerateOfflineRoundTrip(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := execute(t.Context(), []string{"certificate", "root", "generate", "--name", "Development root",
		"--cert", "root.pem", "--key", "root-key.pem", "--env-file", "missing", "--url", "invalid"}, env); err != nil {
		t.Fatal(err)
	}

	if err := execute(t.Context(), []string{"certificate", "generate", "--ca-cert", "root.pem", "--ca-key", "root-key.pem",
		"--dns", "localhost", "--ip", "127.0.0.1", "--dns", "dev.example.test", "--ip", "::1",
		"--cert", "server.pem", "--key", "server-key.pem"}, env); err != nil {
		t.Fatal(err)
	}

	identity, err := certificate.LoadIdentity(
		filepath.Join(env.Dir, "server.pem"), filepath.Join(env.Dir, "server-key.pem"))

	if err != nil {
		t.Fatal(err)
	}

	leaf := identity.Bundle().Certificates()[0]

	if !reflect.DeepEqual(leaf.DNSNames, []string{"localhost", "dev.example.test"}) {
		t.Fatalf("DNS SANs = %v", leaf.DNSNames)
	}

	if fmt.Sprint(leaf.IPAddresses) != "[127.0.0.1 ::1]" {
		t.Fatalf("IP SANs = %v", leaf.IPAddresses)
	}

	root, err := certificate.LoadBundle(filepath.Join(env.Dir, "root.pem"))

	if err != nil {
		t.Fatal(err)
	}

	if err := leaf.CheckSignatureFrom(root.Certificates()[0]); err != nil {
		t.Fatal(err)
	}

	output := env.Stdout.(*bytes.Buffer).String()

	contains(t, output, `"trust_changed": false`)

	if strings.Contains(output, "PRIVATE KEY") {
		t.Fatal("private PEM leaked to stdout")
	}
}

// TestCertificateDNSDoesNotSplitCommas prevents a slice-flag migration from
// turning one invalid DNS input into two independently authorized SANs.
func TestCertificateDNSDoesNotSplitCommas(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "root.pem", "--key", "root-key.pem"}, env); err != nil {
		t.Fatal(err)
	}

	err := runCertificate(t.Context(), []string{"generate", "--ca-cert", "root.pem", "--ca-key", "root-key.pem",
		"--dns", "one.example.test,two.example.test", "--cert", "server.pem", "--key", "server-key.pem"}, env)

	if !errors.Is(err, certificate.ErrInvalid) {
		t.Fatalf("expected unsplit DNS input to fail validation, got %v", err)
	}

	if _, err := os.Lstat(filepath.Join(env.Dir, "server.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid SAN created a certificate: %v", err)
	}
}

// TestCertificateIPDoesNotSplitCommas rejects a comma-bearing IP before issuer
// reads and output creation. Each IP SAN must be supplied by its own --ip flag.
func TestCertificateIPDoesNotSplitCommas(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runCertificate(t.Context(), []string{"generate", "--ca-cert", "missing.pem", "--ca-key", "missing-key.pem",
		"--ip", "127.0.0.1,::1", "--cert", "server.pem", "--key", "server-key.pem"}, env)

	requireNewUsage(t, err)

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid IP SAN created output: %v", err)
	}
}

// TestCertificateCheckForms validates a persisted identity and bundle through
// their distinct check forms, reporting parsing/matching rather than trust.
func TestCertificateCheckForms(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "root.pem", "--key", "key.pem"}, env); err != nil {
		t.Fatal(err)
	}

	identity, err := certificate.LoadIdentity(filepath.Join(env.Dir, "root.pem"), filepath.Join(env.Dir, "key.pem"))

	if err != nil {
		t.Fatal(err)
	}

	if _, err := identity.TLS(); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"check", "--cert", "root.pem"}, {"check", "--cert", "root.pem", "--key", "key.pem"},
	} {
		if err := execute(t.Context(), append([]string{"certificate"}, args...), env); err != nil {
			t.Fatal(err)
		}
	}

	contains(t, env.Stdout.(*bytes.Buffer).String(),
		`"parsed": true`, `"key_matched": true`, `"key_matched": false`, `"expiry_checked": false`, `"trust_checked": false`)
}

// TestCertificateIPOnlySAN accepts IP-only servers without fabricating a DNS
// SAN or requiring a common-name/localhost default from the command line.
func TestCertificateIPOnlySAN(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "root.pem", "--key", "root-key.pem"}, env); err != nil {
		t.Fatal(err)
	}

	if err := runCertificate(t.Context(), []string{"generate", "--ca-cert", "root.pem", "--ca-key", "root-key.pem",
		"--ip", "127.0.0.1", "--cert", "cert.pem", "--key", "key.pem"}, env); err != nil {
		t.Fatal(err)
	}

	bundle, err := certificate.LoadBundle(filepath.Join(env.Dir, "cert.pem"))

	if err != nil {
		t.Fatal(err)
	}

	leaf := bundle.Certificates()[0]

	if len(leaf.DNSNames) != 0 || fmt.Sprint(leaf.IPAddresses) != "[127.0.0.1]" {
		t.Fatalf("unexpected SANs: DNS=%v, IP=%v", leaf.DNSNames, leaf.IPAddresses)
	}
}

// TestCertificateMissingIssuerDoesNotCreateOutputs checks read failures after
// successful destination preflight, before either half of a new pair is written.
func TestCertificateMissingIssuerDoesNotCreateOutputs(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runCertificate(t.Context(), []string{"generate", "--ca-cert", "missing.pem", "--ca-key", "missing-key.pem",
		"--dns", "localhost", "--cert", "cert.pem", "--key", "key.pem"}, env)

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing issuer = %v", err)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("issuer failure wrote files: %v", err)
	}
}

// TestCertificateRejectsUsageBeforeEffects protects grammar validation from
// falling through to issuer reads, writes, or trust-store execution.
func TestCertificateRejectsUsageBeforeEffects(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{}, {"root"}, {"unknown"}, {"root", "unknown"},
		{"root", "generate", "--cert", "root.pem", "--key", "root-key.pem"},
		{"root", "generate", "--name", "Root", "--cert", "root.pem"},
		{"root", "generate", "--name", "Root", "--cert", "root.pem", "--key", "-"},
		{"root", "generate", "--name", "Root", "--cert", "-", "--key", "key.pem"},
		{"root", "generate", "--name", "Root", "--cert", "root.pem", "--key", "key.pem", "extra"},
		{"root", "generate", "--name", "Root", "--cert", "root.pem", "--key", "key.pem", "--force"},
		{"generate", "--dns", "localhost", "--cert", "cert.pem", "--key", "key.pem"},
		{"generate", "--ca-cert", "missing", "--ca-key", "missing", "--cert", "cert.pem", "--key", "key.pem"},
		{"generate", "--ca-cert", "missing", "--ca-key", "missing", "--ip", "invalid",
			"--cert", "cert.pem", "--key", "key.pem"},
		{"check"}, {"check", "--cert", "missing", "extra"}, {"check", "--cert", "missing", "--force"},
	} {
		env := scaffoldEnvironment(t)

		requireNewUsage(t, runCertificate(t.Context(), args, env))

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 0 {
			t.Fatalf("usage failure wrote files: %v", err)
		}
	}
}

// TestCertificateCheckDoesNotAssertExpiry proves the structural check succeeds
// for expired material instead of quietly becoming x509.Verify. The fixture
// has fixed historical dates; no sleep or wall-clock change is needed.
func TestCertificateCheckDoesNotAssertExpiry(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	identity, err := certificate.GenerateRoot("Expired root")

	if err != nil {
		t.Fatal(err)
	}

	pair, err := identity.TLS()

	if err != nil {
		t.Fatal(err)
	}

	template := *identity.Bundle().Certificates()[0]
	template.NotBefore = time.Date(1999, time.January, 1, 0, 0, 0, 0, time.UTC)
	template.NotAfter = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, template.PublicKey, pair.PrivateKey)

	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(env.Dir, "root.pem"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCertificate(t.Context(), []string{"check", "--cert", "root.pem"}, env); err != nil {
		t.Fatal(err)
	}

	contains(t, env.Stdout.(*bytes.Buffer).String(), `"expiry_checked": false`, `"trust_checked": false`)
}

// TestCertificateMismatchedPairFails rejects individually parseable certificates
// and private keys that do not form an identity, without printing key bytes.
func TestCertificateMismatchedPairFails(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	first, err := certificate.GenerateRoot("First")

	if err != nil {
		t.Fatal(err)
	}

	second, err := certificate.GenerateRoot("Second")

	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(env.Dir, "root.pem"), first.Bundle().PEM(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(env.Dir, "wrong-key.pem"), second.PrivatePEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	err = runCertificate(t.Context(), []string{"check", "--cert", "root.pem", "--key", "wrong-key.pem"}, env)

	if err == nil || env.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatalf("mismatched pair check = %v", err)
	}

	if strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatal("private PEM leaked")
	}
}

// TestCertificateMalformedMaterialSecretFree keeps parser errors and stdout
// free of the invalid input's contents, including arbitrary private bytes.
func TestCertificateMalformedMaterialSecretFree(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.WriteFile(filepath.Join(env.Dir, "bad.pem"), []byte("sensitive-invalid-material"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runCertificate(t.Context(), []string{"check", "--cert", "bad.pem"}, env)

	if err == nil {
		t.Fatal("accepted malformed certificate")
	}

	if strings.Contains(err.Error(), "sensitive-invalid-material") || env.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatalf("unsafe error/output: %v", err)
	}
}

// TestCertificateCanceledDoesNotWrite observes cancellation before provisioning
// and never creates output files or emits a success message.
func TestCertificateCanceledDoesNotWrite(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := runCertificate(ctx, []string{"root", "generate", "--name", "Root",
		"--cert", "root.pem", "--key", "key.pem"}, env)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled generation wrote: %v", err)
	}
}

// TestCertificateOutputFailureUsesFailureExit checks the shared short-write
// boundary even after successful persistence; no secret is routed to stdout.
func TestCertificateOutputFailureUsesFailureExit(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Stdout = shortKeyWriter{}

	code := run(t.Context(), []string{"certificate", "root", "generate", "--name", "Root",
		"--cert", "root.pem", "--key", "key.pem"}, env)

	if code != console.ExitFailure {
		t.Fatalf("short stdout exit = %d", code)
	}

	contains(t, env.Stderr.(*bytes.Buffer).String(), io.ErrShortWrite.Error())
}

// TestCertificateHelpOutputFailureUsesFailureExit applies the first-error stdout
// boundary to offline help, before any endpoint settings can be resolved.
func TestCertificateHelpOutputFailureUsesFailureExit(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Stdout = shortKeyWriter{}

	if code := run(t.Context(), []string{"certificate", "--help"}, env); code != console.ExitFailure {
		t.Fatalf("help short stdout exit = %d", code)
	}
}

// TestCertificateCheckOutputFailureUsesFailureExit verifies a valid structural
// check cannot report success when its stdout result fails to reach the caller.
func TestCertificateCheckOutputFailureUsesFailureExit(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "root.pem", "--key", "key.pem"}, env); err != nil {
		t.Fatal(err)
	}

	env.Stdout = shortKeyWriter{}

	if code := run(t.Context(), []string{"certificate", "check", "--cert", "root.pem",
		"--key", "key.pem"}, env); code != console.ExitFailure {
		t.Fatalf("check short stdout exit = %d", code)
	}

	contains(t, env.Stderr.(*bytes.Buffer).String(), io.ErrShortWrite.Error())
}

// TestCertificatePreflightBothOutputs preserves the existing key and leaves no
// certificate when the second destination collides. No first-write rollback is
// needed because both names must pass preflight before generation or creation.
func TestCertificatePreflightBothOutputs(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	path := filepath.Join(env.Dir, "key.pem")

	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "cert.pem", "--key", "key.pem"}, env)

	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "cert.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first output created: %v", err)
	}

	content, err := os.ReadFile(path)

	if err != nil || string(content) != "existing" {
		t.Fatalf("existing key changed: %v", err)
	}
}

// TestCertificatePreflightExistingCertificateDoesNotCreateKey covers collisions
// on the first destination without creating the second output as a side effect.
func TestCertificatePreflightExistingCertificateDoesNotCreateKey(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.WriteFile(filepath.Join(env.Dir, "cert.pem"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "cert.pem", "--key", "key.pem"}, env)

	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "key.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private output created: %v", err)
	}
}

// TestCertificateRefusesDanglingLinks checks both leaves independently. A
// symlink is an existing output entry even when its target does not exist.
func TestCertificateRefusesDanglingLinks(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"cert.pem", "key.pem"} {
		env := scaffoldEnvironment(t)

		if err := os.Symlink("absent", filepath.Join(env.Dir, name)); err != nil {
			t.Fatal(err)
		}

		err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
			"--cert", "cert.pem", "--key", "key.pem"}, env)

		if !errors.Is(err, os.ErrExist) {
			t.Fatalf("dangling link error = %v", err)
		}

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 1 {
			t.Fatalf("link preflight wrote: %v", err)
		}
	}
}

// TestCertificateRefusesLinkedParents prevents redirection through a directory
// alias. Parents are checked for both destinations before either file is made.
func TestCertificateRefusesLinkedParents(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := os.Mkdir(filepath.Join(env.Dir, "real"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("real", filepath.Join(env.Dir, "alias")); err != nil {
		t.Fatal(err)
	}

	err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "cert.pem", "--key", "alias/key.pem"}, env)

	if err == nil {
		t.Fatal("accepted linked parent")
	}

	if _, err := os.Stat(filepath.Join(env.Dir, "cert.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("first output created before parent validation")
	}
}

// TestCertificateRefusesSameDestination checks lexical aliases and case-only
// names before writes, including filesystems whose names are case-insensitive.
func TestCertificateRefusesSameDestination(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"cert.pem", "./cert.pem", "CERT.pem"} {
		env := scaffoldEnvironment(t)

		err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
			"--cert", "cert.pem", "--key", name}, env)

		requireNewUsage(t, err)

		entries, err := os.ReadDir(env.Dir)

		if err != nil || len(entries) != 0 {
			t.Fatalf("same destination wrote: %v", err)
		}
	}
}

// TestCertificateMissingParentPreventsBothWrites never implicitly creates a
// directory or the first output when the second output's directory is missing.
func TestCertificateMissingParentPreventsBothWrites(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	err := runCertificate(t.Context(), []string{"root", "generate", "--name", "Root",
		"--cert", "cert.pem", "--key", "absent/key.pem"}, env)

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent = %v", err)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("preflight wrote: %v", err)
	}
}

// TestCertificateExclusiveCreateAfterPreflight simulates a concurrent writer
// appearing after checks. Exclusive creation preserves that writer's bytes.
func TestCertificateExclusiveCreateAfterPreflight(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	inventory, err := prepareCertificateFiles(t.Context(), certificatePaths{cert: "cert.pem", key: "key.pem"}, env.Dir)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeCertificateFiles(inventory) })

	if err := os.WriteFile(inventory[1].Path, []byte("concurrent"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = writeCertificateFile(t.Context(), inventory[1], []byte("new"))

	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive create = %v", err)
	}

	content, err := os.ReadFile(inventory[1].Path)

	if err != nil || string(content) != "concurrent" {
		t.Fatalf("concurrent file replaced: %v", err)
	}
}

// TestCertificatePartialPairReportsExactPaths injects a second-create failure
// after preflight. The first file remains, the second is not deleted, and the
// error names both absolute paths for manual recovery rather than claiming rollback.
func TestCertificatePartialPairReportsExactPaths(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	inventory, err := prepareCertificateFiles(t.Context(), certificatePaths{cert: "cert.pem", key: "key.pem"}, env.Dir)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeCertificateFiles(inventory) })

	identity, err := certificate.GenerateRoot("Root")

	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(inventory[1].Path, []byte("concurrent"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = persistCertificate(t.Context(), inventory, identity, env)

	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("partial pair = %v", err)
	}

	contains(t, err.Error(), inventory[0].Path, inventory[1].Path, "files may remain")

	content, err := os.ReadFile(inventory[0].Path)

	if err != nil || !bytes.Equal(content, identity.Bundle().PEM()) {
		t.Fatalf("first file absent/changed: %v", err)
	}

	content, err = os.ReadFile(inventory[1].Path)

	if err != nil || string(content) != "concurrent" {
		t.Fatalf("concurrent file replaced/deleted: %v", err)
	}

	if env.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatal("partial pair reported success")
	}
}

// TestCertificateCanceledAfterPreflight keeps cancellation at the persistence
// boundary explicit, even if generation and both output checks already succeeded.
func TestCertificateCanceledAfterPreflight(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	inventory, err := prepareCertificateFiles(t.Context(), certificatePaths{cert: "cert.pem", key: "key.pem"}, env.Dir)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeCertificateFiles(inventory) })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := writeCertificateFile(ctx, inventory[0], []byte("PEM")); !errors.Is(err, context.Canceled) {
		t.Fatalf("persistence cancellation = %v", err)
	}

	if _, err := os.Stat(inventory[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled write created file")
	}
}

// TestCertificateCommandsProvisionVerifiedHTTPS connects CLI provisioning,
// independent certificate loading, and both HTTP transport integration points.
// Trust stays inside the client's private pool, never the operating-system store.
func TestCertificateCommandsProvisionVerifiedHTTPS(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if code := run(t.Context(), []string{
		"certificate", "root", "generate", "--name", "Integration Development CA",
		"--cert", "root.pem", "--key", "root-key.pem",
	}, env); code != console.ExitOK {
		t.Fatalf("root generation exited %d: %s", code, env.Stderr.(*bytes.Buffer).String())
	}

	if code := run(t.Context(), []string{
		"certificate", "generate", "--ca-cert", "root.pem", "--ca-key", "root-key.pem",
		"--dns", "localhost", "--ip", "127.0.0.1",
		"--cert", "server.pem", "--key", "server-key.pem",
	}, env); code != console.ExitOK {
		t.Fatalf("server generation exited %d: %s", code, env.Stderr.(*bytes.Buffer).String())
	}

	identity, err := certificate.LoadIdentity(
		filepath.Join(env.Dir, "server.pem"), filepath.Join(env.Dir, "server-key.pem"),
	)

	if err != nil {
		t.Fatal(err)
	}

	native, err := identity.TLS()

	if err != nil {
		t.Fatal(err)
	}

	bundle, err := certificate.LoadBundle(filepath.Join(env.Dir, "root.pem"))

	if err != nil {
		t.Fatal(err)
	}

	roots, err := bundle.Pool()

	if err != nil {
		t.Fatal(err)
	}

	handler := func(out http.ResponseWriter, request *http.Request) {
		if request.TLS == nil {
			http.Error(out, "TLS required", http.StatusInternalServerError)

			return
		}

		if _, err := io.WriteString(out, "secure"); err != nil {
			t.Errorf("write HTTPS response: %v", err)
		}
	}

	srv := server.New(http.HandlerFunc(handler), func(listener *server.Builder) {
		listener.Addr("127.0.0.1:0")
		listener.TLS(&tls.Config{Certificates: []tls.Certificate{native}})
		listener.ShutdownTimeout(time.Second)
	})

	if err := srv.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("stop HTTPS server: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("HTTPS server did not stop")
		}
	})

	transport, err := client.NewTransport(&tls.Config{RootCAs: roots})

	if err != nil {
		t.Fatal(err)
	}

	outgoing := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(outgoing.CloseIdleConnections)

	response, err := outgoing.Get("https://" + srv.Addr())

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close HTTPS response: %v", err)
		}
	})

	body, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK || string(body) != "secure" {
		t.Fatalf("HTTPS response = %d %q", response.StatusCode, body)
	}

	if response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		t.Fatal("HTTPS response has no verified certificate chain")
	}
}

// trustTestCertificate issues a fast self-signed P-256 CA in a test directory.
// The optional edit runs before signing so tests can exercise invalid roots
// without relying on malformed DER or changing the production clock.
func trustTestCertificate(t *testing.T, edit func(*x509.Certificate)) string {
	t.Helper()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Independent test root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	if edit != nil {
		edit(template)
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)

	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "root.pem")

	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// TestCertificateTrustArgumentsBeforeFile checks flag-first grammar.
func TestCertificateTrustArgumentsBeforeFile(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, func(root *x509.Certificate) {
		root.NotAfter = time.Now().Add(-time.Minute)
	})

	err := runCertificate(t.Context(), []string{"root", "trust", "--yes", path}, scaffoldEnvironment(t))

	if err == nil || !strings.Contains(err.Error(), "outside its validity period") {
		t.Fatalf("expected certificate validation after parsing, got %v", err)
	}
}

// TestCertificateTrustArgumentsAfterFile checks flag-last grammar.
func TestCertificateTrustArgumentsAfterFile(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, func(root *x509.Certificate) {
		root.NotAfter = time.Now().Add(-time.Minute)
	})

	err := runCertificate(t.Context(), []string{"root", "trust", path, "--yes"}, scaffoldEnvironment(t))

	if err == nil || !strings.Contains(err.Error(), "outside its validity period") {
		t.Fatalf("expected certificate validation after parsing, got %v", err)
	}
}

// TestCertificateTrustArgumentsRejectInvalidForms covers inputs that all
// require usage errors without any certificate I/O or native backend calls.
func TestCertificateTrustArgumentsRejectInvalidForms(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		nil, {"--yes"}, {"a.pem", "b.pem"}, {"--yes", "--yes", "a.pem"},
		{"a.pem", "--yes=true"}, {"a.pem", "--yes=false"}, {"a.pem", "-yes"},
		{"a.pem", "-yes=true"}, {"a.pem", "--user"}, {""}, {"--no"},
	} {
		err := runCertificate(t.Context(), append([]string{"root", "trust"}, args...), scaffoldEnvironment(t))

		var failure *console.ExitError

		if !errors.As(err, &failure) || failure.Code != console.ExitUsage {
			t.Fatalf("args %q: expected usage failure, got %v", args, err)
		}
	}
}

// TestCertificateUntrustRejectsValueBearingApproval keeps removal's approval
// grammar as strict as installation without touching any native trust store.
func TestCertificateUntrustRejectsValueBearingApproval(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"root", "untrust", "missing.pem", "--yes=true"},
		{"root", "untrust", "missing.pem", "--yes=false"},
		{"root", "untrust", "missing.pem", "--yes", "--yes"},
		{"root", "untrust", "missing.pem", "-yes"},
	} {
		requireNewUsage(t, runCertificate(t.Context(), args, scaffoldEnvironment(t)))
	}
}

// TestCertificateTrustRelativePathUsesEnvironmentDir rejects an expired root
// after resolution, so this routing test never reaches production preflight or
// the native system backend even on an unsupported host layout.
func TestCertificateTrustRelativePathUsesEnvironmentDir(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, func(root *x509.Certificate) {
		root.NotAfter = time.Now().Add(-time.Minute)
	})

	err := runCertificate(t.Context(), []string{"root", "trust", filepath.Base(path), "--yes"}, console.Environment{
		Dir:    filepath.Dir(path),
		Stdout: io.Discard,
		Stderr: io.Discard,
	})

	if err == nil || !strings.Contains(err.Error(), "outside its validity period") {
		t.Fatalf("relative path was not resolved before validation: %v", err)
	}
}
