package main

import (
	"context"

	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/scaffold"
	"arkoren.dev/cmd/arkoren/internal/skill"
)

// newCLICommand declares the offline scaffold grammar. Each invocation builds
// fresh flag state and reads only the injected environment when its action runs.
// Generated help comes from this same grammar before business validation.
func newCLICommand(env console.Environment) *cli.Command {
	return &cli.Command{
		Name:      "new",
		Usage:     "Create an application you own without downloads or overwrites",
		ArgsUsage: "DIR",
		Description: "No downloads or Git initialization. Existing generated files are never overwritten. " +
			"A nonempty destination requires default-no confirmation or --yes; collisions always fail. " +
			"Includes bundled skills and AGENTS.md unless disabled. Read the generated README for dependency setup.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "module", Usage: "Go module path (default: destination basename)"},
			&cli.StringFlag{Name: "skills-dir", Value: skill.DefaultSkillsDir, Usage: "Project-relative skills parent"},
			&cli.BoolFlag{Name: "no-agent-files", Usage: "Omit skills and AGENTS.md"},
			&cli.BoolFlag{Name: "dry-run", Usage: "Show the plan without writing"},
			&cli.BoolFlag{Name: "yes", Usage: "Approve a nonempty destination, never overwrites"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Len() > 1 {
				return console.UsageError("new requires exactly one destination; usage: arkoren new DIR [flags]")
			}

			return scaffold.Create(ctx, scaffold.Options{
				Folder:       command.Args().First(),
				Module:       command.String("module"),
				SkillsDir:    command.String("skills-dir"),
				NoAgentFiles: command.Bool("no-agent-files"),
				DryRun:       command.Bool("dry-run"),
				Yes:          command.Bool("yes"),
			}, env, newTemplates, skillSource)
		},
	}
}
