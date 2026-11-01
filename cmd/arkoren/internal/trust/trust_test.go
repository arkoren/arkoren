package trust

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/cmd/arkoren/internal/console"
)

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

// TestCertificateTrustAcceptsIndependentCA ensures there is no Arkoren-specific
// issuer/name requirement and the backend receives only canonical public PEM.
func TestCertificateTrustAcceptsIndependentCA(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, nil)
	backend := &recordingCertificateTrust{}
	var warning, output bytes.Buffer

	err := Execute(context.Background(), Request{
		Path: path, Yes: true, Action: "trust",
	}, console.Environment{Stdout: &output, Stderr: &warning}, backend)

	if err != nil {
		t.Fatal(err)
	}

	if backend.calls != 1 || backend.action != "trust" {
		t.Fatalf("backend = %+v", backend)
	}

	if !strings.Contains(warning.String(), backend.root.Fingerprint) || len(backend.root.Fingerprint) != 64 {
		t.Fatalf("missing SHA-256 identity: %q", warning.String())
	}

	if !strings.Contains(output.String(), "completed") {
		t.Fatalf("missing completion: %q", output.String())
	}
}

// recordingCertificateTrust is an isolated backend that never invokes a
// system utility. Each test owns its recorded call and optional failure.
type recordingCertificateTrust struct {
	// calls counts attempted mutations, including ones configured to fail.
	calls int

	// root records the exact validated snapshot received by the backend.
	root Certificate

	// action records trust or untrust without inferring it from certificate data.
	action string

	// err is the injected native failure, if any.
	err error

	// target is the concrete fake store description shown in confirmation.
	target string

	// targetErr fails read-only preflight without calling Apply.
	targetErr error
}

// Target describes only the test-owned store and optionally fails preflight.
// For example, backend.Target() returns backend.target without native I/O.
func (backend *recordingCertificateTrust) Target() (string, error) {
	target := backend.target

	if target == "" {
		target = "isolated test root store"
	}

	return target, backend.targetErr
}

// Apply records one request without filesystem or system-store side effects.
// For example, backend.Apply(ctx, root, "trust") increments backend.calls.
func (backend *recordingCertificateTrust) Apply(_ context.Context, root Certificate, action string) error {
	backend.calls++
	backend.root = root
	backend.action = action

	return backend.err
}

// TestCertificateTrustRejectsExpiredInstall checks time at installation only.
func TestCertificateTrustRejectsExpiredInstall(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, func(root *x509.Certificate) {
		root.NotAfter = time.Now().Add(-time.Minute)
	})

	if _, err := Load(path, "trust"); err == nil {
		t.Fatal("expired root accepted for installation")
	}
}

// TestCertificateTrustAllowsExpiredRemoval permits cleaning up old trust.
func TestCertificateTrustAllowsExpiredRemoval(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, func(root *x509.Certificate) {
		root.NotAfter = time.Now().Add(-time.Minute)
	})

	if _, err := Load(path, "untrust"); err != nil {
		t.Fatal(err)
	}
}

// TestCertificateTrustRejectsFutureInstall prevents premature trust.
func TestCertificateTrustRejectsFutureInstall(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, func(root *x509.Certificate) {
		root.NotBefore = time.Now().Add(time.Minute)
	})

	if _, err := Load(path, "trust"); err == nil {
		t.Fatal("future root accepted for installation")
	}
}

// TestCertificateTrustRejectsLeaf prevents upgrading leaf material into a CA.
func TestCertificateTrustRejectsLeaf(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, func(root *x509.Certificate) {
		root.IsCA = false
	})

	if _, err := Load(path, "untrust"); err == nil {
		t.Fatal("leaf accepted as a root")
	}
}

// TestCertificateTrustRequiresSigningUsage rejects CA-marked roots that cannot sign.
func TestCertificateTrustRequiresSigningUsage(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, func(root *x509.Certificate) {
		root.KeyUsage = x509.KeyUsageDigitalSignature
	})

	if _, err := Load(path, "trust"); err == nil {
		t.Fatal("root without certificate-signing usage accepted")
	}
}

// TestCertificateTrustRejectsCertificateBundle requires one explicit root.
func TestCertificateTrustRejectsCertificateBundle(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, nil)
	encoded, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, bytes.Repeat(encoded, 2), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path, "trust"); err == nil {
		t.Fatal("bundle accepted")
	}
}

// TestCertificateTrustRejectsKeyInInput verifies strict public-only loading.
func TestCertificateTrustRejectsKeyInInput(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, nil)
	encoded, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	encoded = append(encoded, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")})...)

	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path, "trust"); err == nil {
		t.Fatal("private-key block accepted")
	}
}

// TestCertificateTrustRequiresNoninteractiveApproval leaves stores untouched
// when --yes was omitted and stdin is not a terminal.
func TestCertificateTrustRequiresNoninteractiveApproval(t *testing.T) {
	t.Parallel()

	backend := &recordingCertificateTrust{}

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust",
	}, console.Environment{Stderr: io.Discard, Stdout: io.Discard, Stdin: strings.NewReader("yes\n")}, backend)

	if err == nil || backend.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, backend.calls)
	}
}

// TestCertificateTrustDefaultsNo ensures a blank terminal answer cannot mutate trust.
func TestCertificateTrustDefaultsNo(t *testing.T) {
	t.Parallel()

	backend := &recordingCertificateTrust{}

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust",
	}, console.Environment{Stderr: io.Discard, Stdout: io.Discard, Stdin: strings.NewReader("\n"), InputTerminal: true}, backend)

	if err == nil || backend.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, backend.calls)
	}
}

// TestCertificateTrustAcceptsTerminalYes exercises explicit interactive approval.
func TestCertificateTrustAcceptsTerminalYes(t *testing.T) {
	t.Parallel()

	backend := &recordingCertificateTrust{}

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "untrust",
	}, console.Environment{Stderr: io.Discard, Stdout: io.Discard, Stdin: strings.NewReader("YeS\n"), InputTerminal: true}, backend)

	if err != nil || backend.calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, backend.calls)
	}
}

// TestCertificateTrustRejectsUnterminatedYes treats EOF conservatively.
func TestCertificateTrustRejectsUnterminatedYes(t *testing.T) {
	t.Parallel()

	if err := ReadCertificateTrustApproval(strings.NewReader("yes")); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF denial, got %v", err)
	}
}

// TestCertificateTrustWarningFailurePreventsMutation preserves stderr errors.
func TestCertificateTrustWarningFailurePreventsMutation(t *testing.T) {
	t.Parallel()

	failure := errors.New("warning write failed")
	backend := &recordingCertificateTrust{}

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust", Yes: true,
	}, console.Environment{Stdout: io.Discard, Stderr: trustTestWriter(func([]byte) (int, error) {
		return 0, failure
	})}, backend)

	if !errors.Is(err, failure) || backend.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, backend.calls)
	}
}

// trustTestWriter makes individual output boundaries observable without
// mutating global streams or invoking production trust backends.
type trustTestWriter func([]byte) (int, error)

// Write delegates to the test-owned callback; callers can inject short writes
// using trustTestWriter(func(p []byte) (int, error) { return 0, nil }).
func (writer trustTestWriter) Write(data []byte) (int, error) {
	return writer(data)
}

// TestCertificateTrustShortWarningPreventsMutation catches writers that omit errors.
func TestCertificateTrustShortWarningPreventsMutation(t *testing.T) {
	t.Parallel()

	backend := &recordingCertificateTrust{}

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust", Yes: true,
	}, console.Environment{Stdout: io.Discard, Stderr: trustTestWriter(func([]byte) (int, error) {
		return 0, nil
	})}, backend)

	if !errors.Is(err, io.ErrShortWrite) || backend.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, backend.calls)
	}
}

// TestCertificateTrustPromptFailurePreventsMutation checks the second stderr write.
func TestCertificateTrustPromptFailurePreventsMutation(t *testing.T) {
	t.Parallel()

	failure := errors.New("prompt write failed")
	calls := 0
	backend := &recordingCertificateTrust{}

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust",
	}, console.Environment{Stdout: io.Discard, InputTerminal: true, Stderr: trustTestWriter(func(data []byte) (int, error) {
		calls++

		if calls == 2 {
			return 0, failure
		}

		return len(data), nil
	})}, backend)

	if !errors.Is(err, failure) || backend.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, backend.calls)
	}
}

// TestCertificateTrustUsesValidatedSnapshot changes the user's file during the
// warning write and proves the privileged backend receives the original bytes.
func TestCertificateTrustUsesValidatedSnapshot(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, nil)
	original, err := Load(path, "trust")

	if err != nil {
		t.Fatal(err)
	}

	backend := &recordingCertificateTrust{}

	err = Execute(context.Background(), Request{
		Path: path, Action: "trust", Yes: true,
	}, console.Environment{Stdout: io.Discard, Stderr: trustTestWriter(func(data []byte) (int, error) {
		if err := os.WriteFile(path, []byte("changed after approval"), 0o600); err != nil {
			return 0, err
		}

		return len(data), nil
	})}, backend)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(backend.root.DER, original.DER) {
		t.Fatal("backend did not receive validated DER snapshot")
	}
}

// TestCertificateTrustCancellationPreventsMutation checks cancellation during
// the warning boundary, including the noninteractive --yes path.
func TestCertificateTrustCancellationPreventsMutation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend := &recordingCertificateTrust{}

	err := Execute(ctx, Request{
		Path: trustTestCertificate(t, nil), Action: "trust", Yes: true,
	}, console.Environment{Stdout: io.Discard, Stderr: trustTestWriter(func(data []byte) (int, error) {
		cancel()

		return len(data), nil
	})}, backend)

	if !errors.Is(err, context.Canceled) || backend.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, backend.calls)
	}
}

// TestCertificateTrustPromptCancellation releases test-owned stdin and joins
// the read goroutine before synctest finishes, without real-time sleeps.
func TestCertificateTrustPromptCancellation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		reader, writer := io.Pipe()
		t.Cleanup(func() {
			_ = reader.Close()
			_ = writer.Close()
		})

		result := make(chan error, 1)

		go func() {
			result <- AwaitCertificateTrustApproval(ctx, reader)
		}()

		synctest.Wait()
		cancel()

		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	})
}

// TestCertificateTrustCompletionFailureIsReported never hides stdout errors
// after the backend succeeded; a write failure cannot reverse an installed root.
func TestCertificateTrustCompletionFailureIsReported(t *testing.T) {
	t.Parallel()

	backend := &recordingCertificateTrust{}

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust", Yes: true,
	}, console.Environment{Stderr: io.Discard, Stdout: trustTestWriter(func([]byte) (int, error) {
		return 0, io.ErrClosedPipe
	})}, backend)

	if !errors.Is(err, io.ErrClosedPipe) || backend.calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, backend.calls)
	}
}

// TestCertificateTrustWarningNamesConcreteTarget makes the selected backend
// store visible before approval, including commands that use explicit --yes.
func TestCertificateTrustWarningNamesConcreteTarget(t *testing.T) {
	t.Parallel()

	backend := &recordingCertificateTrust{target: "/isolated/distribution/anchors"}
	var warning bytes.Buffer

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust", Yes: true,
	}, console.Environment{Stdout: io.Discard, Stderr: &warning}, backend)

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(warning.String(), `"target": "/isolated/distribution/anchors"`) {
		t.Fatalf("missing concrete target: %q", warning.String())
	}
}

// TestCertificateTrustUnsupportedPreflightPreventsPrompt fails before reading
// terminal input or writing an approval warning when no store can be selected.
func TestCertificateTrustUnsupportedPreflightPreventsPrompt(t *testing.T) {
	t.Parallel()

	failure := errors.New("unsupported system trust layout")
	backend := &recordingCertificateTrust{targetErr: failure}
	var warning bytes.Buffer

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust",
	}, console.Environment{Stdout: io.Discard, Stderr: &warning, InputTerminal: true}, backend)

	if !errors.Is(err, failure) || backend.calls != 0 || warning.Len() != 0 {
		t.Fatalf("preflight error = %v, mutations = %d, warning = %q", err, backend.calls, warning.String())
	}
}

// TestCertificateTrustRejectsBadSelfSignature rejects correctly encoded roots
// whose signatures were changed; parsing alone must never authorize trust.
func TestCertificateTrustRejectsBadSelfSignature(t *testing.T) {
	t.Parallel()

	path := trustTestCertificate(t, nil)
	encoded, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(encoded)
	block.Bytes[len(block.Bytes)-1] ^= 1

	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path, "trust"); err == nil {
		t.Fatal("invalid self-signature accepted")
	}
}

// TestCertificateTrustRejectsIntermediate checks issuer identity independently
// of CA/basic-constraints validation and certificate-signing usage.
func TestCertificateTrustRejectsIntermediate(t *testing.T) {
	t.Parallel()

	root, err := Load(trustTestCertificate(t, nil), "trust")

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := x509.ParseCertificate(root.DER)

	if err != nil {
		t.Fatal(err)
	}

	parsed.RawIssuer = []byte("different issuer")

	if err := ValidateTrustRoot(parsed); err == nil {
		t.Fatal("intermediate accepted")
	}
}

// TestCertificateTrustRechecksValidityAfterApproval advances only synctest's
// virtual clock, preventing installation if the root expires during confirmation.
func TestCertificateTrustRechecksValidityAfterApproval(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		backend := &recordingCertificateTrust{}
		err := Execute(context.Background(), Request{
			Path: trustTestCertificate(t, nil), Action: "trust", Yes: true,
		}, console.Environment{Stdout: io.Discard, Stderr: trustTestWriter(func(data []byte) (int, error) {
			time.Sleep(2 * time.Hour)

			return len(data), nil
		})}, backend)

		if err == nil || backend.calls != 0 {
			t.Fatalf("err = %v, calls = %d", err, backend.calls)
		}
	})
}

// TestCertificateTrustBackendFailurePreservesCause reports injected native
// errors rather than printing a successful completion after an unsuccessful change.
func TestCertificateTrustBackendFailurePreservesCause(t *testing.T) {
	t.Parallel()

	failure := errors.New("native store unavailable")
	backend := &recordingCertificateTrust{err: failure}
	var output bytes.Buffer

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust", Yes: true,
	}, console.Environment{Stdout: &output, Stderr: io.Discard}, backend)

	if !errors.Is(err, failure) || output.Len() != 0 {
		t.Fatalf("backend failure = %v, output = %q", err, output.String())
	}
}

// TestTrustWarningsUseStderrTerminalStyle checks the safety warning uses the
// diagnostic stream's terminal policy independently of stdout.
func TestTrustWarningsUseStderrTerminalStyle(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	env.ErrorTerminal = true
	env.Env = func(string) (string, bool) {
		return "", false
	}

	request := Request{Action: "trust", Target: "isolated store", Yes: true}

	if err := ApproveCertificateTrust(t.Context(), request, Certificate{Fingerprint: "fixture"}, env); err != nil {
		t.Fatal(err)
	}

	output := env.Stderr.(*bytes.Buffer).String()

	if !strings.Contains(output, "\x1b[") || !strings.Contains(output, "\n  ") {
		t.Fatalf("warning = %q", output)
	}
}

// scaffoldEnvironment isolates the filesystem and streams for offline tests.
// Env panics deliberately: scaffolding must never read inspector settings.
func scaffoldEnvironment(t *testing.T) console.Environment {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())

	if err != nil {
		t.Fatal(err)
	}

	return console.Environment{
		Dir:    dir,
		Stdin:  strings.NewReader(""),
		Stdout: new(bytes.Buffer),
		Stderr: new(bytes.Buffer),
		Env: func(string) (string, bool) {
			panic("new read process environment")
		},
	}
}

// TestJSONWarningIsMachineReadable preserves structured safety warnings as
// distinct diagnostic records rather than unparseable stderr prose.
func TestJSONWarningIsMachineReadable(t *testing.T) {
	t.Parallel()

	env := scaffoldEnvironment(t)
	request := Request{Action: "trust", Target: "isolated store", Yes: true}
	root := Certificate{Fingerprint: "fixture"}

	if err := ApproveCertificateTrust(t.Context(), request, root, env); err != nil {
		t.Fatal(err)
	}

	var document map[string]map[string]any
	output := env.Stderr.(*bytes.Buffer).String()

	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatal(err)
	}

	if document["warning"]["target"] != "isolated store" || document["warning"]["sha256"] != "fixture" {
		t.Fatalf("warning = %s", output)
	}

	if len(document["warning"]["effects"].([]any)) != 4 {
		t.Fatalf("warning omitted trust scope: %s", output)
	}
}
