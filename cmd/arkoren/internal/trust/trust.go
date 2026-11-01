// Package trust owns the CLI explicit system certificate trust boundary.
package trust

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/foundation/certificate"
)

// Request holds one explicit system-store operation. Its
// path is resolved relative to environment.Dir before any certificate I/O.
type Request struct {
	// Path names a public PEM file, never a private-key or identity file.
	Path string

	// Yes bypasses the interactive default-no approval, not validation.
	Yes bool

	// Action selects installation or removal without changing validation rules.
	Action string

	// Target is resolved by the backend before approval, so the warning names
	// the concrete store that the subsequent operation will change.
	Target string
}

// Certificate is a validated snapshot detached from the user's path.
// Every backend acts on these exact DER bytes, never a subject or serial.
type Certificate struct {
	// DER is the complete certificate encoding used for exact identity checks.
	DER []byte

	// pem contains only the one validated public certificate, canonically encoded.
	pem []byte

	// Fingerprint is its lowercase SHA-256 digest, also used in Linux filenames.
	Fingerprint string

	// notBefore and notAfter retain the validity window for a time-sensitive
	// final check after a potentially long interactive confirmation.
	notBefore, notAfter time.Time
}

// Backend isolates privileged platform effects from approval
// and validation. Tests provide substitutes; production never elevates itself.
type Backend interface {
	// Target resolves the concrete system store using read-only preflight.
	// It opens no store handles and preserves that selection for Apply.
	//
	// Example:
	//
	//	target, err := backend.Target()
	//	if err != nil {
	//		return err
	//	}
	//	fmt.Fprintln(warning, target)
	Target() (string, error)

	// Apply changes only the exact supplied certificate in the system scope.
	// Errors must explain partial changes when refreshing or cleanup fails.
	//
	// Example:
	//
	//	if err := backend.Apply(ctx, root, "trust"); err != nil {
	//		return err
	//	}
	Apply(context.Context, Certificate, string) error
}

// Run resolves the typed public-file selection against the
// injected working directory. Approval and native effects remain behind the
// injectable Execute boundary; production never elevates itself.
func Run(ctx context.Context, request Request, env console.Environment) error {
	if !filepath.IsAbs(request.Path) {
		request.Path = filepath.Join(env.Dir, request.Path)
	}

	return Execute(ctx, request, env, NewBackend())
}

// Load reads the input exactly once through the certificate
// library's strict public-bundle loader. Removal accepts expired/future roots,
// but installation requires current validity as well as a valid self-signature.
func Load(path, action string) (Certificate, error) {
	bundle, err := certificate.LoadBundle(filepath.Clean(path))

	if err != nil {
		return Certificate{}, fmt.Errorf("load public root certificate: %w", err)
	}

	certificates := bundle.Certificates()

	if len(certificates) != 1 {
		return Certificate{}, errors.New("trust requires exactly one public root certificate")
	}

	root := certificates[0]

	if err := ValidateTrustRoot(root); err != nil {
		return Certificate{}, err
	}

	now := time.Now()

	if action == "trust" && (now.Before(root.NotBefore) || now.After(root.NotAfter)) {
		return Certificate{}, errors.New("cannot trust a root outside its validity period")
	}

	return Certificate{
		DER:         bytes.Clone(root.Raw),
		pem:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}),
		Fingerprint: fmt.Sprintf("%x", sha256.Sum256(root.Raw)),
		notBefore:   root.NotBefore,
		notAfter:    root.NotAfter,
	}, nil
}

// ValidateTrustRoot rejects leaves, intermediates, missing key usage, and
// self-issued certificates whose own public key cannot verify the signature.
func ValidateTrustRoot(root *x509.Certificate) error {
	if !root.IsCA || !root.BasicConstraintsValid {
		return errors.New("trust requires a CA certificate with valid basic constraints")
	}

	if root.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("trust requires certificate-signing key usage")
	}

	if !bytes.Equal(root.RawSubject, root.RawIssuer) {
		return errors.New("trust requires a self-signed root, not an intermediate")
	}

	if err := root.CheckSignatureFrom(root); err != nil {
		return fmt.Errorf("verify root self-signature: %w", err)
	}

	return nil
}

// Execute owns the safety boundary shared by every backend.
// The caller supplies a backend so tests cannot accidentally touch native stores.
// Completion output is checked too, though its failure cannot undo a mutation.
func Execute(
	ctx context.Context,
	request Request,
	env console.Environment,
	backend Backend,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	root, err := Load(request.Path, request.Action)

	if err != nil {
		return err
	}

	request.Target, err = backend.Target()

	if err != nil {
		return err
	}

	if err := ApproveCertificateTrust(ctx, request, root, env); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	now := time.Now()
	outsideValidity := now.Before(root.notBefore) || now.After(root.notAfter)

	if request.Action == "trust" && outsideValidity {
		return errors.New("root validity changed while awaiting approval; no trust changes made")
	}

	if err := backend.Apply(ctx, root, request.Action); err != nil {
		return err
	}

	return env.Result(map[string]any{
		"action": request.Action, "completed": true, "sha256": root.Fingerprint, "target": request.Target,
	})
}

// ApproveCertificateTrust writes the identity and scope warning even with
// --yes. Failed/short warning writes always prevent platform mutations.
func ApproveCertificateTrust(
	ctx context.Context,
	request Request,
	root Certificate,
	env console.Environment,
) error {
	output := &console.Output{Writer: env.Stderr}
	err := console.WriteJSON(output, map[string]any{"warning": map[string]any{
		"action": request.Action, "sha256": root.Fingerprint, "target": request.Target,
		"message": "System-wide root trust change.",
		"effects": []string{
			"Trust permits certificates signed by this CA. Removal affects only the selected system store.",
			"On macOS, removal clears admin trust settings but leaves the public System keychain certificate.",
			"On Linux, removal deletes only Arkoren digest-named anchors; other installed copies are untouched.",
			"Browsers, Java, containers, and applications can use separate stores or cached trust.",
		},
	}}, env.DiagnosticStyle())

	if err != nil {
		return err
	}

	if request.Yes {
		return nil
	}

	if !env.InputTerminal {
		return console.UsageError("system-store changes require terminal confirmation or explicit --yes")
	}

	if _, err := fmt.Fprint(output, "Continue? [y/N] "); err != nil {
		return err
	}

	return AwaitCertificateTrustApproval(ctx, env.Stdin)
}

// AwaitCertificateTrustApproval returns promptly on cancellation without
// closing caller-owned stdin. A blocked generic Reader cannot be interrupted;
// its goroutine exits when the owner releases input or the CLI process exits.
// The buffered result never leaves that goroutine blocked on sending.
func AwaitCertificateTrustApproval(ctx context.Context, input io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	result := make(chan error, 1)

	go func() {
		result <- ReadCertificateTrustApproval(input)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return err
	}
}

// ReadCertificateTrustApproval accepts only a complete y/yes line, ignoring
// case and surrounding whitespace. EOF and every other answer default to no.
func ReadCertificateTrustApproval(input io.Reader) error {
	answer, err := bufio.NewReader(input).ReadString('\n')

	if err != nil {
		return fmt.Errorf("confirmation not received; no trust changes made: %w", err)
	}

	answer = strings.ToLower(strings.TrimSpace(answer))

	if answer != "y" && answer != "yes" {
		return errors.New("confirmation declined; no trust changes made")
	}

	return nil
}
