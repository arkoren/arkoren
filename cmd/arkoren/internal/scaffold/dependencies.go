package scaffold

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Tidy runs go mod tidy in folder, resolving the module independently of any
// enclosing workspace. `arkoren new` and `arkoren add` call it after writing
// files. Go output is captured so the CLI retains one JSON result or diagnostic.
// Failure preserves the project for an explicit retry rather than deleting it.
func Tidy(ctx context.Context, folder string) error {
	return goCommand(ctx, folder, "go mod tidy", "mod", "tidy")
}

// Get runs go get in folder for each module, given as module@version, so go mod
// tidy afterwards keeps those exact versions instead of resolving the latest
// release. Like [Tidy], it resolves independently of any enclosing workspace,
// captures the Go output and preserves the project when it fails.
//
// Example:
//
//	err := scaffold.Get(ctx, root, []string{"arkoren.dev/ark/iam@v0.6.1"})
func Get(ctx context.Context, folder string, modules []string) error {
	return goCommand(ctx, folder, "go get "+strings.Join(modules, " "), append([]string{"get"}, modules...)...)
}

// goCommand runs the go tool with args in folder, outside any workspace.
// description names the command in the error, which tells the caller how to
// retry by hand.
func goCommand(ctx context.Context, folder, description string, args ...string) error {
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = folder
	command.Env = append(os.Environ(), "GOWORK=off")

	output, err := command.CombinedOutput()

	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}

		return fmt.Errorf("project files created in %s, but dependency setup failed; retry %s in that directory: %w\n%s", folder, description, err, output)
	}

	return nil
}
