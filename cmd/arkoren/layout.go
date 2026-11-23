package main

import (
	"context"
	"io"
	"strings"

	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/cmd/arkoren/internal/layout"
	"arkoren.dev/cmd/arkoren/internal/scaffold"
)

// addCLICommand declares `arkoren add`, which creates the files of the project
// layout from the embedded templates and registers them with one-line
// insertions. Every subcommand takes one NAME, --dry-run and --skip-tidy, never
// overwrites an existing file, and reads only the current directory. After
// writing, it runs go mod tidy when a new file imports an unrequired module.
func addCLICommand(env console.Environment) *cli.Command {
	dryRun := &cli.BoolFlag{Name: "dry-run", Usage: "Show the files and edits without writing"}
	skipTidy := &cli.BoolFlag{Name: "skip-tidy", Usage: "Write files offline without running go mod tidy for new imports"}

	// subcommand declares one add operation. argsUsage names its single positional
	// argument, such as NAME; empty means the operation takes none.
	subcommand := func(name, usage, argsUsage string, flags []cli.Flag, plan func(*layout.Project, *cli.Command) (*layout.Plan, error)) *cli.Command {
		return &cli.Command{
			Name:      name,
			Usage:     usage,
			ArgsUsage: argsUsage,
			Flags:     append(flags, dryRun, skipTidy),
			Action: func(ctx context.Context, command *cli.Command) error {
				if argsUsage == "" && command.Args().Present() {
					return console.UsageError("add %s takes no arguments; usage: arkoren add %s [flags]", name, name)
				}

				if argsUsage != "" && command.Args().Len() != 1 {
					return console.UsageError("add %s requires exactly one %s; usage: arkoren add %s %s [flags]", name, argsUsage, name, argsUsage)
				}

				project, err := layout.Load(ctx, env.Dir)

				if err != nil {
					return err
				}

				changes, err := plan(project, command)

				if err != nil {
					return err
				}

				report := addReport(changes, command.Bool("dry-run"))

				if command.Bool("dry-run") {
					return env.Result(report)
				}

				if err := project.Apply(ctx, changes); err != nil {
					return err
				}

				needed, err := project.NeedsTidy(changes)

				if err != nil {
					return err
				}

				if needed && !command.Bool("skip-tidy") {
					if len(changes.Requires) > 0 {
						if err := scaffold.Get(ctx, project.Root, changes.Requires); err != nil {
							return err
						}
					}

					if err := scaffold.Tidy(ctx, project.Root); err != nil {
						return err
					}

					report["tidied"] = true
				}

				return env.Result(report)
			},
		}
	}

	featureFlags := []cli.Flag{&cli.StringFlag{Name: "process", Value: "server", Usage: "Process root (internal/bootstrap/NAME) whose Compose uses the surface providers"}}

	for _, surface := range layout.Surfaces {
		featureFlags = append(featureFlags, &cli.BoolFlag{Name: surface, Usage: "Create the " + surface + " surface and register it"})
	}

	arkIAMFlags := []cli.Flag{
		&cli.StringSliceFlag{Name: "with", Usage: "Features to enable, comma separated: " + strings.Join(layout.IAMFeatures, ", ")},
		&cli.StringFlag{Name: "process", Value: "server", Usage: "Process root (internal/bootstrap/NAME) whose Compose uses the email jobs (IAMJobs and Work)"},
	}

	return &cli.Command{
		Name:  "add",
		Usage: "Add a feature, process, provider, migration or ark module to the project layout",
		Description: "Creates files from templates and never overwrites one; it edits existing code only by inserting " +
			"compose.Use lines into a process root's Compose or the migrations list. Run it in the project root, which needs " +
			"cmd/<process>/main.go and internal/bootstrap. When a new file imports a module go.mod does not require, " +
			"it runs go mod tidy unless --skip-tidy is set. Use --dry-run to preview. See the arkoren-structure skill.",
		Commands: []*cli.Command{
			subcommand("feature", "Create internal/NAME with its bootstrap providers, used from a process's Compose", "NAME", featureFlags,
				func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
					options := layout.FeatureOptions{Name: command.Args().First(), Process: command.String("process")}

					for _, surface := range layout.Surfaces {
						if command.Bool(surface) {
							options.Surfaces = append(options.Surfaces, surface)
						}
					}

					return project.AddFeature(options, newTemplates)
				}),
			subcommand("process", "Create cmd/NAME and its process root with App and an empty Compose", "NAME", nil,
				func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
					return project.AddProcess(command.Args().First(), newTemplates)
				}),
			subcommand("provider", "Create a shared provider in internal/bootstrap", "NAME", nil,
				func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
					return project.AddProvider(command.Args().First(), newTemplates)
				}),
			subcommand("migration", "Create an application migration and register it in All", "NAME", nil,
				func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
					return project.AddMigration(command.Args().First(), newTemplates)
				}),
			{
				Name:  "ark",
				Usage: "Add the providers of an opt-in ark module (iam, oauth)",
				Description: "Writes the bootstrap providers that wire an ark module, with local defaults for every shared provider " +
					"it needs and internal/bootstrap does not declare, and pins the ark's module version with go get. " +
					"The result lists the steps it could not do itself under next_steps.",
				Commands: []*cli.Command{
					subcommand("iam", "Add the identity module (ark/iam): IAM, IAMRouter and a provider per --with feature", "", arkIAMFlags,
						func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
							options := layout.IAMOptions{With: command.StringSlice("with"), Process: command.String("process")}

							return project.AddIAM(options, newTemplates)
						}),
					subcommand("oauth", "Add an OAuth sign-in client (ark/oauth) for PROVIDER: google or github", "PROVIDER", nil,
						func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
							return project.AddOAuth(command.Args().First(), newTemplates)
						}),
				},
			},
		},
	}
}

// addReport is the JSON result of an add: the files created, the insertions
// made into existing files, and whether go mod tidy ran (set by the caller).
// When the plan has them it also lists the pinned modules to fetch (requires)
// and the steps left to the user (next_steps). Paths are slash-separated and
// project-relative.
func addReport(plan *layout.Plan, dryRun bool) map[string]any {
	edits := make([]map[string]any, 0, len(plan.Edit))

	for _, edit := range plan.Edit {
		edits = append(edits, map[string]any{"path": edit.Path, "inserted": edit.Inserted})
	}

	report := map[string]any{"dry_run": dryRun, "created": files.Paths(plan.Create), "edited": edits, "tidied": false}

	if len(plan.Requires) > 0 {
		report["requires"] = plan.Requires
	}

	if len(plan.Notes) > 0 {
		report["next_steps"] = plan.Notes
	}

	return report
}

// checkCLICommand declares `arkoren check`: it reports layout rule violations
// as one JSON document and exits 1 when there are any.
func checkCLICommand(env console.Environment) *cli.Command {
	return &cli.Command{
		Name:  "check",
		Usage: "Check the project against the layout rules",
		Description: "Reports import-direction, bootstrap-shape, naming and Composer.Use violations with file:line as JSON, and exits 1 " +
			"if there are any. Works from source without building. Run it in the project root. " +
			"See the arkoren-structure skill for the rules.",
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Present() {
				return console.UsageError("check takes no arguments")
			}

			project, err := layout.Load(ctx, env.Dir)

			if err != nil {
				return err
			}

			violations := project.Check()

			if violations == nil {
				violations = []layout.Violation{}
			}

			if err := env.Result(map[string]any{"ok": len(violations) == 0, "violations": violations}); err != nil {
				return err
			}

			if len(violations) > 0 {
				return &console.ExitError{Code: console.ExitFailure}
			}

			return nil
		},
	}
}

// mapCLICommand declares `arkoren map`: the compact project inventory, as JSON
// by default or as plain text lines with --text.
func mapCLICommand(env console.Environment) *cli.Command {
	return &cli.Command{
		Name:  "map",
		Usage: "List processes, providers, features and migrations",
		Description: "Prints what each process uses in Compose, the bootstrap providers by file, the " +
			"features with their constructors and surfaces, and the migrations, from source. Read it before searching the code. " +
			"Run it in the project root.",
		Flags: []cli.Flag{&cli.BoolFlag{Name: "text", Usage: "Print compact text lines instead of JSON"}},
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Present() {
				return console.UsageError("map takes no arguments")
			}

			project, err := layout.Load(ctx, env.Dir)

			if err != nil {
				return err
			}

			inventory := project.Map()

			if command.Bool("text") {
				_, err := io.WriteString(&console.Output{Writer: env.Stdout}, inventory.Text())

				return err
			}

			return env.Result(inventory)
		},
	}
}
