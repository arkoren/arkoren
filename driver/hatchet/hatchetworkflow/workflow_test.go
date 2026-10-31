package hatchetworkflow_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/hatchet-dev/hatchet/sdks/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/hatchet/hatchetworkflow"
	"arkoren.dev/driver/hatchet/internal/protocoltest"
)

// TestNativeStepRejectsSuccessAfterTimeout drives the real worker protocol and
// registered public step handler. A cooperative body can return nil after its
// deadline, but the adapter must not publish that late result as success.
// Core observes the handler's nil return, not the adapter's later native outcome;
// exactly one successful body observation and a native timeout are both required.
func TestNativeStepRejectsSuccessAfterTimeout(t *testing.T) {
	recording := telemetrytest.New()
	service := recording.Telemetry(t)
	payload := `{"input":{"data":"{\"v\":\"input\"}","timeout":1}}`
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "arkoren-step-charge", payload)
	step := workflow.NewStep("charge", func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()

		return "late success", nil
	}, func(step *workflow.StepBuilder) {
		step.Timeout(time.Second)
	})

	registry, err := workflow.NewRegistry([]workflow.Definition{step}, func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(service)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := hatchetworkflow.NewRunner(worker.Host, registry); err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	var event struct {
		// EventType reports the actual native attempt outcome.
		EventType string `json:"eventType"`
		// EventPayload carries the facade error envelope on failure.
		EventPayload string `json:"eventPayload"`
	}

	select {
	case input := <-worker.Outcomes:
		if err := json.Unmarshal([]byte(input), &event); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native worker did not report the step outcome")
	}

	if event.EventType != "STEP_EVENT_TYPE_FAILED" {
		t.Fatalf("native attempt = %+v; want timeout failure", event)
	}

	var failure string

	if err := json.Unmarshal([]byte(event.EventPayload), &failure); err != nil {
		t.Fatal(err)
	}

	if err := workflow.DecodeError([]byte(failure)); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("native failure = %v; want workflow.ErrTimeout", err)
	}

	recording.AssertSpanCount(t, 1)
	recording.AssertCounter(t, "arkoren.workflow.operation.count", 1,
		telemetry.String("arkoren.workflow.operation", "step_attempt"),
		telemetry.String("arkoren.workflow.outcome", "success"))
	recording.AssertHistogram(t, "arkoren.workflow.operation.duration", 1,
		telemetry.String("arkoren.workflow.operation", "step_attempt"),
		telemetry.String("arkoren.workflow.outcome", "success"))
}

// TestNativeSelectPreservesDeadlineFailure characterizes a durable wait whose
// engine acknowledgement never arrives. Duplicate timer cases must not hide the
// workflow deadline or turn native cancellation into an unclassified RPC error.
func TestNativeSelectPreservesDeadlineFailure(t *testing.T) {
	payload := `{"input":{"id":"order-42","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI","signal_version":2,"data":"{\"v\":\"input\"}"}}`
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "arkoren-wf-order", payload)
	flow := workflow.New("order", func(ctx *workflow.Context, _ string) (string, error) {
		callback := func() {
			t.Error("unacknowledged timer fired")
		}

		err := ctx.Select(workflow.After(time.Hour, callback), workflow.After(time.Hour, callback))

		return "", err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(100 * time.Millisecond)
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := hatchetworkflow.NewRunner(worker.Host, registry); err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-worker.Outcomes:
		if !strings.Contains(event, "workflow.timeout") {
			t.Fatalf("Select native outcome = %s; want workflow timeout", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Select did not finish at its deadline")
	}
}

// TestNativeStepPreservesZeroBackoff distinguishes a completed zero-delay call
// from positive registration defaults. The native protocol's optional int64 must
// carry a present zero, not an absent override that restores engine backoff.
func TestNativeStepPreservesZeroBackoff(t *testing.T) {
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "arkoren-step-charge",
		`{"input":{"data":"{\"v\":\"input\"}","retry_without_delay":true}}`)
	step := workflow.NewStep("charge", func(context.Context, string) (string, error) {
		return "", errors.New("temporarily unavailable")
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(7*time.Second, 2, time.Minute)
	})

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := hatchetworkflow.NewRunner(worker.Host, registry); err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-worker.Outcomes:
		var outcome struct {
			// RetryAfterMS must be present, not merely a decoded default zero.
			RetryAfterMS *int64 `json:"retryAfterMs,string"`
		}

		if err := json.Unmarshal([]byte(event), &outcome); err != nil {
			t.Fatal(err)
		}

		if outcome.RetryAfterMS == nil {
			t.Fatalf("native retry omitted its zero override: %s", event)
		}

		if *outcome.RetryAfterMS != 0 {
			t.Fatalf("native retry delay = %dms; want explicit zero", *outcome.RetryAfterMS)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native step did not report its zero-delay retry")
	}
}

// TestNativeZeroBackoffStopsAtAttemptLimit observes the last of three permitted
// attempts. Zero delay changes retry timing, never the finite retry count or the
// native nonretryable marker sent once the attempt budget is exhausted.
func TestNativeZeroBackoffStopsAtAttemptLimit(t *testing.T) {
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "arkoren-step-charge",
		`{"input":{"data":"{\"v\":\"input\"}"}}`)
	worker.RetryCount = 2

	step := workflow.NewStep("charge", func(context.Context, string) (string, error) {
		return "", errors.New("still unavailable")
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(0, 2, time.Minute)
	})

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := hatchetworkflow.NewRunner(worker.Host, registry); err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-worker.Outcomes:
		var outcome struct {
			// ShouldNotRetry terminates the native attempt chain.
			ShouldNotRetry bool `json:"shouldNotRetry"`
			// RetryAfterMS must be absent on a terminal failure.
			RetryAfterMS *int64 `json:"retryAfterMs,string"`
		}

		if err := json.Unmarshal([]byte(event), &outcome); err != nil {
			t.Fatal(err)
		}

		if !outcome.ShouldNotRetry || outcome.RetryAfterMS != nil {
			t.Fatalf("exhausted zero-delay policy requested another retry: %s", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native step did not report its terminal attempt")
	}
}

// TestNativeStepPreservesInitialBackoff observes the retry delay on the native
// failure event. Engine v0.111.0+ consumes this explicit delay; no local retry,
// sleep, additional durable node, or application-payload mutation is involved.
func TestNativeStepPreservesInitialBackoff(t *testing.T) {
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "arkoren-step-charge",
		`{"input":{"data":"{\"v\":\"input\"}"}}`)
	step := workflow.NewStep("charge", func(context.Context, string) (string, error) {
		return "", errors.New("temporarily unavailable")
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(7*time.Second, 3, time.Minute)
	})

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := hatchetworkflow.NewRunner(worker.Host, registry); err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-worker.Outcomes:
		var outcome struct {
			// RetryAfterMS is the explicit engine delay, encoded as an int64 string.
			RetryAfterMS int64 `json:"retryAfterMs,string"`
		}

		if err := json.Unmarshal([]byte(event), &outcome); err != nil {
			t.Fatal(err)
		}

		if outcome.RetryAfterMS != 7000 {
			t.Fatalf("native retry delay = %dms; want 7000ms", outcome.RetryAfterMS)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native step did not report its retry")
	}
}

// TestNativeWorkflowBindsMaxDurationToSDKContext observes the documented Raw
// context inside a public workflow handler. Native durable joins and RPCs use
// this same SDK context, so its deadline must be the runtime's MaxDuration.
func TestNativeWorkflowBindsMaxDurationToSDKContext(t *testing.T) {
	payload := `{"input":{"id":"order-42","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI","signal_version":2,"data":"{\"v\":\"input\"}"}}`
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "arkoren-wf-order", payload)
	flow := workflow.New("order", func(ctx *workflow.Context, _ string) (string, error) {
		_, bounded := ctx.Raw().(sdk.DurableContext).GetContext().Deadline()

		if !bounded {
			return "", errors.New("native durable context has no workflow deadline")
		}

		return "bounded", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := hatchetworkflow.NewRunner(worker.Host, registry); err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-worker.Outcomes:
		var outcome struct {
			// EventType is the native terminal outcome emitted after restoration.
			EventType string `json:"eventType"`
		}

		if err := json.Unmarshal([]byte(event), &outcome); err != nil {
			t.Fatal(err)
		}

		if outcome.EventType != "STEP_EVENT_TYPE_COMPLETED" {
			t.Fatalf("native workflow = %s; want completed", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native workflow did not finish")
	}
}

// TestNativeWorkflowRejectsProcessLocalMemoFallback lets the host's durable
// version probe succeed, then fails the SDK's independent probe. The native
// task must fail before application code, never accepting process-local memos.
func TestNativeWorkflowRejectsProcessLocalMemoFallback(t *testing.T) {
	payload := `{"input":{"id":"order-42","key":"arkoren.workflow.v2..YXJrb3Jlbg.b3JkZXI.b3JkZXItNDI","signal_version":2,"data":"{\"v\":\"input\"}"}}`
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "arkoren-wf-order", payload)
	var probes atomic.Int32
	worker.Version = func() (string, error) {
		if probes.Add(1) == 3 {
			return "", status.Error(codes.PermissionDenied, "version unavailable")
		}

		return "v0.114.11", nil
	}

	var invoked atomic.Bool
	flow := workflow.New("order", func(*workflow.Context, string) (string, error) {
		invoked.Store(true)

		return "done", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := hatchetworkflow.NewRunner(worker.Host, registry); err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-worker.Outcomes:
	case <-time.After(5 * time.Second):
		t.Fatal("native worker did not report its capability failure")
	}

	if invoked.Load() {
		t.Fatal("workflow ran with the SDK's non-durable memo fallback")
	}
}
