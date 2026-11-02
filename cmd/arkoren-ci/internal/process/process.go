// Package process runs bounded maintenance subprocesses without ambient secrets.
package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Environment retains runtime paths, not Git overrides or credentials.
// readToken explicitly grants gh its metadata-read credential.
func Environment(readToken bool) []string {
	result := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GH_PROMPT_DISABLED=1"}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "SystemRoot", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(name); ok {
			result = append(result, name+"="+value)
		}
	}
	if readToken {
		for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
			if value, ok := os.LookupEnv(name); ok {
				result = append(result, name+"="+value)
			}
		}
	}

	return result
}

// Run executes in root with a timeout. Errors exclude child output and arguments.
func Run(ctx context.Context, root, binary, input string, readToken bool, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	return Execute(ctx, root, Environment(readToken), binary, input, args...)
}

// Execute runs a credential-scoped operation with caller-owned cancellation.
// Diagnostics never contain arguments, environment values or child output.
func Execute(ctx context.Context, root string, env []string, binary, input string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = root
	command.Env = env
	command.Stdin = strings.NewReader(input)
	data, err := command.Output()

	if err != nil {
		return nil, fmt.Errorf("maintenance subprocess failed: %w", err)
	}

	return data, nil
}
