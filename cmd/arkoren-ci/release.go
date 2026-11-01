package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/evidence"
	"arkoren.dev/cmd/arkoren-ci/internal/process"
	"arkoren.dev/cmd/arkoren-ci/internal/release"
)

// repeated collects explicit new-module intents without introducing aliases.
type repeated []string

// String provides standard flag help formatting.
func (values *repeated) String() string { return fmt.Sprint([]string(*values)) }

// Set retains each distinct-intent candidate for planner validation.
func (values *repeated) Set(value string) error {
	*values = append(*values, value)

	return nil
}

// releaseCommand composes the implemented read-only operations and flags.
func releaseCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)

		return err
	}
	if args[0] == "tool" && len(args) == 1 {
		result, err := release.Tool()

		if err != nil {
			return err
		}

		return emit(stdout, result)
	}
	if args[0] != "plan" && args[0] != "evidence" {
		return fmt.Errorf("unknown release command")
	}
	flags := flag.NewFlagSet("release "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("repo", ".", "candidate checkout directory")
	output := flags.String("output", "", "output path outside source checkout")
	sha, attachment := "", ""
	initial := repeated{}
	historical := false
	if args[0] == "plan" {
		flags.StringVar(&sha, "sha", "", "exact full commit SHA")
		flags.StringVar(&attachment, "evidence", "", "validated exact-SHA evidence file")
		flags.Var(&initial, "initial-module", "explicit new public module intent (repeatable)")
		flags.BoolVar(&historical, "historical-v1", false, "recompute historical signed-v1 inventory; not current policy approval")
	}
	err := flags.Parse(args[1:])

	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(stdout, usage)

		return err
	}
	if err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" {
		return fmt.Errorf("release operations require --output and accept flags only")
	}
	absolute, err := filepath.Abs(*root)

	if err != nil {
		return err
	}
	checkout, err := process.Run(ctx, absolute, "git", "", false, "rev-parse", "--show-toplevel")

	if err != nil {
		return err
	}
	absolute = strings.TrimSpace(string(checkout))
	if args[0] == "evidence" {
		return collectEvidence(ctx, absolute, *output, stdout)
	}
	prepare := release.Prepare
	if historical {
		prepare = release.PrepareHistorical
	}
	result, err := prepare(ctx, absolute, sha, initial)

	if err != nil {
		return err
	}
	if attachment != "" {
		data, err := os.ReadFile(attachment)

		if err != nil {
			return err
		}
		verified := evidence.Result{}
		if err := json.Unmarshal(data, &verified); err != nil {
			return err
		}
		if verified.SHA != sha || !verified.Validated {
			return fmt.Errorf("CI evidence does not validate planned SHA")
		}
		result.Validation = "CI gate and website succeeded at this exact SHA"
		result.Evidence = verified
	}
	if err := release.Write(absolute, *output, result); err != nil {
		return err
	}

	return emit(stdout, result)
}

// collectEvidence validates trigger and checkout before invoking authenticated gh.
func collectEvidence(ctx context.Context, root, output string, stdout io.Writer) error {
	data, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))

	if err != nil {
		return err
	}
	event := evidence.Event{}
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("invalid workflow event")
	}
	target, err := evidence.SelectTarget(os.Getenv("GITHUB_EVENT_NAME"), event, os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_REF"))

	if err != nil {
		return err
	}
	if err := evidence.ValidateCheckout(ctx, root, target); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	result, err := evidence.Collect(ctx, target, evidence.GitHub(root), evidence.Delay)

	if err != nil {
		return err
	}
	data, err = json.Marshal(result)

	if err != nil {
		return err
	}
	if err := os.WriteFile(output, append(data, '\n'), 0o644); err != nil {
		return err
	}

	return emit(stdout, result)
}

// emit writes exactly one JSON result and preserves stdout failures.
func emit(stdout io.Writer, value any) error {
	data, err := json.Marshal(value)

	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))

	return err
}
