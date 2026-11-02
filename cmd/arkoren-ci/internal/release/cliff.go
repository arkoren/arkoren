// Package release prepares read-only independent-module release proposals.
package release

import (
	"context"
	_ "embed"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// tool records the single maintained git-cliff version pin.
//
//go:embed tool.json
var tool []byte

// configuration is executable-owned, never read from the candidate tree.
//
//go:embed cliff.toml
var configuration []byte

// Tool returns the pinned native tool specification for workflow installation.
func Tool() (map[string]string, error) {
	result := map[string]string{}
	err := json.Unmarshal(tool, &result)

	return result, err
}

// cliff owns the candidate cwd and trusted native parser configuration.
type cliff struct {
	// root selects the candidate repository.
	root string
	// binary is an explicit GIT_CLIFF override or PATH executable.
	binary string
	// config selects executable-owned temporary configuration.
	config string
}

// newCliff materializes trusted configuration and verifies the exact version.
func newCliff(ctx context.Context, root string) (cliff, func(), error) {
	directory, err := os.MkdirTemp("", "arkoren-cliff-")

	if err != nil {
		return cliff{}, nil, err
	}
	cleanup := func() {
		// This directory is exclusively owned by this invocation.
		_ = os.RemoveAll(directory)
	}
	config := filepath.Join(directory, "cliff.toml")
	if err := os.WriteFile(config, configuration, 0o600); err != nil {
		cleanup()

		return cliff{}, nil, err
	}
	binary := os.Getenv("GIT_CLIFF")
	if binary == "" {
		binary = "git-cliff"
	}
	parser := cliff{root: root, binary: binary, config: config}
	version, err := parser.run(ctx, "", "--version")

	if err != nil {
		cleanup()

		return cliff{}, nil, err
	}
	pin, err := Tool()

	if err != nil || strings.TrimSpace(version) != "git-cliff "+pin["git_cliff"] {
		cleanup()

		return cliff{}, nil, fmt.Errorf("install the pinned git-cliff version from release tool")
	}

	return parser, cleanup, nil
}

// run captures stdout only and never discloses child stderr.
func (parser cliff) run(ctx context.Context, input string, args ...string) (string, error) {
	options := append([]string{"--config", parser.config, "--offline", "--no-exec"}, args...)
	data, err := process.Run(ctx, parser.root, parser.binary, input, false, options...)

	return string(data), err
}
