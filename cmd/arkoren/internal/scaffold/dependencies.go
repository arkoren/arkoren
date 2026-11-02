package scaffold

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// tidy resolves the generated module independently of any enclosing workspace.
// Go output is captured so the CLI retains one JSON result or diagnostic.
// Failure preserves the project for an explicit retry rather than deleting it.
func tidy(ctx context.Context, folder string) error {
	command := exec.CommandContext(ctx, "go", "mod", "tidy")
	command.Dir = folder
	command.Env = append(os.Environ(), "GOWORK=off")

	output, err := command.CombinedOutput()

	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}

		return fmt.Errorf("project files created in %s, but dependency setup failed; retry go mod tidy in that directory: %w\n%s", folder, err, output)
	}

	return nil
}
