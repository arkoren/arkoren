// Package scaffold owns the CLI offline application scaffold planning.
package scaffold

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/cmd/arkoren/internal/skill"
)

// Options is the complete, local-only scaffold request. folder becomes an
// absolute destination before rendering.
type Options struct {
	// Folder selects the destination, relative to environment.Dir by default.
	Folder string
	// Module is the Go import prefix; an omitted value uses the folder basename.
	Module string
	// SkillsDir is the project-relative parent for the bundled agent skill.
	SkillsDir string
	// NoAgentFiles omits both the skill installation and AGENTS.md.
	NoAgentFiles bool
	// DryRun reports the plan and collisions without prompts or writes.
	DryRun bool
	// Yes approves nonempty directories but never permits replacing a file.
	Yes bool
}

// Create plans and validates every emitted byte before prompting or writing.
// It uses no inspector settings, subprocesses, downloads, or project state.
// Options carry parsed flags; help and argument counts belong to the CLI tree.
// The caller supplies templates and the authored skill tree as read-only filesystems.
func Create(ctx context.Context, options Options, env console.Environment, templates fs.FS, source fs.FS) error {
	if err := validateNewOptions(options); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := resolveNew(&options, env.Dir); err != nil {
		return err
	}

	inventory, err := Render(ctx, options, templates, source)

	if err != nil {
		return err
	}

	return Execute(ctx, options, inventory, env)
}

// Execute checks the filesystem plan, obtains approval when needed, and
// commits the rendered files. Conflicts are reported before any prompt.
func Execute(ctx context.Context, options Options, inventory []files.File, env console.Environment) error {
	conflicts, err := Inspect(ctx, options.Folder, inventory)

	if err != nil {
		return err
	}

	if options.DryRun {
		if err := printNewPlan(env, options, inventory, conflicts); err != nil {
			return err
		}
	}

	if len(conflicts) != 0 {
		if options.DryRun {
			return &console.ExitError{Code: console.ExitUsage}
		}

		return console.ConflictingPaths("create project", options.Folder, conflicts)
	}

	if options.DryRun {
		return nil
	}

	if err := confirmNew(ctx, options, env); err != nil {
		return err
	}

	if err := files.Write(ctx, options.Folder, inventory); err != nil {
		return err
	}

	report := map[string]any{
		"created": true, "destination": options.Folder, "module": options.Module, "file_count": len(inventory),
	}

	if env.Presentation.Verbose {
		report["files"] = files.Paths(inventory)
	}

	return env.Result(report)
}

// validateNewOptions rejects missing destinations and unsafe agent paths
// before rendering or filesystem access. It does not normalize user input.
func validateNewOptions(options Options) error {
	if options.Folder == "" {
		return console.UsageError("new requires a destination (use . for the current directory); usage: arkoren new DIR [flags]")
	}

	if options.NoAgentFiles && options.SkillsDir != skill.DefaultSkillsDir {
		return console.UsageError("--skills-dir cannot be combined with --no-agent-files")
	}

	if !fs.ValidPath(options.SkillsDir) || !filepath.IsLocal(options.SkillsDir) ||
		strings.IndexFunc(options.SkillsDir, func(character rune) bool {
			return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-/", character)
		}) >= 0 {
		return console.UsageError("--skills-dir must be a project-relative slash-separated directory")
	}

	return nil
}

// resolveNew normalizes paths without creating them. Resolving the working
// directory permits platform aliases such as macOS /var; user-selected symlink
// destinations and descendants are still rejected by inspectNew.
func resolveNew(options *Options, dir string) error {
	if options.Folder == "" {
		return console.UsageError("destination must not be empty")
	}

	base, err := filepath.EvalSymlinks(dir)

	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	base, err = filepath.Abs(base)

	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	folder := options.Folder

	if !filepath.IsAbs(folder) {
		folder = filepath.Join(base, folder)
	}

	options.Folder = filepath.Clean(folder)
	name := filepath.Base(options.Folder)

	if err := validateNewElement(name); err != nil {
		return console.UsageError("destination basename: %v", err)
	}

	if options.Module == "" {
		options.Module = name
	}

	return ValidateNewModule(options.Module)
}

// confirmNew requires a terminal and explicit approval only for a nonempty
// destination. EOF, an empty line, and all answers except y/yes decline safely.
func confirmNew(ctx context.Context, options Options, env console.Environment) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	entries, err := os.ReadDir(options.Folder)

	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("inspect destination: %w", err)
	}

	if len(entries) == 0 || options.Yes {
		return nil
	}

	if !env.InputTerminal {
		return console.UsageError("destination is not empty; review it and pass --yes to approve without a terminal")
	}

	return askNew(ctx, options.Folder, env)
}

// askNew writes the complete prompt before starting an approval reader. Failed
// or short prompt writes return an error without consuming caller-owned stdin.
// It waits for one approval line or context cancellation without closing stdin.
// A generic io.Reader cannot be interrupted: on cancellation its read goroutine
// can live until input finishes or the CLI process exits.
// The buffered result lets that goroutine exit without a waiting receiver.
// Embedding callers that own an interruptible reader must release it themselves;
// tests close their owned pipe and wait for all goroutines to finish.
func askNew(ctx context.Context, folder string, env console.Environment) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// output normalizes short writes before approval input can be consumed.
	output := &console.Output{Writer: env.Stderr}
	_, err := fmt.Fprintf(output, "Destination %s is not empty (including hidden files). Continue? [y/N] ", folder)

	if err != nil {
		return fmt.Errorf("write confirmation prompt: %w", err)
	}

	result := make(chan error, 1)

	go func() {
		result <- readNewApproval(env.Stdin)
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

// readNewApproval consumes one confirmation line using the conservative y/yes
// rule. EOF, including an unterminated yes, never grants approval. It may block
// until the supplied reader produces input; askNew owns cancellation of the
// wait, not cancellation or closure of the caller's reader.
func readNewApproval(input io.Reader) error {
	answer, err := bufio.NewReader(input).ReadString('\n')

	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read confirmation: %w", err)
	}

	answer = strings.ToLower(strings.TrimSpace(answer))

	if errors.Is(err, io.EOF) || (answer != "y" && answer != "yes") {
		return console.UsageError("scaffolding canceled; no files were written")
	}

	return nil
}
