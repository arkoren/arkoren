package main

import (
	"context"
	"fmt"
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

	// subcommand declares one add operation. argsUsage names its positional
	// argument, such as NAME, or ends in "..." when it takes one or more; empty
	// means the operation takes none.
	subcommand := func(name, usage, description, argsUsage string, flags []cli.Flag, plan func(*layout.Project, *cli.Command) (*layout.Plan, error)) *cli.Command {
		variadic := strings.HasSuffix(argsUsage, "...")

		return &cli.Command{
			Name:        name,
			Usage:       usage,
			Description: description,
			ArgsUsage:   argsUsage,
			Flags:       append(flags, dryRun, skipTidy),
			Action: func(ctx context.Context, command *cli.Command) error {
				if argsUsage == "" && command.Args().Present() {
					return console.UsageError("too many arguments: %s takes no arguments", command.FullName())
				}

				if argsUsage != "" && command.Args().Len() == 0 {
					return console.UsageError("missing argument %s", argsUsage)
				}

				if argsUsage != "" && !variadic && command.Args().Len() > 1 {
					return console.UsageError("too many arguments: %s takes one %s", command.FullName(), argsUsage)
				}

				project, err := layout.Load(ctx, env.Dir)

				if err != nil {
					return err
				}

				changes, err := plan(project, command)

				if err != nil {
					return err
				}

				summary := addSummary(command)

				if command.Bool("dry-run") {
					return env.Result(addReport(summary, changes, true, false))
				}

				if err := project.Apply(ctx, changes); err != nil {
					return err
				}

				needed, err := project.NeedsTidy(changes)

				if err != nil {
					return err
				}

				tidied := false

				if needed && !command.Bool("skip-tidy") {
					if len(changes.Requires) > 0 {
						if err := scaffold.Get(ctx, project.Root, changes.Requires); err != nil {
							return err
						}
					}

					if err := scaffold.Tidy(ctx, project.Root); err != nil {
						return err
					}

					tidied = true
				}

				return env.Result(addReport(summary, changes, false, tidied))
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
			subcommand("feature", "Create internal/NAME with its bootstrap providers, used from a process's Compose",
				"Writes internal/NAME (the package, its service and its store) and internal/bootstrap/NAME.go, whose providers a process "+
					"root's Compose uses. Each surface flag adds the subpackage internal/NAME/<surface> with its providers and registers "+
					"them in the --process root: web serves HTTP routes, jobs handles queued work, flow runs workflows and tools exposes MCP tools.",
				"NAME", featureFlags,
				func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
					options := layout.FeatureOptions{Name: command.Args().First(), Process: command.String("process")}

					for _, surface := range layout.Surfaces {
						if command.Bool(surface) {
							options.Surfaces = append(options.Surfaces, surface)
						}
					}

					return project.AddFeature(options, newTemplates)
				}),
			subcommand("process", "Create cmd/NAME and its process root with App and an empty Compose",
				"Writes cmd/NAME/main.go and internal/bootstrap/NAME with the process's App provider and an empty Compose. "+
					"Add providers to it with compose.Use, or let add feature and add ark do it.",
				"NAME", nil,
				func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
					return project.AddProcess(command.Args().First(), newTemplates)
				}),
			subcommand("provider", "Create a shared provider in internal/bootstrap",
				"Writes internal/bootstrap/NAME.go with a provider of that name, for a value several features or processes share. "+
					"Use it from a Compose with compose.Use.",
				"NAME", nil,
				func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
					return project.AddProvider(command.Args().First(), newTemplates)
				}),
			subcommand("migration", "Create an application migration and register it in All",
				"Writes the numbered migration internal/migrations/NNNN_NAME.go and registers it in the All list the migrator applies "+
					"at startup, creating internal/migrations/migrations.go when it is missing. Migrations are not run by this command.",
				"NAME", nil,
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
					subcommand("iam", "Add the identity module (ark/iam): IAM, IAMRouter and a provider per --with feature",
						"Wires ark/iam: users, password login and sessions, mounted under /auth. Writes internal/bootstrap/iam.go "+
							"with the IAM and IAMRouter providers, one provider file per --with feature, and every shared provider "+
							"it needs (Database, Cache, Hasher, Migrator) with local defaults when internal/bootstrap lacks them. "+
							"Existing providers are reused. Migrations run when the server starts, and sign-in clients already "+
							"declared by add ark oauth are passed to the module.\n\n"+
							"Features for --with:\n"+
							"   reset         password reset and registration, by email\n"+
							"   verification  email verification, by email\n"+
							"   two-factor    second factor at sign-in\n"+
							"   tokens        personal API tokens with a scope catalog\n"+
							"   permissions   roles and permissions\n"+
							"   account       GET /auth/account for the signed-in user\n\n"+
							"reset and verification send email through jobs, so the --process root also gets IAMJobs and Work.",
						"", arkIAMFlags,
						func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
							options := layout.IAMOptions{With: command.StringSlice("with"), Process: command.String("process")}

							return project.AddIAM(options, newTemplates)
						}),
					subcommand("oauth", "Add OAuth sign-in clients (ark/oauth) for each PROVIDER: "+strings.Join(layout.OAuthProviders(), ", "),
						"Writes internal/bootstrap/PROVIDER.go for each provider: a client built from <PROVIDER>_* environment "+
							"variables. Client IDs and secrets stay yours to set; the redirect URL is appended to .env. When the IAM "+
							"provider exists the clients are passed to it, otherwise run add ark iam afterwards. github also writes "+
							"GitHubSignUp, which lets a first GitHub login create a user; the other providers sign in existing users only.\n\n"+
							"Providers: "+strings.Join(layout.OAuthProviders(), ", "),
						"PROVIDER...", nil,
						func(project *layout.Project, command *cli.Command) (*layout.Plan, error) {
							return project.AddOAuth(command.Args().Slice(), newTemplates)
						}),
				},
			},
		},
	}
}

// addResult is the JSON result of an add, in the local action envelope: a
// summary, whether it was a dry run, then the file changes and the steps left
// to the user.
type addResult struct {
	// Summary reads "Added feature orders", or "Would add feature orders" for a
	// dry run.
	Summary string `json:"summary"`

	// DryRun reports that nothing was written.
	DryRun bool `json:"dry_run,omitzero"`

	// Changes lists each created file, each inserted line, each pinned module
	// and, when it ran, go mod tidy. Paths are slash-separated and
	// project-relative.
	Changes []console.Change `json:"changes,omitzero"`

	// NextSteps lists the steps the command could not do itself.
	NextSteps []console.Step `json:"next_steps,omitzero"`
}

// addSummary names an add operation and its argument: "Added feature orders"
// or "Added ark iam". With --dry-run it starts with "Would add".
func addSummary(command *cli.Command) string {
	subject := command.Name

	if command.Name == "iam" || command.Name == "oauth" {
		subject = "ark " + command.Name
	}

	if command.Args().Present() {
		subject += " " + strings.Join(command.Args().Slice(), " ")
	}

	if command.Bool("dry-run") {
		return "Would add " + subject
	}

	return "Added " + subject
}

// addReport builds the result of an add from its plan. Each created file is a
// create change, each inserted line an edit change whose detail is that line,
// each pinned module a require change, and go.mod a tidy change when tidied.
func addReport(summary string, plan *layout.Plan, dryRun, tidied bool) addResult {
	report := addResult{Summary: summary, DryRun: dryRun, NextSteps: plan.Steps}

	for _, path := range files.Paths(plan.Create) {
		report.Changes = append(report.Changes, console.Change{Action: console.ActionCreate, Path: path})
	}

	for _, edit := range plan.Edit {
		for _, line := range edit.Inserted {
			report.Changes = append(report.Changes, console.Change{Action: console.ActionEdit, Path: edit.Path, Detail: line})
		}
	}

	for _, module := range plan.Requires {
		report.Changes = append(report.Changes, console.Change{Action: console.ActionRequire, Path: module})
	}

	if tidied {
		report.Changes = append(report.Changes, console.Change{Action: console.ActionTidy, Path: "go.mod"})
	}

	return report
}

// checkResult is the JSON result of `arkoren check`: a verdict in the local
// check envelope, with each violation as a problem naming its rule, message,
// file and line.
type checkResult struct {
	// Summary reads "No layout violations", "1 layout violation" or "3 layout
	// violations".
	Summary string `json:"summary"`

	// OK is false when there is at least one violation; it is always present.
	OK bool `json:"ok"`

	// Problems lists every broken rule; it is an empty array when OK.
	Problems []console.Problem `json:"problems"`
}

// checkReport summarizes violations as problems. The problem list is never
// nil, so the JSON always carries an array.
func checkReport(violations []layout.Violation) checkResult {
	report := checkResult{Summary: "No layout violations", OK: true, Problems: make([]console.Problem, 0, len(violations))}

	for _, violation := range violations {
		report.Problems = append(report.Problems, console.Problem{
			Rule: violation.Rule, Message: violation.Message, Path: violation.File, Line: violation.Line,
		})
	}

	if len(violations) > 0 {
		report.Summary = fmt.Sprintf("%d layout violations", len(violations))

		if len(violations) == 1 {
			report.Summary = "1 layout violation"
		}

		report.OK = false
	}

	return report
}

// checkCLICommand declares `arkoren check`: it reports layout rule violations
// as one result document and exits 1 when there are any.
func checkCLICommand(env console.Environment) *cli.Command {
	return &cli.Command{
		Name:  "check",
		Usage: "Check the project against the layout rules",
		Description: "Reports import-direction, bootstrap-shape, naming and Composer.Use violations as problems with file and line, as JSON when piped, and exits 1 " +
			"if there are any. Works from source without building. Run it in the project root. " +
			"See the arkoren-structure skill for the rules.",
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Present() {
				return console.UsageError("too many arguments: %s takes no arguments", command.FullName())
			}

			project, err := layout.Load(ctx, env.Dir)

			if err != nil {
				return err
			}

			violations := project.Check()

			if err := env.Result(checkReport(violations)); err != nil {
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
				return console.UsageError("too many arguments: %s takes no arguments", command.FullName())
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
