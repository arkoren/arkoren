//go:build !windows

package trust

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// TestCertificateTrustUnsupportedBackendFailsBeforePrompt exercises the actual
// unsupported-platform backend without relying on the host OS's supported list.
// Nil stdin would fail if approval were attempted; no store handle is acquired.
func TestCertificateTrustUnsupportedBackendFailsBeforePrompt(t *testing.T) {
	t.Parallel()

	var warning bytes.Buffer

	err := Execute(context.Background(), Request{
		Path: trustTestCertificate(t, nil), Action: "trust",
	}, console.Environment{Stdout: io.Discard, Stderr: &warning, InputTerminal: true}, &unsupportedCertificateTrust{})

	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported-platform preflight error, got %v", err)
	}

	if warning.Len() != 0 {
		t.Fatalf("unsupported backend prompted for approval: %q", warning.String())
	}
}
