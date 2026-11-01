// Command arkoren-ci maintains repository policy and approved release automation.
package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

// usage describes the implemented boundary, not future release operations.
const usage = `Usage: arkoren-ci inventory [--repo DIRECTORY] [--commit FULL_SHA]
       arkoren-ci release plan --sha FULL_SHA --output DIRECTORY [--repo ROOT] [--evidence FILE] [--initial-module ROOT] [--historical-v1]
       arkoren-ci release evidence --output FILE [--repo ROOT]
        arkoren-ci release tool
        arkoren-ci release notes prepare|inspect|verify|publish [--manifest FILE] [--output FILE] [--repo ROOT]
        arkoren-ci release publish prepare|preflight|tags|verify|releases [--manifest FILE] [--output FILE] [--verification FILE] [--repo ROOT]
        arkoren-ci release policy check [--locked] [--output FILE] [--repo ROOT]
        arkoren-ci release policy activate --output NEW_DIRECTORY [--repo ROOT]

Development-only maintenance command, never a public module release target.
Publishing requires the trusted main workflow event and exact-green evidence.
inventory emits one JSON document; diagnostics are JSON on stderr.
Without --commit, inventory includes ignored working-tree modules.
--commit validates the exact committed public inventory against its policy.
--repo defaults to the enclosing Git checkout, never the executable location.
`

// main maps command outcomes to process status and cancels owned work on signals.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout)

	if err != nil {
		data, _ := json.Marshal(map[string]string{"error": err.Error()})
		// A diagnostic failure cannot turn an already-failed command into success.
		_, _ = fmt.Fprintln(os.Stderr, string(data))
		os.Exit(1)
	}
}

// run keeps help offline and inventory output separate from flag diagnostics.
func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)

		return err
	}
	if args[0] == "release" {
		return releaseCommand(ctx, args[1:], stdout)
	}
	if args[0] != "inventory" {
		return fmt.Errorf("unknown command: %s", args[0])
	}
	flags := flag.NewFlagSet("inventory", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("repo", "", "checkout directory")
	revision := flags.String("commit", "", "exact commit SHA")
	err := flags.Parse(args[1:])

	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(stdout, usage)

		return err
	}
	if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("inventory accepts flags only")
	}
	if *root == "" {
		data, err := process.Run(ctx, ".", "git", "", false, "rev-parse", "--show-toplevel")

		if err != nil {
			return fmt.Errorf("resolve repository root; use --repo: %w", err)
		}
		*root = strings.TrimSpace(string(data))
	}
	result, err := repository.Inspect(ctx, *root, *revision)

	if err != nil {
		return err
	}
	data, err := json.Marshal(result)

	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))

	return err
}
