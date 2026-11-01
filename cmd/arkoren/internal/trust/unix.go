//go:build !windows

package trust

import (
	"context"
	"fmt"
	"os"
	"runtime"
)

// unsupportedCertificateTrust fails explicitly on platforms without a native
// implementation rather than approximating trust by writing arbitrary files.
type unsupportedCertificateTrust struct{}

// Target rejects unsupported platforms before a warning or approval prompt.
// No fallback store is opened or inferred from a familiar-looking directory.
//
// Example:
//
//	if _, err := backend.Target(); err != nil {
//		return err // Fail before asking the user to approve an operation.
//	}
func (backend *unsupportedCertificateTrust) Target() (string, error) {
	return "", fmt.Errorf("system certificate trust is unsupported on %s", runtime.GOOS)
}

// Apply never mutates a store. The caller still gets an actionable OS name.
//
// Example:
//
//	if err := backend.Apply(ctx, root, "trust"); err != nil {
//		return err // No fallback to user-scope trust.
//	}
func (backend *unsupportedCertificateTrust) Apply(context.Context, Certificate, string) error {
	return fmt.Errorf("system certificate trust is unsupported on %s", runtime.GOOS)
}

// NewBackend wires native effects only at the CLI boundary;
// deterministic tests construct backends with fake execution instead.
func NewBackend() Backend {
	switch runtime.GOOS {
	case "darwin":
		return &macCertificateTrust{
			command:            executeTrustCommand,
			temporaryDirectory: "/private/var/tmp",
			elevated: func() bool {
				return os.Geteuid() == 0
			},
		}
	case "linux":
		return &linuxCertificateTrust{
			layouts: supportedLinuxTrustLayouts(),
			command: executeTrustCommand,
			open:    os.OpenRoot,
		}
	default:
		return &unsupportedCertificateTrust{}
	}
}
