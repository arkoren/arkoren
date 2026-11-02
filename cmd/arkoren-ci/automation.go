package main

import (
	"context"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/evidence"
	"arkoren.dev/cmd/arkoren-ci/internal/github"
	"arkoren.dev/cmd/arkoren-ci/internal/notes"
	"arkoren.dev/cmd/arkoren-ci/internal/process"
	"arkoren.dev/cmd/arkoren-ci/internal/publication"
	"arkoren.dev/cmd/arkoren-ci/internal/tagpolicy"
)

// automationCommand keeps privileged phases explicit rather than registering tasks.
func automationCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("release automation requires an explicit phase")
	}
	flags := flag.NewFlagSet("release "+args[0]+" "+args[1], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("repo", ".", "reviewed checkout root")
	manifest := flags.String("manifest", "", "prepared state file")
	output := flags.String("output", "", "new result or verification file")
	proof := flags.String("verification", "", "public-consumer verification receipt")
	locked := flags.Bool("locked", false, "expect the temporary global creation lock")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("automation accepts flags only")
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
	client := github.Client{Root: absolute}
	switch args[0] {
	case "notes":
		return notesCommand(ctx, client, args[1], *manifest, *output, stdout)
	case "policy":
		if args[1] == "check" {
			result, err := tagpolicy.Check(ctx, client, *locked)
			if err != nil {
				return err
			}
			if *output != "" {
				if err := writeState(*output, result); err != nil {
					return err
				}
			}
			return emit(stdout, map[string]any{"validated": true, "creation_locked": *locked})
		}
		if args[1] != "activate" || *output == "" {
			return fmt.Errorf("policy requires check or activate --output NEW_DIRECTORY")
		}
		// Activation is a separate administrative operation, not a publisher step.
		head, err := process.Run(ctx, absolute, "git", "", false, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		target := evidence.Target{SHA: strings.TrimSpace(string(head)), Branch: "main", Repository: github.Repository, Mode: "tag-policy-activation"}
		if err := evidence.ValidateCheckout(ctx, absolute, target); err != nil {
			return err
		}
		if _, err := evidence.Collect(ctx, target, evidence.GitHub(absolute), evidence.Delay); err != nil {
			return err
		}
		if err := tagpolicy.Activate(ctx, client, *output); err != nil {
			return err
		}
		return emit(stdout, map[string]any{"activated": true, "snapshot": *output})
	case "publish":
		return publishCommand(ctx, client, args[1], *manifest, *output, *proof, stdout)
	default:
		return fmt.Errorf("unknown release automation command")
	}
}

// readState rejects unknown fields; manifests are data, never execution sources.
func readState(path string, destination any) error {
	if path == "" {
		return fmt.Errorf("provide --manifest or --verification")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, destination, json.RejectUnknownMembers(true))
}

// writeState uses exclusive creation so prior evidence cannot be silently replaced.
func writeState(path string, value any) error {
	if path == "" {
		return fmt.Errorf("provide --output")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	closeErr := file.Close()
	if err != nil {
		return err
	}

	return closeErr
}

// changed writes only a bounded boolean for native workflow conditions.
func changed(value bool) error {
	if os.Getenv("GITHUB_OUTPUT") == "" {
		return nil
	}
	file, err := os.OpenFile(os.Getenv("GITHUB_OUTPUT"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "changed=%t\n", value)
	closeErr := file.Close()
	if err != nil {
		return err
	}

	return closeErr
}

// notesCommand obtains no App access for a no-op import or read-only inspection.
func notesCommand(ctx context.Context, client github.Client, mode, manifest, output string, stdout io.Writer) error {
	switch mode {
	case "prepare":
		state, err := notes.Prepare(ctx, client.Root)
		if err != nil {
			return err
		}
		if err := writeState(output, state); err != nil {
			return err
		}
		if err := changed(len(state.Records) > 0); err != nil {
			return err
		}
		return emit(stdout, map[string]any{"records": len(state.Records), "changed": len(state.Records) > 0})
	case "verify":
		if err := client.VerifyApp(ctx, os.Getenv("APP_SLUG"), os.Getenv("INSTALLATION_ID")); err != nil {
			return err
		}
		if err := notes.Verify(ctx, client); err != nil {
			return err
		}
		return emit(stdout, map[string]bool{"verified": true, "writes": false})
	case "inspect", "publish":
		state := notes.State{}
		if err := readState(manifest, &state); err != nil {
			return err
		}
		if mode == "inspect" {
			_, _, err := notes.Inspect(ctx, client, state)
			if err != nil {
				return err
			}
			return emit(stdout, map[string]bool{"owned": true, "writes": false})
		}
		if len(state.Records) > 0 {
			if err := client.VerifyApp(ctx, os.Getenv("APP_SLUG"), os.Getenv("INSTALLATION_ID")); err != nil {
				return err
			}
		}
		message, err := notes.Publish(ctx, client, state)
		if err != nil {
			return err
		}
		return emit(stdout, map[string]string{"message": message})
	default:
		return fmt.Errorf("notes phase must be prepare, inspect, verify or publish")
	}
}

// publicationTarget validates repository, branch and exact event candidate.
func publicationTarget() (evidence.Target, error) {
	if os.Getenv("GITHUB_REPOSITORY") != github.Repository || os.Getenv("GITHUB_REF") != "refs/heads/main" {
		return evidence.Target{}, fmt.Errorf("publisher runs only on arkoren/arkoren main")
	}
	data, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return evidence.Target{}, err
	}
	event := evidence.Event{}
	if err := json.Unmarshal(data, &event); err != nil {
		return evidence.Target{}, err
	}

	return evidence.SelectTarget(os.Getenv("GITHUB_EVENT_NAME"), event, github.Repository, "refs/heads/main")
}

// verification binds a same-job public-build receipt to source code and full plan.
type verification struct {
	Manifest string `json:"manifest"`
	Code     string `json:"code"`
}

// publishCommand separates short-lived writes from potentially long public builds.
func publishCommand(ctx context.Context, client github.Client, mode, manifest, output, proof string, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 110*time.Minute)
	defer cancel()
	target, err := publicationTarget()
	if err != nil {
		return err
	}
	trust := filepath.Join(client.Root, ".github/release-signing.allowed-signers")
	if mode == "prepare" {
		state, err := publication.Prepare(ctx, client, trust, target.SHA)
		if err != nil {
			return err
		}
		if err := writeState(output, state); err != nil {
			return err
		}
		if err := changed(len(state.Batches) > 0); err != nil {
			return err
		}
		return emit(stdout, map[string]any{"candidate": target.SHA, "batches": len(state.Batches), "changed": len(state.Batches) > 0})
	}
	state := publication.Manifest{}
	if err := readState(manifest, &state); err != nil {
		return err
	}
	if state.Candidate != target.SHA {
		return fmt.Errorf("manifest target differs from trusted event")
	}
	if err := publication.ValidateManifest(ctx, client, trust, state); err != nil {
		return err
	}
	app := github.Client{Root: client.Root, Token: os.Getenv("APP_TOKEN")}
	if mode == "preflight" || mode == "tags" || mode == "releases" {
		if app.Token == "" {
			return fmt.Errorf("write phase requires a fresh scoped App token")
		}
		if err := app.VerifyApp(ctx, os.Getenv("APP_SLUG"), os.Getenv("INSTALLATION_ID")); err != nil {
			return err
		}
	}
	if mode == "preflight" {
		if err := publication.Preflight(ctx, trust, os.Getenv("SIGNING_KEY")); err != nil {
			return err
		}
		return emit(stdout, map[string]bool{"app_verified": true, "signer_verified": true, "remote_writes": false})
	}
	encoded, err := publication.Canonical(state)
	if err != nil {
		return err
	}
	head, err := process.Run(ctx, client.Root, "git", "", false, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	receipt := verification{Manifest: encoded, Code: strings.TrimSpace(string(head))}
	if mode == "releases" {
		actual := verification{}
		if err := readState(proof, &actual); err != nil {
			return err
		}
		if actual != receipt {
			return fmt.Errorf("public-build receipt differs from current code or signed batches")
		}
	}
	for _, batch := range state.Batches {
		switch mode {
		case "tags":
			// Recheck main/evidence immediately before each recovered batch write.
			if err := publication.ValidateManifest(ctx, client, trust, publication.Manifest{Candidate: state.Candidate, Batches: []publication.Batch{batch}}); err != nil {
				return err
			}
			if err := publication.PushTags(ctx, client.Root, trust, batch, os.Getenv("SIGNING_KEY"), app.Token); err != nil {
				return err
			}
		case "verify", "releases":
			complete, err := publication.Reconcile(ctx, client.Root, trust, batch)
			if err != nil {
				return err
			}
			if !complete {
				return fmt.Errorf("remote signed batch is incomplete; do not probe future versions")
			}
			if mode == "verify" {
				if err := publication.PublicVerify(ctx, client.Root, batch); err != nil {
					return err
				}
			} else if err := publication.PublishReleases(ctx, app, batch); err != nil {
				return err
			}
		default:
			return fmt.Errorf("publish phase must be prepare, preflight, tags, verify or releases")
		}
	}
	if mode != "tags" && mode != "verify" && mode != "releases" {
		return fmt.Errorf("unknown publication phase")
	}
	if mode == "verify" {
		if err := writeState(output, receipt); err != nil {
			return err
		}
	}

	return emit(stdout, map[string]any{"phase": mode, "batches": len(state.Batches), "completed": true})
}
