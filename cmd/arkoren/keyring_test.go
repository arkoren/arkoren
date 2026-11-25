package main

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/filesecret"
	"arkoren.dev/core/service/secret/secretmux"
	"arkoren.dev/foundation/key"
)

// newRing returns a ring of count independent keys, current first.
func newRing(t *testing.T, count int) key.Ring[key.Shared] {
	t.Helper()

	var keys []key.Shared

	for range count {
		secret, err := key.GenerateShared()

		if err != nil {
			t.Fatal(err)
		}

		keys = append(keys, secret)
	}

	ring, err := key.NewRing(keys[0], keys[1:]...)

	if err != nil {
		t.Fatal(err)
	}

	return ring
}

// writeRingFile writes ring as FILE in the environment's directory with the
// given mode and returns its path.
func writeRingFile(t *testing.T, env console.Environment, name string, ring key.Ring[key.Shared], mode os.FileMode) string {
	t.Helper()

	path := filepath.Join(env.Dir, name)

	if err := os.WriteFile(path, []byte(ring.Encode()+"\n"), mode); err != nil {
		t.Fatal(err)
	}

	return path
}

// readRingFile parses the ring stored in path.
func readRingFile(t *testing.T, path string) key.Ring[key.Shared] {
	t.Helper()

	data, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	ring, err := key.ParseRing[key.Shared](string(data))

	if err != nil {
		t.Fatal(err)
	}

	return ring
}

// fingerprints lists the fingerprints of ring, current first.
func fingerprints(ring key.Ring[key.Shared]) []string {
	var list []string

	for _, entry := range ringEntries(ring) {
		list = append(list, entry.Fingerprint)
	}

	return list
}

// requireInvalidInput checks that err is the invalid_input failure and that its
// message never contains secret.
func requireInvalidInput(t *testing.T, err error, secret string) {
	t.Helper()

	failure, ok := errors.AsType[*console.ExitError](err)

	if !ok || failure.Type != console.TypeInvalidInput {
		t.Fatalf("error = %v, want invalid_input", err)
	}

	if secret != "" && strings.Contains(err.Error(), secret) {
		t.Fatal("error repeats key material")
	}
}

// TestKeyGenerateSharedWritesOneKeyRing proves a generated shared key is a
// single line that decodes as a one-key ring.
func TestKeyGenerateSharedWritesOneKeyRing(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runKey(t.Context(), []string{"generate", "shared", "--out", "link-key"}, env); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(env.Dir, "link-key"))

	if err != nil {
		t.Fatal(err)
	}

	if strings.Count(string(data), "\n") != 1 || !strings.HasSuffix(string(data), "\n") || strings.Contains(string(data), ",") {
		t.Fatalf("not a single-line one-key ring: %q", data)
	}

	if ring := readRingFile(t, filepath.Join(env.Dir, "link-key")); ring.Len() != 1 {
		t.Fatalf("ring length = %d", ring.Len())
	}
}

// TestGeneratedRingResolvesThroughSecretBinding decodes a file written by
// `key generate` and rotated by `key rotate` into a key.Ring through the same
// secret store, binding and file source the generated Secrets provider builds.
func TestGeneratedRingResolvesThroughSecretBinding(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	if err := runKey(t.Context(), []string{"generate", "shared", "--out", ".secrets/link-key"}, env); err != nil {
		t.Fatal(err)
	}

	if err := runKey(t.Context(), []string{"rotate", ".secrets/link-key"}, env); err != nil {
		t.Fatal(err)
	}

	sources, err := secretmux.New(func(sources *secretmux.Builder) {
		sources.Source("file", filesecret.New())
	})

	if err != nil {
		t.Fatal(err)
	}

	secrets := secret.New(sources, func(secrets *secret.Builder) {
		secrets.Bindings(func(name string) (string, bool) {
			return "file:" + filepath.Join(env.Dir, ".secrets/link-key"), name == "LINK_KEY"
		})
	})

	ring, err := secrets.Get(t.Context(), secret.NewBinding[key.Ring[key.Shared]]("LINK_KEY"))

	if err != nil || ring.Len() != 2 || ring.Current().Fingerprint() != readRingFile(t, filepath.Join(env.Dir, ".secrets/link-key")).Current().Fingerprint() {
		t.Fatalf("ring = %v, %v", ring, err)
	}
}

// TestKeyCheckListsFingerprints reports every key of a ring by full
// fingerprint, marks the first as current and never prints key material.
func TestKeyCheckListsFingerprints(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 2)
	writeRingFile(t, env, "ring", ring, 0o600)

	if err := runKey(t.Context(), []string{"check", "shared", "ring"}, env); err != nil {
		t.Fatal(err)
	}

	output := env.Stdout.(*bytes.Buffer).String()

	var document struct {
		Summary string     `json:"summary"`
		OK      bool       `json:"ok"`
		Keys    []keyEntry `json:"keys"`
	}

	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatal(err)
	}

	want := fingerprints(ring)

	if document.Summary != "Valid shared key ring" || !document.OK || len(document.Keys) != 2 ||
		document.Keys[0] != (keyEntry{Fingerprint: want[0], Current: true}) || document.Keys[1] != (keyEntry{Fingerprint: want[1]}) {
		t.Fatalf("unexpected report: %s", output)
	}

	if strings.Contains(output, ring.Encode()) || strings.Contains(output, ring.Current().Base64()) {
		t.Fatal("check printed key material")
	}
}

// TestKeyCheckTextShortensFingerprints shows the terminal presentation: a
// headline and a table of 12-digit fingerprints, the current one marked.
func TestKeyCheckTextShortensFingerprints(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	writeRingFile(t, env, "ring", newRing(t, 2), 0o600)

	got := terminalText(t, env, "key", "check", "shared", "ring")

	want := "✓ Valid shared key ring\n\nKind  shared\nPath  ring\n\nKeys\n  FINGERPRINT   CURRENT\n  REV  true\n  REV\n"

	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestKeyCheckReadsStdin accepts a ring piped in from a secrets manager.
func TestKeyCheckReadsStdin(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Stdin = strings.NewReader(newRing(t, 3).Encode() + "\n")

	if err := runKey(t.Context(), []string{"check", "shared", "-"}, env); err != nil {
		t.Fatal(err)
	}

	output := env.Stdout.(*bytes.Buffer).String()

	if !strings.Contains(output, `"path": "-"`) || strings.Count(output, `"fingerprint"`) != 3 {
		t.Fatalf("unexpected report: %s", output)
	}
}

// TestKeyCheckReportsInvalidRingWithoutKeys fails with exit 1 and a problem for
// text that is not a ring, with no keys listed.
func TestKeyCheckReportsInvalidRingWithoutKeys(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 1)
	writeProjectFile(t, env, "ring", ring.Encode()+","+ring.Encode()+"\n")

	err := runKey(t.Context(), []string{"check", "shared", "ring"}, env)
	exit, failed := errors.AsType[*console.ExitError](err)
	output := env.Stdout.(*bytes.Buffer).String()

	if !failed || exit.Code != console.ExitFailure || !strings.Contains(output, `"ok": false`) || strings.Contains(output, `"keys"`) {
		t.Fatalf("duplicate keys accepted: %v\n%s", err, output)
	}
}

// TestKeyRotateFilePutsNewKeyFirst makes the new key current, keeps the old
// current as previous, rewrites the file with mode 0600 and reports the edit.
func TestKeyRotateFilePutsNewKeyFirst(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 1)
	path := writeRingFile(t, env, "ring", ring, 0o644)

	if err := runKey(t.Context(), []string{"rotate", "ring"}, env); err != nil {
		t.Fatal(err)
	}

	rotated := readRingFile(t, path)

	if rotated.Len() != 2 || rotated.Current().Fingerprint() == ring.Current().Fingerprint() ||
		rotated.Previous()[0].Fingerprint() != ring.Current().Fingerprint() {
		t.Fatalf("unexpected ring: %v", fingerprints(rotated))
	}

	info, err := os.Stat(path)

	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info, err)
	}

	output := env.Stdout.(*bytes.Buffer).String()

	if !strings.Contains(output, `"action": "edit"`) || !strings.Contains(output, `"summary": "Rotated shared key ring"`) ||
		strings.Contains(output, rotated.Encode()) {
		t.Fatalf("unexpected report: %s", output)
	}

	entries, err := os.ReadDir(env.Dir)

	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v, %v", entries, err)
	}
}

// TestKeyRotateStageAppendsPrevious keeps the current key and adds the new key
// at the end, so it can be distributed before promotion.
func TestKeyRotateStageAppendsPrevious(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 2)
	path := writeRingFile(t, env, "ring", ring, 0o600)

	if err := runKey(t.Context(), []string{"rotate", "--stage", "ring"}, env); err != nil {
		t.Fatal(err)
	}

	staged := readRingFile(t, path)
	before := fingerprints(ring)
	after := fingerprints(staged)

	if len(after) != 3 || after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("staging reordered the ring: %v", after)
	}

	if !strings.Contains(env.Stdout.(*bytes.Buffer).String(), "arkoren key promote "+after[2][:12]+" ring") {
		t.Fatalf("report lacks the promote step: %s", env.Stdout)
	}
}

// TestKeyRotateStdinWritesRingToStdout pipes a ring through: stdout carries only
// the new ring, stderr the report, and no file is created.
func TestKeyRotateStdinWritesRingToStdout(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 1)
	env.Stdin = strings.NewReader(ring.Encode())

	if err := runKey(t.Context(), []string{"rotate", "-"}, env); err != nil {
		t.Fatal(err)
	}

	rotated, err := key.ParseRing[key.Shared](env.Stdout.(*bytes.Buffer).String())

	if err != nil || rotated.Len() != 2 || rotated.Previous()[0].Fingerprint() != ring.Current().Fingerprint() {
		t.Fatalf("stdout is not the rotated ring: %v", err)
	}

	diagnostic := env.Stderr.(*bytes.Buffer).String()

	if !strings.Contains(diagnostic, `"summary": "Rotated shared key ring"`) || strings.Contains(diagnostic, rotated.Encode()) ||
		strings.Contains(diagnostic, `"changes"`) {
		t.Fatalf("unexpected diagnostic: %s", diagnostic)
	}

	if entries, err := os.ReadDir(env.Dir); err != nil || len(entries) != 0 {
		t.Fatalf("stdin mode wrote files: %v, %v", entries, err)
	}
}

// TestKeyRotateInvalidRingKeepsFile refuses text that is not a ring, leaves the
// file untouched and never echoes it.
func TestKeyRotateInvalidRingKeepsFile(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	writeProjectFile(t, env, "ring", "not-a-key-but-secret-looking\n")

	err := runKey(t.Context(), []string{"rotate", "ring"}, env)

	requireInvalidInput(t, err, "not-a-key-but-secret-looking")

	contents, readErr := os.ReadFile(filepath.Join(env.Dir, "ring"))

	if readErr != nil || string(contents) != "not-a-key-but-secret-looking\n" {
		t.Fatal("invalid ring file was changed")
	}
}

// TestKeyRotateMissingFile reports a file the user named but does not exist as
// invalid input, and creates nothing.
func TestKeyRotateMissingFile(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	requireInvalidInput(t, runKey(t.Context(), []string{"rotate", "missing"}, env), "")

	if entries, err := os.ReadDir(env.Dir); err != nil || len(entries) != 0 {
		t.Fatalf("files created: %v, %v", entries, err)
	}
}

// TestKeyRotateFollowsSymlink rewrites the link's target and keeps the link.
func TestKeyRotateFollowsSymlink(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	target := writeRingFile(t, env, "target", newRing(t, 1), 0o600)
	link := filepath.Join(env.Dir, "link")

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := runKey(t.Context(), []string{"rotate", "link"}, env); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v, %v", info, err)
	}

	if readRingFile(t, target).Len() != 2 {
		t.Fatal("target not rotated")
	}
}

// TestKeyRewriteLeavesFileOnFailure keeps the old ring when the replacement
// cannot be written: a read-only directory refuses the temporary file.
func TestKeyRewriteLeavesFileOnFailure(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	env := scaffoldEnvironment(t)
	ring := newRing(t, 1)
	dir := filepath.Join(env.Dir, "locked")

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "ring")

	if err := os.WriteFile(path, []byte(ring.Encode()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // Restore access so the temporary directory can be removed.

	if err := rewriteKeyFile(path, []byte("replacement\n")); err == nil {
		t.Fatal("rewrite succeeded in a read-only directory")
	}

	if readRingFile(t, path).Current().Fingerprint() != ring.Current().Fingerprint() {
		t.Fatal("old ring changed after failed rewrite")
	}
}

// TestKeyPromoteMovesKeyToFront accepts the 12-digit prefix, keeps the rest in
// order and is idempotent for the current key.
func TestKeyPromoteMovesKeyToFront(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 3)
	path := writeRingFile(t, env, "ring", ring, 0o600)
	before := fingerprints(ring)

	if err := runKey(t.Context(), []string{"promote", before[2][:12], "ring"}, env); err != nil {
		t.Fatal(err)
	}

	after := fingerprints(readRingFile(t, path))

	if after[0] != before[2] || after[1] != before[0] || after[2] != before[1] {
		t.Fatalf("order = %v, want %v first", after, before[2])
	}

	if err := runKey(t.Context(), []string{"promote", strings.ToUpper(before[2]), "ring"}, env); err != nil {
		t.Fatal(err)
	}

	if fingerprints(readRingFile(t, path))[0] != before[2] {
		t.Fatal("promoting the current key changed the ring")
	}
}

// TestKeyPromoteRejectsBadPrefixes refuses a prefix that is too short or
// matches nothing, without changing the file.
func TestKeyPromoteRejectsBadPrefixes(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 2)
	path := writeRingFile(t, env, "ring", ring, 0o600)
	before := fingerprints(ring)

	for _, prefix := range []string{before[1][:11], strings.Repeat("0", 12)} {
		requireInvalidInput(t, runKey(t.Context(), []string{"promote", prefix, "ring"}, env), "")
	}

	if after := fingerprints(readRingFile(t, path)); after[0] != before[0] || after[1] != before[1] {
		t.Fatal("rejected promote changed the ring")
	}
}

// TestKeyPromoteStdin promotes through a pipe: the new ring is on stdout.
func TestKeyPromoteStdin(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 2)
	before := fingerprints(ring)
	env.Stdin = strings.NewReader(ring.Encode() + "\n")

	if err := runKey(t.Context(), []string{"promote", before[1], "-"}, env); err != nil {
		t.Fatal(err)
	}

	promoted, err := key.ParseRing[key.Shared](env.Stdout.(*bytes.Buffer).String())

	if err != nil || fingerprints(promoted)[0] != before[1] {
		t.Fatalf("stdout ring = %v, %v", promoted, err)
	}
}

// TestKeyRetireRemovesPreviousKey drops the selected previous key only.
func TestKeyRetireRemovesPreviousKey(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	ring := newRing(t, 3)
	path := writeRingFile(t, env, "ring", ring, 0o600)
	before := fingerprints(ring)

	if err := runKey(t.Context(), []string{"retire", before[1][:12], "ring"}, env); err != nil {
		t.Fatal(err)
	}

	after := fingerprints(readRingFile(t, path))

	if len(after) != 2 || after[0] != before[0] || after[1] != before[2] {
		t.Fatalf("ring = %v", after)
	}
}

// TestKeyRetireRefusesCurrentKey keeps the current key, and with it the last
// key of a one-key ring.
func TestKeyRetireRefusesCurrentKey(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	two := newRing(t, 2)
	writeRingFile(t, env, "two", two, 0o600)
	one := newRing(t, 1)
	writeRingFile(t, env, "one", one, 0o600)

	requireInvalidInput(t, runKey(t.Context(), []string{"retire", fingerprints(two)[0], "two"}, env), "")
	requireInvalidInput(t, runKey(t.Context(), []string{"retire", fingerprints(one)[0], "one"}, env), "")

	if readRingFile(t, filepath.Join(env.Dir, "two")).Len() != 2 || readRingFile(t, filepath.Join(env.Dir, "one")).Len() != 1 {
		t.Fatal("refused retire changed a ring")
	}
}

// TestKeyRingCommandsRequireArguments reports missing and surplus positionals
// as usage errors before reading anything.
func TestKeyRingCommandsRequireArguments(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)

	requireNewUsage(t, runKey(t.Context(), []string{"rotate"}, env))
	requireNewUsage(t, runKey(t.Context(), []string{"rotate", "a", "b"}, env))
	requireNewUsage(t, runKey(t.Context(), []string{"promote", "abc"}, env))
	requireNewUsage(t, runKey(t.Context(), []string{"retire"}, env))
}

// TestKeyRingOutputFailureStopsStdinMode reports a failed stdout write instead
// of printing the diagnostic as if the ring were delivered.
func TestKeyRingOutputFailureStopsStdinMode(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.Stdin = strings.NewReader(newRing(t, 1).Encode())
	env.Stdout = failingWriter{}

	if err := runKey(t.Context(), []string{"rotate", "-"}, env); err == nil {
		t.Fatal("write failure ignored")
	}

	if env.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("diagnostic printed after failed delivery")
	}
}

// failingWriter fails every write.
type failingWriter struct{}

// Write always fails.
func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("closed pipe")
}
