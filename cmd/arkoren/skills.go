package main

import (
	"context"

	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/skill"
)

// skillsCLICommand declares the three offline skill operations. Family flags
// are inherited by default in urfave/cli v3, accepting the skills parent and
// preview choice before or after the selected operation. No environment sources
// are configured, so omitted flags retain the declared defaults.
func skillsCLICommand(env console.Environment) *cli.Command {
	// action translates the selected leaf and typed flags into one local request.
	// Extra positionals fail before bundle inspection or any filesystem change.
	action := func(ctx context.Context, command *cli.Command) error {
		if command.Args().Present() {
			return console.UsageError("too many arguments: %s takes no arguments, only flags", command.FullName())
		}

		return skill.Manage(ctx, skill.Options{
			Operation: command.Name,
			Dir:       command.String("dir"),
			DryRun:    command.Bool("dry-run"),
		}, env, skillSource)
	}

	return &cli.Command{
		Name:  "skills",
		Usage: "Manage the Arkoren documentation bundled with this CLI",
		Description: "Offline: no downloads or AGENTS.md edits. Local modifications and unsafe links block updates; " +
			"unrelated files are preserved. Upgrade the CLI for a newer bundle. No parent-directory search.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: skill.DefaultSkillsDir, Usage: "Skills parent directory"},
			&cli.BoolFlag{Name: "dry-run", Usage: "Show the plan without writing"},
		},
		Commands: []*cli.Command{
			{Name: "install", Usage: "Install into an empty directory or matching installation", Action: action,
				Description: "Copies the bundled skills into --dir. It fails when the directory holds files that are not a matching " +
					"installation, and never overwrites a file. Use --dry-run to preview."},
			{Name: "update", Usage: "Update a managed installation, preserving unrelated files", Action: action,
				Description: "Replaces the managed skills in --dir with the bundle of this CLI. Locally modified files and unsafe " +
					"links block the update, and unrelated files are kept. Use --dry-run to preview."},
			{Name: "status", Usage: "Show bundled and installed revisions and local modifications", Action: action,
				Description: "Compares the skills in --dir with this CLI's bundle and reports each skill's state and revisions. " +
					"A stale or modified installation is a problem and exits 1; nothing is written."},
		},
	}
}
