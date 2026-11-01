package restateworkflow_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
)

// TestOwnershipDiagnosticsCaptureBeforeCleanup executes an intentionally failed
// child test process, proving the real t.Cleanup gate captures state before the
// fixture's kill/stop cleanup. The parent test remains successful.
//
//	go test -run '^TestOwnershipDiagnosticsCaptureBeforeCleanup$' ./restateworkflow
func TestOwnershipDiagnosticsCaptureBeforeCleanup(t *testing.T) {
	if os.Getenv("ARKOREN_TEST_OWNERSHIP_DIAGNOSTICS") == "failure" {
		t.Cleanup(func() {
			t.Log("fixture cleanup")
		})
		trace := &ownershipTrace{started: time.Now()}
		trace.record("entry-return run=fixture-child success=true context=<nil>")
		registerOwnershipDiagnostics(t, trace, func(ctx context.Context, _ string) ([]map[string]any, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("diagnostic query has no deadline")
			}

			return []map[string]any{{"id": "inv_fixture"}}, nil
		})
		t.Error("forced diagnostic failure")
		return
	}

	binary, err := os.Executable()

	if err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), binary,
		"-test.run=^TestOwnershipDiagnosticsCaptureBeforeCleanup$", "-test.v")
	command.Env = append(os.Environ(), "ARKOREN_TEST_OWNERSHIP_DIAGNOSTICS=failure")
	output, err := command.CombinedOutput()
	var exit *exec.ExitError

	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("child test must fail normally: %v\n%s", err, output)
	}

	text := string(output)
	state := strings.Index(text, "ownership state query 1:")
	cleanup := strings.Index(text, "fixture cleanup")

	if state < 0 || cleanup < state {
		t.Fatalf("state was not captured before cleanup:\n%s", output)
	}

	if !strings.Contains(text, "entry-return run=fixture-child") || !strings.Contains(text, "ownership goroutines") {
		t.Fatalf("missing execution history or stacks:\n%s", output)
	}
}

// TestOwnershipDiagnosticsSuccessDoesNotQuery verifies that a successful
// fixture performs no diagnostic I/O, even when its cleanup runs normally.
//
//	go test -run '^TestOwnershipDiagnosticsSuccessDoesNotQuery$' ./restateworkflow
func TestOwnershipDiagnosticsSuccessDoesNotQuery(t *testing.T) {
	trace := &ownershipTrace{started: time.Now()}
	registerOwnershipDiagnostics(t, trace, func(context.Context, string) ([]map[string]any, error) {
		t.Error("successful test queried diagnostic state")

		return nil, nil
	})
}

// TestOwnershipDiagnosticsHistoryBoundedConcurrent exercises callbacks from
// concurrent invocations and makes loss visible after the fixed history limit.
//
//	go test -race -run '^TestOwnershipDiagnosticsHistoryBoundedConcurrent$' ./restateworkflow
func TestOwnershipDiagnosticsHistoryBoundedConcurrent(t *testing.T) {
	t.Parallel()
	trace := &ownershipTrace{started: time.Now()}
	var workers sync.WaitGroup

	for range 16 {
		workers.Go(func() {
			for range 100 {
				trace.record("entry-enter run=fixture-child")
			}
		})
	}
	workers.Wait()
	history, dropped := trace.snapshot()

	if len(history) != 128 || dropped != 1472 {
		t.Fatalf("history=%d dropped=%d; want 128 and 1472", len(history), dropped)
	}

	history[0] = "changed snapshot"
	again, _ := trace.snapshot()

	if again[0] == history[0] {
		t.Fatal("snapshot aliases mutable history")
	}
}

// TestOwnershipDiagnosticsEntryPreservesWorkAndPolicy proves duplicate original
// executions remain duplicates, rather than being deduplicated by instrumentation.
// The wrapper retains every completed retry/timeout setting.
//
//	go test -run '^TestOwnershipDiagnosticsEntryPreservesWorkAndPolicy$' ./restateworkflow
func TestOwnershipDiagnosticsEntryPreservesWorkAndPolicy(t *testing.T) {
	trace := &ownershipTrace{started: time.Now()}
	var calls atomic.Int32
	step := workflow.NewStep("conf-ownership-entry", func(_ context.Context, id string) (string, error) {
		calls.Add(1)

		return id, nil
	}, func(entry *workflow.StepBuilder) {
		entry.Retry(3)
		entry.Backoff(time.Second, 2, 4*time.Second)
		entry.Timeout(5 * time.Second)
	})
	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}
	original, _ := registry.Step(step.Name())
	wrapped, err := workflow.Handlers(tracedOwnershipEntry(t, step, trace))

	if err != nil {
		t.Fatal(err)
	}
	handler, _ := wrapped.Step(step.Name())

	if handler.Options != original.Options {
		t.Fatalf("policy changed: %+v != %+v", handler.Options, original.Options)
	}

	for range 2 {
		output, err := handler.Handle(t.Context(), []byte(`{"v":"fixture-child"}`))

		if err != nil || string(output) != `{"v":"fixture-child"}` {
			t.Fatalf("original entry output=%s err=%v", output, err)
		}
	}
	history, _ := trace.snapshot()

	completions := strings.Count(strings.Join(history, "\n"), "entry-return run=fixture-child success=true")

	if calls.Load() != 2 || completions != 2 {
		t.Fatalf("original calls=%d, history=%v; want two observable completions", calls.Load(), history)
	}
}

// TestOwnershipDiagnosticsEntryPreservesCancellation requires an original
// callback failure to remain the caller's error and retain its context state.
// No error message is copied into the diagnostic history.
//
//	go test -run '^TestOwnershipDiagnosticsEntryPreservesCancellation$' ./restateworkflow
func TestOwnershipDiagnosticsEntryPreservesCancellation(t *testing.T) {
	trace := &ownershipTrace{started: time.Now()}
	step := workflow.NewStep("conf-ownership-entry", func(ctx context.Context, _ string) (string, error) {
		return "", ctx.Err()
	})
	registry, err := workflow.Handlers(tracedOwnershipEntry(t, step, trace))

	if err != nil {
		t.Fatal(err)
	}
	handler, _ := registry.Step(step.Name())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := handler.Handle(ctx, []byte(`{"v":"fixture-child"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("original cancellation was changed: %v", err)
	}
	history, _ := trace.snapshot()

	if !strings.Contains(strings.Join(history, "\n"), "success=false context=context canceled") {
		t.Fatalf("missing original callback context state: %v", history)
	}
}

// TestOwnershipDiagnosticsCaptureExcludesSDKErrorText keeps native lifecycle
// correlation while excluding payload-bearing messages and error attributes.
//
//	go test -run '^TestOwnershipDiagnosticsCaptureExcludesSDKErrorText$' ./restateworkflow
func TestOwnershipDiagnosticsCaptureExcludesSDKErrorText(t *testing.T) {
	trace := &ownershipTrace{started: time.Now()}
	logger := ownershipLog{Handler: slog.DiscardHandler, trace: trace}
	view := logger.WithAttrs([]slog.Attr{
		slog.String("method", "ArkorenWf_conf-ownership-child/Run"),
		slog.String("invocationID", "inv_fixture"),
	}).WithGroup("sdk")
	record := slog.NewRecord(time.Now(), slog.LevelWarn, "Invocation returned a terminal failure", 0)
	record.AddAttrs(slog.String("err", "private-response"))

	if err := view.Handle(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	record.Message = "private-response"

	if err := view.Handle(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	history, _ := trace.snapshot()

	if len(history) != 1 || strings.Contains(history[0], "private-response") {
		t.Fatalf("unsafe lifecycle capture: %v", history)
	}

	if !strings.Contains(history[0], "invocation=inv_fixture phase=handler-return-terminal") {
		t.Fatalf("missing native correlation: %v", history)
	}
}

// TestOwnershipDiagnosticsQueryErrorIsSanitized reports failed diagnostics
// without copying server error bodies into the bounded report.
//
//	go test -run '^TestOwnershipDiagnosticsQueryErrorIsSanitized$' ./restateworkflow
func TestOwnershipDiagnosticsQueryErrorIsSanitized(t *testing.T) {
	var output strings.Builder
	ownershipState(t.Context(), func(context.Context, string) ([]map[string]any, error) {
		return nil, errors.New("private-response")
	}, func(format string, values ...any) {
		fmt.Fprintf(&output, format, values...)
	})

	if strings.Contains(output.String(), "private-response") || strings.Count(output.String(), "error_type=") != 2 {
		t.Fatalf("unsafe or missing query errors: %s", output.String())
	}
}

// TestOwnershipDiagnosticsLoggingRestored checks both standard-log bridging
// during capture and restoration before other nonparallel fixtures can log.
//
//	go test -run '^TestOwnershipDiagnosticsLoggingRestored$' ./restateworkflow
func TestOwnershipDiagnosticsLoggingRestored(t *testing.T) {
	var output strings.Builder
	writer := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() {
		log.SetOutput(writer)
	})
	previous, flags := slog.Default(), log.Flags()

	t.Run("capture", func(t *testing.T) {
		captureOwnershipLogs(t, &ownershipTrace{started: time.Now()})
		log.Print("fixture standard log")
		slog.InfoContext(t.Context(), "fixture structured log")
	})

	if slog.Default() != previous || log.Writer() != &output || log.Flags() != flags {
		t.Fatal("fixture did not restore both logging APIs")
	}
	log.Print("fixture log after cleanup")

	if !strings.Contains(output.String(), "fixture log after cleanup") {
		t.Fatal("standard logging did not survive fixture cleanup")
	}
}
