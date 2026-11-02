package evidence_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/evidence"
)

const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func target() evidence.Target {
	return evidence.Target{SHA: sha, Repository: "arkoren/arkoren", Branch: "main", Mode: "workflow_run"}
}

func run(workflow string) evidence.Run {
	return evidence.Run{ID: 1, Attempt: 1, SHA: sha, Branch: "main", Repository: evidence.Repository{FullName: "arkoren/arkoren"}, Path: ".github/workflows/" + workflow, Event: "push", Status: "completed", Conclusion: "success", URL: "https://github.com/arkoren/arkoren/actions/runs/1"}
}

func event() evidence.Event {
	return evidence.Event{Repository: evidence.Repository{FullName: "arkoren/arkoren"}, Run: run("ci.yml")}
}

func TestSelectTargetUsesUpstreamNotDefaultTip(t *testing.T) {
	result, err := evidence.SelectTarget("workflow_run", event(), "arkoren/arkoren", "refs/heads/main")
	if err != nil || result.SHA != sha {
		t.Fatalf("target %+v: %v", result, err)
	}
}

func TestSelectTargetRejectsFork(t *testing.T) {
	value := event()
	value.Run.Repository.FullName = "fork/repo"
	if _, err := evidence.SelectTarget("workflow_run", value, "arkoren/arkoren", "refs/heads/main"); err == nil {
		t.Fatal("fork trusted")
	}
}

func TestSelectTargetRejectsWrongWorkflow(t *testing.T) {
	value := event()
	value.Run.Path = ".github/workflows/other.yml"
	if _, err := evidence.SelectTarget("workflow_run", value, "arkoren/arkoren", "refs/heads/main"); err == nil {
		t.Fatal("other workflow trusted")
	}
}

func TestSelectTargetRejectsPRAndFailedRun(t *testing.T) {
	value := event()
	value.Run.Event = "pull_request"
	if _, err := evidence.SelectTarget("workflow_run", value, "arkoren/arkoren", "refs/heads/main"); err == nil {
		t.Fatal("PR trusted")
	}
	value = event()
	value.Run.Conclusion = "failure"
	if _, err := evidence.SelectTarget("workflow_run", value, "arkoren/arkoren", "refs/heads/main"); err == nil {
		t.Fatal("failure trusted")
	}
}

func TestSelectTargetManualRequiresExactSHAAndBranch(t *testing.T) {
	value := event()
	value.Inputs.SHA = sha
	result, err := evidence.SelectTarget("workflow_dispatch", value, "arkoren/arkoren", "refs/heads/feature")
	if err != nil || result.Branch != "feature" {
		t.Fatalf("manual %+v %v", result, err)
	}
	value.Inputs.SHA = "main"
	if _, err := evidence.SelectTarget("workflow_dispatch", value, "arkoren/arkoren", "refs/heads/feature"); err == nil {
		t.Fatal("ref accepted")
	}
	value.Inputs.SHA = sha
	if _, err := evidence.SelectTarget("workflow_dispatch", value, "arkoren/arkoren", "refs/tags/core/v0.1.0"); err == nil {
		t.Fatal("tag branch accepted")
	}
}

func TestSelectRunLaterFailureBlocksEarlierGreen(t *testing.T) {
	old, later := run("ci.yml"), run("ci.yml")
	later.ID, later.Conclusion = 2, "failure"
	if _, err := evidence.SelectRun([]evidence.Run{old, later}, target(), "ci.yml"); err == nil {
		t.Fatal("earlier green hid failure")
	}
}

func TestSelectRunLatestAttemptBlocksEarlierGreen(t *testing.T) {
	old, later := run("ci.yml"), run("ci.yml")
	later.Attempt, later.Status, later.Conclusion = 2, "in_progress", ""
	result, err := evidence.SelectRun([]evidence.Run{later, old}, target(), "ci.yml")
	if err != nil || result.Attempt != 2 || result.Status != "in_progress" {
		t.Fatalf("attempt %+v %v", result, err)
	}
	later.Status, later.Conclusion = "completed", "failure"
	if _, err := evidence.SelectRun([]evidence.Run{old, later}, target(), "ci.yml"); err == nil {
		t.Fatal("canceled attempt accepted")
	}
}

func TestSelectRunRejectsStaleSHAAndNonMainPush(t *testing.T) {
	value := run("ci.yml")
	value.SHA = strings.Repeat("b", 40)
	result, err := evidence.SelectRun([]evidence.Run{value}, target(), "ci.yml")
	if err != nil || result != nil {
		t.Fatal("stale SHA accepted")
	}
	value = run("ci.yml")
	value.Branch = "feature"
	selected := target()
	selected.Branch = "feature"
	result, err = evidence.SelectRun([]evidence.Run{value}, selected, "ci.yml")
	if err != nil || result != nil {
		t.Fatal("feature push accepted")
	}
}

func TestRequiredJobRejectsMissingDuplicateStaleAndNonSuccess(t *testing.T) {
	job := evidence.Job{Name: "CI gate", SHA: sha, Status: "completed", Conclusion: "success"}
	if _, err := evidence.RequiredJob(nil, "CI gate", sha); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := evidence.RequiredJob([]evidence.Job{job, job}, "CI gate", sha); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := evidence.RequiredJob([]evidence.Job{job}, "CI gate", strings.Repeat("b", 40)); err == nil {
		t.Fatal("stale accepted")
	}
	job.Conclusion = "neutral"
	if _, err := evidence.RequiredJob([]evidence.Job{job}, "CI gate", sha); err == nil {
		t.Fatal("neutral accepted")
	}
	job.Conclusion, job.Status = "success", "queued"
	if _, err := evidence.RequiredJob([]evidence.Job{job}, "CI gate", sha); err == nil {
		t.Fatal("queued accepted")
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCollectReadsEveryPageAndExactAttemptJobs(t *testing.T) {
	read := func(_ context.Context, path string) ([]byte, error) {
		if strings.Contains(path, "/runs?") {
			workflow := "ci.yml"
			if strings.Contains(path, "website.yml") {
				workflow = "website.yml"
			}
			old, latest := run(workflow), run(workflow)
			latest.ID, latest.Attempt = 3, 2
			return encode(t, []any{map[string]any{"workflow_runs": []evidence.Run{old}}, map[string]any{"workflow_runs": []evidence.Run{latest}}}), nil
		}
		if !strings.Contains(path, "/runs/3/attempts/2/jobs?per_page=100") {
			t.Fatalf("wrong attempt path: %s", path)
		}
		return encode(t, []any{map[string]any{"jobs": []evidence.Job{}}, map[string]any{"jobs": []evidence.Job{{Name: "CI gate", SHA: sha, Status: "completed", Conclusion: "success"}, {Name: "website", SHA: sha, Status: "completed", Conclusion: "success"}}}}), nil
	}
	result, err := evidence.Collect(context.Background(), target(), read, func(context.Context, time.Duration) error { t.Fatal("unexpected wait"); return nil })
	if err != nil || !result.Validated || result.Workflows["ci.yml"].Attempt != 2 {
		t.Fatalf("result %+v %v", result, err)
	}
}

func TestCollectPendingThenSuccessRefreshesBothWorkflows(t *testing.T) {
	poll, calls := 0, 0
	read := func(_ context.Context, path string) ([]byte, error) {
		calls++
		if strings.Contains(path, "/runs?") {
			workflow := "ci.yml"
			if strings.Contains(path, "website.yml") {
				workflow = "website.yml"
			}
			value := run(workflow)
			if poll == 0 && workflow == "website.yml" {
				value.Status, value.Conclusion = "queued", ""
			}
			return encode(t, []any{map[string]any{"workflow_runs": []evidence.Run{value}}}), nil
		}
		return []byte(`[{"jobs":[{"name":"CI gate","head_sha":"` + sha + `","status":"completed","conclusion":"success"},{"name":"website","head_sha":"` + sha + `","status":"completed","conclusion":"success"}]}]`), nil
	}
	wait := func(_ context.Context, duration time.Duration) error {
		if duration != 15*time.Second {
			t.Fatal("wrong interval")
		}
		poll++
		return nil
	}
	result, err := evidence.Collect(context.Background(), target(), read, wait)
	if err != nil || !result.Validated || poll != 1 || calls != 7 {
		t.Fatalf("polls %d calls %d: %v", poll, calls, err)
	}
}

func TestCollectMissingWorkflowReachesDeadlineWithoutSleeping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reads, waits := 0, 0
	read := func(context.Context, string) ([]byte, error) { reads++; return []byte(`[{"workflow_runs":[]}]`), nil }
	wait := func(context.Context, time.Duration) error { waits++; cancel(); return nil }
	_, err := evidence.Collect(ctx, target(), read, wait)
	if !errors.Is(err, context.Canceled) || reads != 2 || waits != 1 {
		t.Fatalf("deadline: %d %d %v", reads, waits, err)
	}
}

func TestCollectFailureDoesNotPoll(t *testing.T) {
	read := func(context.Context, string) ([]byte, error) {
		value := run("ci.yml")
		value.Conclusion = "failure"
		return encode(t, []any{map[string]any{"workflow_runs": []evidence.Run{value}}}), nil
	}
	_, err := evidence.Collect(context.Background(), target(), read, func(context.Context, time.Duration) error { t.Fatal("failure polled"); return nil })
	if err == nil {
		t.Fatal("failure accepted")
	}
}

func TestCollectPendingWorkflowDeadlineIsBounded(t *testing.T) {
	reads, waits := 0, 0
	read := func(context.Context, string) ([]byte, error) {
		reads++
		return []byte(`[{"workflow_runs":[]}]`), nil
	}
	wait := func(context.Context, time.Duration) error {
		waits++
		return context.DeadlineExceeded
	}
	_, err := evidence.Collect(context.Background(), target(), read, wait)
	if !errors.Is(err, context.DeadlineExceeded) || reads != 2 || waits != 1 {
		t.Fatalf("unbounded pending poll: reads=%d waits=%d err=%v", reads, waits, err)
	}
}

func TestCollectMalformedResponseIsSafe(t *testing.T) {
	_, err := evidence.Collect(context.Background(), target(), func(context.Context, string) ([]byte, error) { return []byte(`secret-invalid-response`), nil }, evidence.Delay)
	if err == nil || strings.Contains(fmt.Sprint(err), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}
