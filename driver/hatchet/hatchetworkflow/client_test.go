package hatchetworkflow_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/hatchet"
	"arkoren.dev/driver/hatchet/hatchetworkflow"
	"arkoren.dev/driver/hatchet/internal/protocoltest"
)

// TestStatusFindsWorkflowBeyondFirstPage uses partial native name matches on
// page one. The execution query must use the exact workflow UUID on page two.
func TestStatusFindsWorkflowBeyondFirstPage(t *testing.T) {
	queried := make(chan string, 1)
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/workflow-runs") {
			queried <- r.URL.Query().Get("workflow_ids")
			_, _ = fmt.Fprint(w, `{"rows":[]}`)

			return
		}

		if r.URL.Query().Get("offset") == "50" {
			_, _ = fmt.Fprint(w, `{"rows":[{"name":"arkoren-wf-order","metadata":{"id":"22222222-2222-2222-2222-222222222222"}}]}`)

			return
		}

		row := `{"name":"arkoren-wf-order-other","metadata":{"id":"11111111-1111-1111-1111-111111111111"}}`
		_, _ = fmt.Fprint(w, `{"rows":[`+strings.TrimSuffix(strings.Repeat(row+",", 50), ",")+`]}`)
	}))
	driver, err := hatchetworkflow.NewDriver(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Status(t.Context(), "order", "missing-run"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status = %v; want missing execution", err)
	}

	select {
	case got := <-queried:
		if got != "22222222-2222-2222-2222-222222222222" {
			t.Fatalf("execution query workflow UUID = %q", got)
		}
	default:
		t.Fatal("Status did not discover the registered workflow on page two")
	}
}

// TestSignalChecksTerminalStateBeforeInput characterizes error precedence: a
// completed execution with corrupt retained input is simply not signalable.
// It must not reach marker submission or return a JSON decoding failure instead.
func TestSignalChecksTerminalStateBeforeInput(t *testing.T) {
	var reads atomic.Int32
	address := protocoltest.GRPC(t, func(_ context.Context, method, _ string) (string, error) {
		if !strings.HasSuffix(method, "/GetRunDetails") {
			t.Errorf("terminal signal reached native mutation: %s", method)
		}

		// Discovery needs valid identity once, then the signal's state read sees
		// a completed row whose input no longer decodes.
		input := `{"id":"order-42","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI"}`

		if reads.Add(1) > 1 {
			input = "not-json"
		}

		return fmt.Sprintf(`{"input":%q,"status":"COMPLETED"}`, base64.StdEncoding.EncodeToString([]byte(input))), nil
	})
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/workflow-runs") {
			_, _ = fmt.Fprint(w, `{"rows":[{"workflowName":"arkoren-wf-order","workflowRunExternalId":"33333333-3333-3333-3333-333333333333","createdAt":"2026-10-01T00:00:00Z"}]}`)

			return
		}

		_, _ = fmt.Fprint(w, `{"rows":[{"name":"arkoren-wf-order","metadata":{"id":"22222222-2222-2222-2222-222222222222"}}]}`)
	}), func(host *hatchet.Builder) {
		host.HostPort(address)
	})
	driver, err := hatchetworkflow.NewDriver(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	err = driver.Signal(t.Context(), "order", "order-42", "approved", []byte(`true`))

	if !errors.Is(err, workflow.ErrNotFound) || !strings.Contains(err.Error(), "run is not running") {
		t.Fatalf("Signal = %v; want terminal-state rejection", err)
	}
}

// TestStatusDoesNotHideNewestIdentityMismatch supplies an older valid execution
// before a newer corrupt one. Discovery sorts by native creation time and must
// fail closed at the newest candidate, not fall back to the older execution.
func TestStatusDoesNotHideNewestIdentityMismatch(t *testing.T) {
	valid := `{"id":"order-42","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI","signal_version":2}`
	address := protocoltest.GRPC(t, func(_ context.Context, _ string, request string) (string, error) {
		input := valid

		if strings.Contains(request, "55555555-5555-5555-5555-555555555555") {
			input = `{"id":"someone-else","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI","signal_version":2}`
		}

		return fmt.Sprintf(`{"input":%q,"status":"COMPLETED"}`, base64.StdEncoding.EncodeToString([]byte(input))), nil
	})
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/workflow-runs") {
			_, _ = fmt.Fprint(w, `{"rows":[{"workflowName":"arkoren-wf-order","workflowRunExternalId":"33333333-3333-3333-3333-333333333333","createdAt":"2026-10-01T00:00:00Z"},{"workflowName":"arkoren-wf-order","workflowRunExternalId":"55555555-5555-5555-5555-555555555555","createdAt":"2026-10-02T00:00:00Z"}]}`)

			return
		}

		_, _ = fmt.Fprint(w, `{"rows":[{"name":"arkoren-wf-order","metadata":{"id":"22222222-2222-2222-2222-222222222222"}}]}`)
	}), func(host *hatchet.Builder) {
		host.HostPort(address)
	})
	driver, err := hatchetworkflow.NewDriver(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Status(t.Context(), "order", "order-42"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status = %v; want newest identity mismatch, not older success", err)
	}
}

// TestResultCapsEveryPollDelay checks a small configured cap at the public wait
// boundary. Real SDK transport goroutines cannot cross a synctest bubble, so a
// caller deadline distinguishes the old 25ms first delay from three 1ms polls.
// No test sleep or detached application goroutine is used.
func TestResultCapsEveryPollDelay(t *testing.T) {
	var requests atomic.Int32
	input := base64.StdEncoding.EncodeToString([]byte(`{"id":"order-42","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI","signal_version":2}`))
	address := protocoltest.GRPC(t, func(context.Context, string, string) (string, error) {
		state := "RUNNING"

		if requests.Add(1) >= 5 {
			state = "COMPLETED"
		}

		return fmt.Sprintf(`{"input":%q,"status":%q}`, input, state), nil
	})
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/workflow-runs") {
			_, _ = fmt.Fprint(w, `{"rows":[{"workflowName":"arkoren-wf-order","workflowRunExternalId":"33333333-3333-3333-3333-333333333333","createdAt":"2026-10-01T00:00:00Z"}]}`)

			return
		}

		_, _ = fmt.Fprint(w, `{"rows":[{"name":"arkoren-wf-order","metadata":{"id":"22222222-2222-2222-2222-222222222222"}}]}`)
	}), func(host *hatchet.Builder) {
		host.HostPort(address)
	})
	driver, err := hatchetworkflow.NewDriver(host, func(driver *hatchetworkflow.DriverBuilder) {
		driver.PollInterval(time.Millisecond)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Warm both native transports outside the timing assertion.
	if _, err := driver.Status(t.Context(), "order", "order-42"); err != nil {
		t.Fatal(err)
	}

	// Permit scheduler stalls under race instrumentation, but never extend an
	// individual budget past the old minimum first delay (20ms with jitter).
	for range 10 {
		requests.Store(0)

		ctx, cancel := context.WithTimeout(t.Context(), 18*time.Millisecond)

		_, err := driver.Result(ctx, "order", "order-42")
		cancel()

		if err == nil {
			return
		}

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}

	t.Fatal("Result repeatedly exceeded caller budget with 1ms poll cap")
}

// TestStatusResolvesReusedLogicalIDAgain models the engine retaining an old
// completed execution after permitting logical-ID reuse. A long-lived driver
// must observe the replacement rather than indefinitely returning cached state.
func TestStatusResolvesReusedLogicalIDAgain(t *testing.T) {
	var replaced atomic.Bool
	input := base64.StdEncoding.EncodeToString([]byte(`{"id":"order-42","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI","signal_version":2}`))
	address := protocoltest.GRPC(t, func(_ context.Context, _ string, request string) (string, error) {
		state := "COMPLETED"

		if strings.Contains(request, "55555555-5555-5555-5555-555555555555") {
			state = "RUNNING"
		}

		return fmt.Sprintf(`{"input":%q,"status":%q}`, input, state), nil
	})
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/workflow-runs") {
			id := "33333333-3333-3333-3333-333333333333"

			if replaced.Load() {
				id = "55555555-5555-5555-5555-555555555555"
			}

			_, _ = fmt.Fprintf(w, `{"rows":[{"workflowName":"arkoren-wf-order","workflowRunExternalId":%q,"createdAt":"2026-10-01T00:00:00Z"}]}`, id)

			return
		}

		_, _ = fmt.Fprint(w, `{"rows":[{"name":"arkoren-wf-order","metadata":{"id":"22222222-2222-2222-2222-222222222222"}}]}`)
	}), func(host *hatchet.Builder) {
		host.HostPort(address)
	})
	driver, err := hatchetworkflow.NewDriver(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got, err := driver.Status(t.Context(), "order", "order-42"); err != nil || got != workflow.Completed {
		t.Fatalf("first Status = %q, %v; want completed", got, err)
	}

	replaced.Store(true)

	if got, err := driver.Status(t.Context(), "order", "order-42"); err != nil || got != workflow.Running {
		t.Fatalf("replacement Status = %q, %v; want running", got, err)
	}
}

// TestResultPreservesCallerCancellation cancels during a real native gRPC
// lookup, rather than between polls. Its error identity must match the caller's
// context regardless of which transport operation observed cancellation.
func TestResultPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	address := protocoltest.GRPC(t, func(context.Context, string, string) (string, error) {
		cancel()

		return "", status.Error(codes.Canceled, "caller canceled")
	})
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/workflow-runs") {
			_, _ = fmt.Fprint(w, `{"rows":[{"workflowName":"arkoren-wf-order","workflowRunExternalId":"33333333-3333-3333-3333-333333333333","createdAt":"2026-10-01T00:00:00Z"}]}`)

			return
		}

		_, _ = fmt.Fprint(w, `{"rows":[{"name":"arkoren-wf-order","metadata":{"id":"22222222-2222-2222-2222-222222222222"}}]}`)
	}), func(host *hatchet.Builder) {
		host.HostPort(address)
	})
	driver, err := hatchetworkflow.NewDriver(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Result(ctx, "order", "order-42"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Result = %v; want context.Canceled", err)
	}
}

// TestNewDriverRejectsNegativePollInterval keeps invalid polling policy at the
// public constructor boundary instead of creating an unbounded request loop.
func TestNewDriverRejectsNegativePollInterval(t *testing.T) {
	t.Parallel()

	_, err := hatchetworkflow.NewDriver(hatchet.New(), func(driver *hatchetworkflow.DriverBuilder) {
		driver.PollInterval(-time.Second)
	})

	if !errors.Is(err, hatchet.ErrInvalidConfig) {
		t.Fatalf("NewDriver = %v; want ErrInvalidConfig", err)
	}
}

// TestStartUsesRemainingLookupGrace models an acknowledged submission whose
// inventory row becomes visible near the end of its consistency window. The
// adapter must use the remaining grace when the next backoff will not fit, not
// return ErrNotFound early or submit again. Real SDK sockets prevent fake-time
// scheduling; delayed inventory is modeled by a clock check, without test sleeps.
func TestStartUsesRemainingLookupGrace(t *testing.T) {
	// grace leaves a final 20ms window after the 10ms and 20ms backoffs.
	const grace = 50 * time.Millisecond
	// visibilityDelay puts the row inside that final window, without sleeps.
	const visibilityDelay = 40 * time.Millisecond
	var submissions, listings atomic.Int32
	var visibleAt atomic.Int64
	input := base64.StdEncoding.EncodeToString([]byte(`{"id":"order-42","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI","signal_version":2}`))
	address := protocoltest.GRPC(t, func(_ context.Context, method, _ string) (string, error) {
		if strings.HasSuffix(method, "/TriggerWorkflow") {
			submissions.Add(1)
			visibleAt.Store(time.Now().Add(visibilityDelay).UnixNano())

			return `{"workflowRunId":"33333333-3333-3333-3333-333333333333"}`, nil
		}

		return fmt.Sprintf(`{"input":%q,"status":"RUNNING"}`, input), nil
	})
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if !strings.HasSuffix(r.URL.Path, "/workflow-runs") {
			_, _ = fmt.Fprint(w, `{"rows":[{"name":"arkoren-wf-order","metadata":{"id":"22222222-2222-2222-2222-222222222222"}}]}`)

			return
		}

		listings.Add(1)

		if time.Now().UnixNano() < visibleAt.Load() {
			_, _ = fmt.Fprint(w, `{"rows":[]}`)

			return
		}

		_, _ = fmt.Fprint(w, `{"rows":[{"workflowName":"arkoren-wf-order","workflowRunExternalId":"33333333-3333-3333-3333-333333333333","createdAt":"2026-10-01T00:00:00Z"}]}`)
	}), func(host *hatchet.Builder) {
		host.HostPort(address)
	})
	driver, err := hatchetworkflow.NewDriver(host, func(driver *hatchetworkflow.DriverBuilder) {
		driver.LookupGrace(grace)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context(), "order", "order-42", []byte(`{"v":"input"}`), workflow.StartOptions{}); err != nil {
		t.Fatalf("Start discarded the remaining lookup grace: %v (submissions=%d, listings=%d)", err, submissions.Load(), listings.Load())
	}

	if submissions.Load() != 1 {
		t.Fatalf("submissions=%d; want one acknowledged submission", submissions.Load())
	}
}
