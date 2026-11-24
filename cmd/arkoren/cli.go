package main

import (
	"cmp"
	"context"
	"regexp"
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
		Description:     "Run 'arkoren tools' to discover remote MCP tools. Results are text on a terminal and JSON otherwise or with --json; use --help for usage.",
		Flags:           rootCLIFlags(),
		Reader:          env.Stdin,
		Writer:          env.Stdout,
		ErrWriter:       env.Stderr,
		Commands: []*cli.Command{
			newCLICommand(env), addCLICommand(env), checkCLICommand(env), mapCLICommand(env),
			skillsCLICommand(env), keyCLICommand(env), certificateCLICommand(env),
			toolsCLICommand(env, flags),
			statusCLICommand(env, flags),
		},
		ExitErrHandler: func(context.Context, *cli.Command, error) {
			// Arkoren reports the returned error once and never exits here.
		},
	}
	root.Flags = append(root.Flags, &cli.BoolFlag{
		Name: "version", Aliases: []string{"v"}, Usage: "Print version; --verbose adds safe build identifiers",
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
	// Every command group prints its own help when run bare.
	_ = root.Walk(func(command *cli.Command) error {
		command.OnUsageError = staticCLIUsageError

		if command != root && len(command.Commands) > 0 && command.Action == nil {
			command.Action = groupAction
		}

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
// Its result contains endpoint context and callable tool summaries. The
// global --json flag, not a local one, selects JSON on a terminal.
func toolsCLICommand(env console.Environment, flags console.Globals) *cli.Command {
	return &cli.Command{
		Name:  "tools",
		Usage: "List remote tool commands, descriptions, and capabilities",
		Description: "Asks the inspector at --url which tools it serves and lists each as a command you can run, " +
			"with its description. --verbose adds schemas.",
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Present() {
				return console.UsageError("too many arguments: %s takes no arguments", command.FullName())
			}

			session, err := remote.NewSession(env, flags)

			if err != nil {
				return err
			}

			return remote.ToolFailure(session.Tools(ctx))
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
		Description: "Connects to the inspector at --url and reports its identity, protocol and health, and where " +
			"the URL and token came from. --verbose adds build information.",
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Present() {
				return console.UsageError("too many arguments: %s takes no arguments", command.FullName())
			}

			session, err := remote.NewSession(env, flags)

			if err != nil {
				return err
			}

			return remote.ToolFailure(session.Status(ctx))
		},
	}
}

// groupAction is the action of a command that only groups subcommands. Run bare
// it prints its help to stdout and succeeds, like the top-level command; an
// argument that names no subcommand is a usage error listing the accepted ones.
func groupAction(_ context.Context, command *cli.Command) error {
	if !command.Args().Present() {
		// ShowSubcommandHelp ignores CustomHelpTemplate, which carries the bold
		// section labels, so print with the command's own template.
		template := cmp.Or(command.CustomHelpTemplate, cli.SubcommandHelpTemplate)
		cli.HelpPrinter(command.Root().Writer, template, command)

		return nil
	}

	names := make([]string, 0, len(command.Commands))

	for _, subcommand := range command.Commands {
		if !subcommand.Hidden && subcommand.Name != "help" {
			names = append(names, subcommand.Name)
		}
	}

	return console.UsageError("unknown command %q for %s: accepted commands are %s",
		command.Args().First(), command.FullName(), strings.Join(names, ", "))
}

// requireArguments checks the positionals of a command whose declared arguments
// are optional to urfave, so the message can name the one at fault: names are
// the declared argument names in order and values what each received (empty
// when absent). Anything left in command.Args() is surplus.
func requireArguments(command *cli.Command, names, values []string) error {
	for index, name := range names {
		if values[index] == "" {
			return console.UsageError("missing argument %s", name)
		}
	}

	if command.Args().Present() {
		takes := "no arguments"

		if len(names) > 0 {
			takes = strings.Join(names, " ")
		}

		return console.UsageError("too many arguments: %s takes %s", command.FullName(), takes)
	}

	return nil
}

// Framework usage failures quote the offending token, and a mistyped flag may
// hold a credential, so the messages below are rebuilt from the flag or argument
// name alone, never from a value.
var (
	unknownFlagPattern  = regexp.MustCompile(`^flag provided but not defined: -+([^=\s]+)`)
	missingValuePattern = regexp.MustCompile(`^flag needs an argument: -+([^=\s]+)`)
	invalidFlagPattern  = regexp.MustCompile(`for flag -*([A-Za-z0-9][A-Za-z0-9._-]*)`)
	invalidArgPattern   = regexp.MustCompile(`for argument ([A-Za-z0-9_]+)`)
)

// staticCLIUsageError replaces parser messages with a value-free diagnostic
// that names the flag or argument at fault and, where the command knows them,
// the accepted flags. Framework errors may quote credentials supplied as
// unknown flags or malformed values; neither their text nor wrapped cause is
// safe to report to the user.
func staticCLIUsageError(_ context.Context, command *cli.Command, cause error, _ bool) error {
	text := cause.Error()

	if match := unknownFlagPattern.FindStringSubmatch(text); match != nil {
		return console.UsageError("unknown flag %s for %s: %s", flagLabel(match[1]), command.FullName(), acceptedFlags(command))
	}

	if match := missingValuePattern.FindStringSubmatch(text); match != nil {
		return console.UsageError("flag %s needs a value", flagLabel(match[1]))
	}

	if match := invalidFlagPattern.FindStringSubmatch(text); match != nil {
		return console.UsageError("invalid value for flag %s", flagLabel(match[1]))
	}

	if match := invalidArgPattern.FindStringSubmatch(text); match != nil {
		return console.UsageError("invalid value for argument %s", match[1])
	}

	if strings.Contains(text, "can't duplicate this flag") {
		return console.UsageError("a flag of %s was given more than once", command.FullName())
	}

	return console.UsageError("invalid arguments for %s", command.FullName())
}

// maxFlagNameRunes bounds the unknown flag name that a usage error echoes. Real
// flag names are short words; a longer token is more likely a pasted credential
// than a typo, so it is reported only as "a flag".
const maxFlagNameRunes = 24

// flagLabel spells an unknown flag for a usage error: "--name" when the name
// looks like one, otherwise "(an unrecognized long token)" so a credential
// pasted where a flag belongs never reaches the terminal or a log.
func flagLabel(name string) string {
	if len([]rune(name)) > maxFlagNameRunes {
		return "(an unrecognized long token)"
	}

	return "--" + name
}

// acceptedFlags lists the flags a command and its parent groups declare,
// excluding the global flags that every command takes, as "accepted flags are
// --a, --b" or "it takes no flags".
func acceptedFlags(command *cli.Command) string {
	var names []string

	lineage := command.Lineage()

	for _, current := range lineage[:len(lineage)-1] {
		for _, flag := range current.Flags {
			if flag.Names()[0] != "help" {
				names = append(names, "--"+flag.Names()[0])
			}
		}
	}

	if len(names) == 0 {
		return "it takes no flags"
	}

	return "accepted flags are " + strings.Join(names, ", ")
}
