package main

import (
	"context"
	"strings"

	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/remote"
)

// rootCLICommand owns only the static grammar. The tree and its mutable flags
// belong to one invocation, while all streams and process inputs remain injected.
// Automatic commands and process exits are disabled: run owns those policies.
func rootCLICommand(env console.Environment, flags console.Globals) *cli.Command {
	root := &cli.Command{
		Name:            "arkoren",
		Usage:           "Create projects and inspect applications through MCP",
		HideHelpCommand: true,
		Version:         console.VersionInfo(),
		HideVersion:     true,
		Description:     "Run 'arkoren tools' to discover remote MCP tools as JSON. Command results are JSON; use --help for usage.",
		Flags:           rootCLIFlags(),
		Reader:          env.Stdin,
		Writer:          env.Stdout,
		ErrWriter:       env.Stderr,
		Commands: []*cli.Command{
			newCLICommand(env), skillsCLICommand(env), keyCLICommand(env), certificateCLICommand(env),
			toolsCLICommand(env, flags),
			statusCLICommand(env, flags),
		},
		ExitErrHandler: func(context.Context, *cli.Command, error) {
			// Arkoren reports the returned error once and never exits here.
		},
	}
	root.Flags = append(root.Flags, &cli.BoolFlag{
		Name: "version", Aliases: []string{"v"}, Usage: "Print version as JSON; --verbose adds safe build identifiers",
		Action: func(_ context.Context, _ *cli.Command, enabled bool) error {
			if !enabled {
				return nil
			}

			if err := env.Result(console.VersionReport(env.Presentation.Verbose)); err != nil {
				return err
			}

			return &console.ExitError{Code: console.ExitOK}
		},
	})
	// Walk reaches every leaf because urfave does not inherit OnUsageError.
	// The callback cannot fail; parser errors never print framework diagnostics.
	_ = root.Walk(func(command *cli.Command) error {
		command.OnUsageError = staticCLIUsageError

		return nil
	})

	return root
}

// styleCLIHelp decorates this invocation's help only when output is a terminal
// and neither presentation opt-out applies. Ordinary commands do not consult
// the environment merely to construct an unused help template.
func styleCLIHelp(root *cli.Command, env console.Environment, noColor bool) {
	if !env.IsTerminal || noColor {
		return
	}

	if env.Env != nil {
		if value, _ := env.Env("NO_COLOR"); value != "" {
			return
		}
	}

	root.CustomRootCommandHelpTemplate = boldHelpTemplate(cli.RootCommandHelpTemplate)
	// Walk's callback cannot fail; every template remains local to this tree.
	_ = root.Walk(func(command *cli.Command) error {
		template := cli.CommandHelpTemplate

		if len(command.Commands) > 0 {
			template = cli.SubcommandHelpTemplate
		}

		command.CustomHelpTemplate = boldHelpTemplate(template)

		return nil
	})
}

// boldHelpTemplate decorates only urfave's section labels before rendering.
// Command descriptions and other generated content retain their original text;
// per-command templates avoid changing urfave's package-level defaults.
func boldHelpTemplate(template string) string {
	replacements := []string{}

	for _, heading := range []string{
		"NAME", "USAGE", "VERSION", "DESCRIPTION", "CATEGORY", "COMMANDS", "GLOBAL OPTIONS", "OPTIONS", "COPYRIGHT",
	} {
		replacements = append(replacements, heading+":", "\x1b[1m"+heading+":\x1b[0m")
	}

	return strings.NewReplacer(replacements...).Replace(template)
}

// rootCLIFlags exposes the reserved global definitions in generated help. The
// prepass owns their values; these fresh flags have no environment bindings or
// destinations. Framework help and version flags remain framework-owned.
func rootCLIFlags() []cli.Flag {
	flags := make([]cli.Flag, 0, len(remote.GlobalFlagDefs))

	for _, definition := range remote.GlobalFlagDefs {
		if definition.Names[0] == "h" {
			continue
		}

		switch definition.Value {
		case "duration":
			flags = append(flags, &cli.DurationFlag{
				Name: definition.Names[0], Aliases: definition.Names[1:], Usage: definition.Doc,
				Value: console.DefaultTimeout, HideDefault: true,
			})
		case "":
			flags = append(flags, &cli.BoolFlag{
				Name: definition.Names[0], Aliases: definition.Names[1:], Usage: definition.Doc,
			})
		default:
			flags = append(flags, &cli.StringFlag{
				Name: definition.Names[0], Aliases: definition.Names[1:], Usage: definition.Doc,
			})
		}
	}

	return flags
}

// runStaticCLI applies raw-token approval policy before handing grammar to
// urfave. Help skips approval validation without executing provisioning actions.
// Only the parsed global help state bypasses approval, not a literal --help
// argument after --. Family help is rendered through its parent so urfave uses
// that family's local template; leaves use the native help flag.
func runStaticCLI(ctx context.Context, root *cli.Command, args []string, flags console.Globals) error {
	if flags.Help {
		args = localHelpArgs(root, args)
	}

	certificateAction := len(args) > 0 && args[0] == "certificate"

	if certificateAction && !flags.Help {
		if err := validateCertificateApproval(args[1:]); err != nil {
			return err
		}
	}

	return root.Run(ctx, append([]string{"arkoren"}, args...))
}

// localHelpArgs restores the help flag consumed by the global prepass using
// only the recognized command path. Discarding flags and operands prevents --
// or a value-taking flag from turning help into data and executing an action.
// Asking the parent to describe a family preserves its per-command template
// without changing urfave's global help renderer. Literal help after -- never
// reaches this function because the prepass does not mark it as global help.
func localHelpArgs(root *cli.Command, args []string) []string {
	for index, name := range args {
		next := root.Command(name)

		if next == nil {
			args = args[:index:index]
			break
		}

		root = next
	}

	if len(args) > 0 && len(root.Commands) > 0 {
		return append(append([]string{}, args[:len(args)-1]...), "--help", args[len(args)-1])
	}

	return append(args, "--help")
}

// toolsCLICommand owns whole-catalog discovery. Its native help is offline;
// the action resolves configuration only after rejecting positional arguments.
// JSON output contains endpoint context and callable tool summaries.
func toolsCLICommand(env console.Environment, flags console.Globals) *cli.Command {
	return &cli.Command{
		Name:  "tools",
		Usage: "List remote tool commands, descriptions, and capabilities as JSON",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "Compatibility flag; tools always prints JSON"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Present() {
				return console.UsageError("tools takes no arguments; run 'arkoren NAME --help' for command usage")
			}

			session, err := remote.NewSession(env, flags)

			if err != nil {
				return err
			}

			return session.Tools(ctx)
		},
	}
}

// statusCLICommand validates the argument-free status grammar before resolving
// endpoint settings. Construction never discovers tools or opens a connection;
// generated help therefore remains independent of inspector configuration.
func statusCLICommand(env console.Environment, flags console.Globals) *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "Show inspector identity, protocol, health, and configuration sources",
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Present() {
				return console.UsageError("status takes no arguments")
			}

			session, err := remote.NewSession(env, flags)

			if err != nil {
				return err
			}

			return session.Status(ctx)
		},
	}
}

// staticCLIUsageError replaces parser messages with a value-free diagnostic.
// Framework errors may quote credentials supplied as unknown flags or malformed
// values; neither their text nor wrapped cause is safe to report to the user.
func staticCLIUsageError(_ context.Context, command *cli.Command, _ error, _ bool) error {
	return console.UsageError("invalid arguments for %s; run '%s --help' for usage", command.FullName(), command.FullName())
}
