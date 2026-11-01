// Package evidence requires trusted workflow and job metadata at one exact SHA.
package evidence

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Repository identifies the event's source repository.
type Repository struct {
	// FullName is the owner/name identity supplied by GitHub.
	FullName string `json:"full_name"`
}

// Run is the trusted subset of GitHub workflow-run metadata.
type Run struct {
	// ID orders distinct runs, including replacement manual runs.
	ID int64 `json:"id"`
	// Attempt orders reruns of the same run.
	Attempt int64 `json:"run_attempt"`
	// SHA is the tested commit, not the default branch tip.
	SHA string `json:"head_sha"`
	// Branch identifies the tested branch.
	Branch string `json:"head_branch"`
	// Repository rejects fork evidence.
	Repository Repository `json:"head_repository"`
	// Path identifies the actual workflow file.
	Path string `json:"path"`
	// Event distinguishes trusted pushes and dispatches from PR execution.
	Event string `json:"event"`
	// Status distinguishes terminal and pending runs.
	Status string `json:"status"`
	// Conclusion must be exactly success for terminal runs.
	Conclusion string `json:"conclusion"`
	// URL identifies the selected run for the UI.
	URL string `json:"html_url"`
}

// Event carries only target-selection metadata, never credentials.
type Event struct {
	// Repository is the event's own repository identity.
	Repository Repository `json:"repository"`
	// Run is populated for workflow_run triggers.
	Run Run `json:"workflow_run"`
	// Inputs supplies the exact manually requested SHA.
	Inputs struct {
		SHA string `json:"sha"`
	} `json:"inputs"`
}

// Target records the validated trigger's immutable target.
type Target struct {
	// SHA is always a full commit ID.
	SHA string
	// Branch is main for automatic triggers or the selected dispatch branch.
	Branch string
	// Repository is the validated owner/name.
	Repository string
	// Mode is the original event name.
	Mode string
}

// Job is the required subset of attempt-specific GitHub jobs.
type Job struct {
	// Name identifies the required gate.
	Name string `json:"name"`
	// SHA identifies the job's tested commit.
	SHA string `json:"head_sha"`
	// Status must be completed.
	Status string `json:"status"`
	// Conclusion must be exactly success.
	Conclusion string `json:"conclusion"`
	// URL identifies the required job for the UI.
	URL string `json:"html_url"`
}

// Workflow is the original compact evidence artifact for one selected attempt.
type Workflow struct {
	// RunID identifies the selected run.
	RunID int64 `json:"run_id"`
	// Attempt identifies the selected attempt.
	Attempt int64 `json:"run_attempt"`
	// URL points to the selected run.
	URL string `json:"url"`
	// Job is the exact required job name.
	Job string `json:"job"`
	// JobURL points to the selected required job.
	JobURL string `json:"job_url"`
}

// Result preserves the exact-SHA evidence artifact schema.
type Result struct {
	// Validated is true only after both required workflows and jobs succeed.
	Validated bool `json:"validated"`
	// SHA is the exact immutable target.
	SHA string `json:"sha"`
	// Branch identifies the validated branch.
	Branch string `json:"branch"`
	// Repository identifies the validated repository.
	Repository string `json:"repository"`
	// Mode identifies the trigger type.
	Mode string `json:"mode"`
	// Workflows records both selected runs and required jobs.
	Workflows map[string]Workflow `json:"workflows"`
}

// fullSHA accepts exact immutable commit IDs only.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// repositoryName bounds repository names before constructing API paths.
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// SelectTarget deliberately does not accept GITHUB_SHA: workflow_run sets that
// variable to the default branch tip, not necessarily the tested commit.
func SelectTarget(name string, event Event, repository, ref string) (Target, error) {
	if !repositoryName.MatchString(repository) || event.Repository.FullName != repository {
		return Target{}, fmt.Errorf("event repository mismatch")
	}
	target := Target{Repository: repository, Mode: name}
	switch name {
	case "workflow_run":
		run := event.Run
		if run.Repository.FullName != repository || run.Branch != "main" || run.Event != "push" ||
			run.Path != ".github/workflows/ci.yml" || run.Status != "completed" || run.Conclusion != "success" {
			return Target{}, fmt.Errorf("automatic preview requires successful trusted main push CI")
		}
		target.SHA, target.Branch = run.SHA, "main"
	case "workflow_dispatch":
		branch, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok || branch == "" {
			return Target{}, fmt.Errorf("dispatch requires a selected branch")
		}
		target.SHA, target.Branch = event.Inputs.SHA, branch
	default:
		return Target{}, fmt.Errorf("unsupported preview event")
	}
	if !fullSHA.MatchString(target.SHA) {
		return Target{}, fmt.Errorf("an exact full commit SHA is required")
	}

	return target, nil
}

// SelectRun chooses the latest eligible run and attempt, never any earlier green.
func SelectRun(runs []Run, target Target, workflow string) (*Run, error) {
	var latest *Run
	for _, run := range runs {
		trustedEvent := run.Event == "workflow_dispatch" || (target.Branch == "main" && run.Event == "push")
		if run.SHA != target.SHA || run.Branch != target.Branch || run.Repository.FullName != target.Repository ||
			run.Path != ".github/workflows/"+workflow || !trustedEvent {
			continue
		}
		if run.ID <= 0 || run.Attempt <= 0 {
			return nil, fmt.Errorf("invalid workflow identity")
		}
		if latest == nil || run.ID > latest.ID || (run.ID == latest.ID && run.Attempt > latest.Attempt) {
			latest = &run
		}
	}
	if latest != nil && latest.Status == "completed" && latest.Conclusion != "success" {
		return nil, fmt.Errorf("%s did not succeed at %s", workflow, target.SHA)
	}

	return latest, nil
}

// RequiredJob rejects absent, duplicate, mismatched and unsuccessful job records.
func RequiredJob(jobs []Job, name, sha string) (Job, error) {
	matches := []Job{}
	for _, job := range jobs {
		if job.Name == name {
			matches = append(matches, job)
		}
	}
	if len(matches) != 1 || matches[0].SHA != sha || matches[0].Status != "completed" || matches[0].Conclusion != "success" {
		return Job{}, fmt.Errorf("missing, ambiguous or unsuccessful required job %s at %s", name, sha)
	}

	return matches[0], nil
}

// Reader owns paginated GET transport; it never fetches artifacts or caches.
type Reader func(context.Context, string) ([]byte, error)

// Wait owns the bounded polling delay and is replaceable by tests without sleep.
type Wait func(context.Context, time.Duration) error

// Collect refreshes both workflow identities on every poll and uses exact-attempt
// jobs. The caller owns the total deadline; no stale partial result is retained.
func Collect(ctx context.Context, target Target, read Reader, wait Wait) (Result, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("timed out waiting for exact-SHA CI gate AND website: %s: %w", target.SHA, err)
		}
		workflows := map[string]Workflow{}
		for _, workflow := range []string{"ci.yml", "website.yml"} {
			query := url.Values{"head_sha": {target.SHA}, "branch": {target.Branch}, "per_page": {"100"}}
			data, err := read(ctx, "repos/"+target.Repository+"/actions/workflows/"+workflow+"/runs?"+query.Encode())

			if err != nil {
				return Result{}, fmt.Errorf("read workflow metadata: %w", err)
			}
			runs, err := decodeRuns(data)

			if err != nil {
				return Result{}, err
			}
			run, err := SelectRun(runs, target, workflow)

			if err != nil {
				return Result{}, err
			}
			if run == nil || run.Status != "completed" {
				continue
			}
			data, err = read(ctx, fmt.Sprintf("repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", target.Repository, run.ID, run.Attempt))

			if err != nil {
				return Result{}, fmt.Errorf("read job metadata: %w", err)
			}
			jobs, err := decodeJobs(data)

			if err != nil {
				return Result{}, err
			}
			name := "website"
			if workflow == "ci.yml" {
				name = "CI gate"
			}
			job, err := RequiredJob(jobs, name, target.SHA)

			if err != nil {
				return Result{}, err
			}
			workflows[workflow] = Workflow{RunID: run.ID, Attempt: run.Attempt, URL: run.URL, Job: job.Name, JobURL: job.URL}
		}
		if len(workflows) == 2 {
			return Result{Validated: true, SHA: target.SHA, Branch: target.Branch,
				Repository: target.Repository, Mode: target.Mode, Workflows: workflows}, nil
		}
		if err := wait(ctx, 15*time.Second); err != nil {
			return Result{}, fmt.Errorf("waiting for exact-SHA evidence at %s: %w", target.SHA, err)
		}
	}
}

// decodeRuns flattens every gh --paginate --slurp response page.
func decodeRuns(data []byte) ([]Run, error) {
	pages := []struct {
		Runs []Run `json:"workflow_runs"`
	}{}
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid workflow metadata")
	}
	result := []Run{}
	for _, page := range pages {
		result = append(result, page.Runs...)
	}

	return result, nil
}

// decodeJobs flattens every attempt-specific jobs response page.
func decodeJobs(data []byte) ([]Job, error) {
	pages := []struct {
		Jobs []Job `json:"jobs"`
	}{}
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid job metadata")
	}
	result := []Job{}
	for _, page := range pages {
		result = append(result, page.Jobs...)
	}

	return result, nil
}
