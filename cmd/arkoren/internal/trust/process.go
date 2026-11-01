//go:build !windows

package trust

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// certificateTrustCommand executes one native utility without a shell or
// automatic elevation. Injected commands let host tests inspect exact argv.
type certificateTrustCommand func(context.Context, string, ...string) ([]byte, error)

// executeTrustCommand captures diagnostics rather than allowing a child to
// write unchecked output or read confirmation input. Context errors retain their
// identity; other failures include the utility's bounded diagnostic text.
func executeTrustCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)

	output, err := command.CombinedOutput()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if err != nil {
		return output, fmt.Errorf("%s: %w: %.2048s", name, err, strings.TrimSpace(string(output)))
	}

	return output, nil
}
