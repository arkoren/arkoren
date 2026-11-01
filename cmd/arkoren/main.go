// Command arkoren creates projects, manages agent skills, provisions keys and
// certificates offline, and inspects applications through standard MCP endpoints.
// Local commands need no
// connection. Remote commands read tools/list on every run and build flags and
// help from native tool schemas. Exact MCP names work without Arkoren metadata;
// explicit CLI metadata adds groups, positional arguments, and cursor
// polling for application, router, scheduler, telemetry, and custom tools.
// Duplicate metadata aliases are refused; use the exact native names advertised
// by tools instead. MCP input-required results fail without automatic replay or
// interactive continuation.
// Connection, discovery, and remote help never invoke tools. The status command
// explicitly calls uniquely advertised identity and health roles for its report.
//
// # Install
//
//	go install arkoren.dev/cmd/arkoren@latest
//
// or pin it in a module with "go get -tool arkoren.dev/cmd/arkoren" and run
// it as "go tool arkoren".
//
// # Usage
//
//	arkoren new DIR [flags]
//	arkoren skills install|update|status [flags]
//	arkoren certificate root generate --name "Local development" --cert root.pem --key root-key.pem
//	arkoren certificate generate --ca-cert root.pem --ca-key root-key.pem --dns localhost --cert server.pem --key server-key.pem
//	arkoren certificate check --cert server.pem --key server-key.pem
//	arkoren [global flags] tools [--json]
//	arkoren [global flags] <command> --help
//	arkoren [global flags] status
//	arkoren --version
//	arkoren [global flags] <native-tool-name> [-input JSON]
//	arkoren [global flags] <extension>:<operation> [flags] [arguments]
//
// Remote operations require the MCP endpoint URL: -url, else ARKOREN_MCP_URL, else
// that key in ./.env. The bearer token is optional and found the same way
// (-token, ARKOREN_MCP_TOKEN, ./.env). The value is an existing bearer API key,
// JWT, or OAuth access token accepted by the endpoint, not an inspection-specific
// shared secret. The CLI does not log in, refresh tokens, manage cookies, or
// configure mutual TLS automatically. Obtain credentials through the endpoint's
// application or authorization server, then supply them, for example:
//
//	ARKOREN_MCP_URL=https://admin.example.com/mcp ARKOREN_MCP_TOKEN="$ACCESS_TOKEN" arkoren status
//
// Endpoint URLs must not contain userinfo, a query, or a fragment. Invalid-URL
// diagnostics omit the supplied URL; credentials belong in the token setting.
// Escaped path separators remain part of the approved endpoint resource.
//
// Certificate generation is offline and never installs trust. Check validates
// structural material and optional key matching, not expiry or trust. Explicit
// root trust/untrust commands change only the selected local system trust store.
// Cleanup errors describe completed or potentially partial native changes; they
// never imply rollback. Private key files and scaffold .env receive POSIX 0600
// or a protected current-user-only Windows DACL at exclusive creation.
//
// Command results, including local operations and --version, are JSON.
// "arkoren tools" prints command names, descriptions, and capabilities; --json
// remains accepted for compatibility. Errors are JSON objects on stderr. Follow emits
// NDJSON. Help and interactive prompts remain text; explicit key exports remain
// raw PEM or base64 rather than JSON strings.
// Default reports summarize incidental lists and diagnostics. --verbose adds
// complete safe context, file inventories, schemas, and status/build details;
// it does not change remote request bodies or successful tool payloads.
//
// A URL from implicit ./.env cannot receive a token from the environment or
// -token: the CLI exits 2 before any network request. Pin the trusted URL with
// -url or ARKOREN_MCP_URL, or explicitly approve a trusted file's URL with
// -env-file path (including -env-file .env). An empty -env-file disables file
// loading. A URL and token both from the file still work. With no arguments,
// the CLI prints offline help without reading configuration.
//
// An operation's flags are generated from its input schema. A flag is a
// dotted path into the input JSON (-address.city Paris, -items.0.sku A,
// -tag a -tag b), -input supplies a base object, and positional arguments
// fill the operation's positional properties. Only value conversion is done
// locally: the server is the sole validator, and its errors are mapped back
// to the flag that set each value. Integer flags have no machine-size limit;
// number flags accept JSON number syntax without floating-point conversion.
// Exact numeric JSON is retained across supported SDK result/schema decoding
// and through safe failure metadata; SDK protocol/decoding errors still fail.
// Repeated -input flags read only the final source. Cancellation returns without
// closing caller-owned stdin; a blocked generic reader ends when its owner
// releases input. Cursor follow never receives a condition-wait total deadline.
// Before tool discovery, conventional-looking tokens provide only provisional
// connection retry hints. They cannot consume another flag's value or authorize
// waiting/replay: normal schema-guided parsing determines the actual request and
// deadline. Session termination is best effort, bounded by one second and by the
// command's remaining deadline or cancellation, independently of long tool calls.
//
// # Design
//
// [run] is the whole program, parameterized by an injected process environment
// so tests run against a real inspector without a process. cli.go declares a
// fresh static urfave command tree; remote input bypasses that parser entirely.
// internal/remote owns MCP connection, discovery, exact wire JSON, schema-guided
// input, waiting, and follow. internal/console owns shared output and diagnostics.
// internal/scaffold plans application creation; internal/skill manages the bundled
// consumer skill. Both use internal/files for confined filesystem operations and
// exclusive private creation. assets.go embeds the unchanged templates and skill
// tree at the module root and the command boundary injects their filesystems.
// certificate.go owns offline certificate grammar and foundation/certificate
// material; persist.go owns key and certificate persistence. internal/trust owns
// explicit approval and native system trust backends.
//
// # Compatibility
//
// The official MCP SDK negotiates protocol versions, including legacy response
// forms. CLI metadata is optional: external tools remain directly invocable by
// exact name, with -input for schemas outside the generated-flag subset. Only
// explicit metadata enables positional arguments or cursor polling.
//
// # Exit codes
//
//	0  success
//	1  failure: unreachable, server error, 404 or 409 or 405, protocol
//	   mismatch, stdout write failure, or "app:health" reports status "failing"
//	2  usage: bad flag or value, unapproved project URL with a token,
//	   missing URL, unknown command or operation
//	4  authentication failed (401)
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/remote"
)

// main wires the process to [run] and cancels on SIGINT or SIGTERM, which
// ends a follow stream with exit code 0. SIGPIPE is ignored so writes to a
// closed stdout pipe return EPIPE to run instead of bypassing its exit policy.
func main() {
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stat, err := os.Stdout.Stat()
	outputTerminal := err == nil && stat.Mode()&os.ModeCharDevice != 0

	stat, err = os.Stdin.Stat()
	inputTerminal := err == nil && stat.Mode()&os.ModeCharDevice != 0

	stat, err = os.Stderr.Stat()
	errorTerminal := err == nil && stat.Mode()&os.ModeCharDevice != 0

	env := console.Environment{
		Stdin:         os.Stdin,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		Env:           os.LookupEnv,
		IsTerminal:    outputTerminal,
		ErrorTerminal: errorTerminal,
		InputTerminal: inputTerminal,
	}

	dir, err := os.Getwd()

	if err != nil {
		// Even process setup failures use the requested diagnostic presentation.
		env.Presentation, _, _ = parseGlobals(os.Args[1:])
		os.Exit(report(env, err))
	}

	env.Dir = dir

	code := run(ctx, os.Args[1:], env)
	stop()
	os.Exit(code)
}

// run executes one command line and returns the exit code. args excludes the
// program name. Every stdout path uses commandOutput: its first write failure
// takes precedence over command status and is reported as exitFailure. This
// includes EPIPE; no output failure is treated as successful cancellation.
func run(ctx context.Context, args []string, env console.Environment) int {
	output := &console.Output{Writer: env.Stdout}
	env.Stdout = output

	flags, rest, err := parseGlobals(args)
	env.Presentation = flags

	if err == nil {
		err = executeParsed(ctx, rest, env, flags)
	}

	if output.Err != nil {
		err = fmt.Errorf("write stdout: %w", output.Err)
	}

	return report(env, err)
}

// report prints err to stderr and returns the exit code it stands for.
func report(env console.Environment, err error) int {
	if err == nil {
		return console.ExitOK
	}

	if exit, ok := errors.AsType[*console.ExitError](err); ok {
		if exit.Err != nil {
			printError(env.Stderr, exit.Err, env.DiagnosticStyle(), env.Presentation.Verbose)
		}

		return exit.Code
	}

	printError(env.Stderr, err, env.DiagnosticStyle(), env.Presentation.Verbose)

	if failure, ok := errors.AsType[*remote.APIError](err); ok && failure.Status == 401 {
		return console.ExitAuth
	}

	return console.ExitFailure
}

// printError writes one JSON diagnostic using stderr's presentation. Writes are
// best effort because reporting their failure would recurse on the same sink.
func printError(out io.Writer, err error, presentation console.Style, verbose bool) {
	_ = console.WriteJSON(out, map[string]console.ErrorReport{"error": remote.DescribeError(err, verbose)}, presentation) // Final diagnostics cannot report their own write failure.
}

// execute extracts reserved globals before constructing the static command tree.
// Only exact local names enter urfave; all other tokens and their arguments reach
// MCP unchanged, so native names resolve before qualified-name validation.
func execute(ctx context.Context, args []string, env console.Environment) error {
	flags, rest, err := parseGlobals(args)

	if err != nil {
		return err
	}

	env.Presentation = flags

	return executeParsed(ctx, rest, env, flags)
}

// executeParsed dispatches an invocation after global parsing. run also uses
// these parsed options when rendering errors, including configuration failures.
func executeParsed(ctx context.Context, rest []string, env console.Environment, flags console.Globals) error {
	root := rootCLICommand(env, flags)

	if len(rest) == 0 {
		styleCLIHelp(root, env, flags.NoColor)

		return runStaticCLI(ctx, root, nil, flags)
	}

	if root.Command(rest[0]) != nil || strings.HasPrefix(rest[0], "-") {
		if flags.Help {
			styleCLIHelp(root, env, flags.NoColor)
		}

		return runStaticCLI(ctx, root, rest, flags)
	}

	session, err := remote.NewSession(env, flags)

	if err != nil {
		return err
	}

	return session.Remote(ctx, rest[0], rest[1:])
}

// parseGlobals extracts the global flags from anywhere in args and returns
// what remains, in order, for the command and the operation's own flags. It
// stops at "--", which stays in the remainder. Global names win over an
// operation's own flags of the same name.
func parseGlobals(args []string) (console.Globals, []string, error) {
	flags := console.Globals{
		Timeout: console.DefaultTimeout,
	}
	var rest []string

	for index := 0; index < len(args); index++ {
		arg := args[index]

		if arg == "--" {
			return flags, append(rest, args[index:]...), nil
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		def := remote.GlobalFlagFor(name)

		if !strings.HasPrefix(arg, "-") || def == nil {
			rest = append(rest, arg)

			continue
		}

		if def.Value != "" && !hasValue {
			if index+1 >= len(args) {
				return flags, nil, console.UsageError("flag -%s needs a value", name)
			}

			index++
			value = args[index]
		}

		switch def.Names[0] {
		case "url":
			flags.URL = value
		case "token":
			flags.Token = value
		case "env-file":
			flags.EnvFile = &value
		case "request-timeout":
			timeout, err := time.ParseDuration(value)

			if err != nil {
				return flags, nil, console.UsageError("-request-timeout: expected a duration such as 10s")
			}

			flags.Timeout = timeout
			flags.TimeoutSet = true
		default:
			enabled := true

			if hasValue {
				var err error

				if enabled, err = parseBool(value); err != nil {
					return flags, nil, console.UsageError("-%s: %v", name, err)
				}
			}

			switch def.Names[0] {
			case "verbose":
				flags.Verbose = enabled
			case "compact":
				flags.Compact = enabled
			case "no-color":
				flags.NoColor = enabled
			case "f":
				flags.Follow = enabled
			case "h":
				flags.Help = enabled
			}
		}
	}

	return flags, rest, nil
}

// parseBool accepts exactly true and false. Invalid input is not included in
// diagnostics because a misplaced credential may reach any global flag.
func parseBool(text string) (bool, error) {
	switch text {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}

	return false, fmt.Errorf("expected true or false")
}
