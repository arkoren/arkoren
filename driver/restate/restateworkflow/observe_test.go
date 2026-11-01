package restateworkflow_test

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

const (
	// ownershipHistoryLimit bounds failure-output volume under retries or replay.
	ownershipHistoryLimit = 128
	// ownershipStackLimit caps the all-goroutine dump, reporting truncation explicitly.
	ownershipStackLimit = 256 << 10
	// ownershipReportTimeout bounds read-only diagnostics after an existing failure;
	// it does not change execution, polling or assertion deadlines.
	ownershipReportTimeout = 3 * time.Second
)

// ownershipTrace keeps only this fixture's ownership lifecycle, never payloads,
// headers or error messages. Its bounded history is safe for concurrent SDK and
// step callbacks; successful tests never print it.
type ownershipTrace struct {
	// mu protects history and dropped while concurrent invocations report events.
	mu sync.Mutex
	// started measures cold-start and callback delays using monotonic elapsed time.
	started time.Time
	// history retains at most 128 events, dropping the oldest on overflow.
	history []string
	// dropped makes incomplete histories explicit rather than implying exact totals.
	dropped int
}

// record retains one bounded, fixture-owned event. Callers supply identifiers
// and classifications only, never application values or raw errors.
func (trace *ownershipTrace) record(event string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()

	if len(trace.history) == ownershipHistoryLimit {
		trace.history = slices.Delete(trace.history, 0, 1)
		trace.dropped++
	}

	trace.history = append(trace.history, fmt.Sprintf("%s %s", time.Since(trace.started), event))
}

// snapshot detaches the bounded history so reporting never holds the callback
// lock during network queries or test-output writes.
func (trace *ownershipTrace) snapshot() ([]string, int) {
	trace.mu.Lock()
	defer trace.mu.Unlock()

	return slices.Clone(trace.history), trace.dropped
}

// ownershipLog preserves ordinary SDK logging while retaining only recognized
// ownership lifecycle messages and native invocation identifiers. Attribute
// views share the trace but never mutate each other's SDK correlation fields.
type ownershipLog struct {
	// Handler writes ordinary SDK records to the original standard-log writer.
	//
	//	logger := ownershipLog{Handler: slog.NewTextHandler(log.Writer(), nil), trace: trace}
	slog.Handler
	// trace owns the bounded lifecycle history shared by attribute views.
	trace *ownershipTrace
	// method identifies the native service/handler, without a request body or key.
	method string
	// invocation correlates lifecycle events with the safe sys_invocation query.
	invocation string
}

// captureOwnershipLogs installs capture only for this nonparallel fixture and
// restores both logging APIs after its workers stop. A standalone text handler
// avoids forwarding into slog's legacy default handler: SetDefault redirects
// the standard logger, so forwarding to that handler would recurse and deadlock.
func captureOwnershipLogs(t *testing.T, trace *ownershipTrace) {
	t.Helper()

	previous := slog.Default()
	writer, flags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(ownershipLog{
		Handler: slog.NewTextHandler(writer, nil),
		trace:   trace,
	}))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
}

// WithAttrs retains SDK correlation while forwarding all attributes unchanged.
// Only method and invocationID are copied into diagnostic state.
//
//	logger.WithAttrs([]slog.Attr{slog.String("invocationID", "inv_fixture")})
func (logger ownershipLog) WithAttrs(attrs []slog.Attr) slog.Handler {
	logger.Handler = logger.Handler.WithAttrs(attrs)

	for _, attr := range attrs {
		switch attr.Key {
		case "method":
			logger.method = attr.Value.String()
		case "invocationID":
			logger.invocation = attr.Value.String()
		}
	}

	return logger
}

// WithGroup preserves the capture view when the SDK groups ordinary log output.
// Group contents are forwarded, not collected as diagnostic data.
//
//	grouped := logger.WithGroup("sdk")
//	_ = slog.New(grouped)
func (logger ownershipLog) WithGroup(name string) slog.Handler {
	logger.Handler = logger.Handler.WithGroup(name)

	return logger
}

// Handle captures lifecycle boundaries, not raw SDK error text. Ordinary SDK
// records still reach the standard-log writer with their original context.
//
//	record := slog.NewRecord(time.Now(), slog.LevelInfo, "Handling invocation", 0)
//	if err := logger.Handle(context.Background(), record); err != nil {
//		panic(err)
//	}
func (logger ownershipLog) Handle(ctx context.Context, record slog.Record) error {
	phases := map[string]string{
		"Handling invocation":                             "handler-enter",
		"Invocation completed successfully":               "handler-return-success",
		"Invocation returned a terminal failure":          "handler-return-terminal",
		"Invocation panicked, returning error to Restate": "handler-panic",
		"Suspending invocation":                           "handler-suspend",
	}
	phase := phases[record.Message]

	if strings.HasPrefix(logger.method, "ArkorenWf_conf-ownership-") && phase != "" {
		logger.trace.record(fmt.Sprintf("native method=%s invocation=%s phase=%s", logger.method, logger.invocation, phase))
	}

	return logger.Handler.Handle(ctx, record)
}

// ownershipRegistry replaces only the entry step with a diagnostic wrapper.
// The original registered handler still increments the suite's private counter;
// no definition is mutated and every workflow and entry policy is preserved.
// Successful original-handler returns are evidence of completed increments,
// not direct reads of the inaccessible private conformance counter.
func ownershipRegistry(t *testing.T, trace *ownershipTrace) *workflow.Registry {
	t.Helper()

	definitions := append(workflowtest.Definitions(), edgeDefinitions()...)

	for index, definition := range definitions {
		step, ok := definition.(*workflow.Step[string, string])

		if ok && step.Name() == "conf-ownership-entry" {
			definitions[index] = tracedOwnershipEntry(t, step, trace)
		}
	}

	registry, err := workflow.NewRegistry(definitions, func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(telemetrytest.New().Telemetry(t))
	})

	if err != nil {
		t.Fatal(err)
	}

	return registry
}

// tracedOwnershipEntry snapshots the original callback before wrapping it.
// Encoding is the same JSON envelope used by the suite; errors propagate without
// retrying, suppressing failures, or changing the step's completed policy.
func tracedOwnershipEntry(
	t *testing.T,
	step *workflow.Step[string, string],
	trace *ownershipTrace,
) *workflow.Step[string, string] {
	t.Helper()

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Step(step.Name())

	return workflow.NewStep(step.Name(), func(ctx context.Context, id string) (string, error) {
		input, err := workflow.EncodeValue(nil, id)

		if err != nil {
			return "", err
		}

		deadline, bounded := ctx.Deadline()
		trace.record(fmt.Sprintf("entry-enter run=%s context=%v deadline_set=%t deadline=%s",
			id, ctx.Err(), bounded, deadline.UTC().Format(time.RFC3339Nano)))
		output, err := handler.Handle(ctx, input)
		trace.record(fmt.Sprintf("entry-return run=%s success=%t context=%v", id, err == nil, ctx.Err()))

		if err != nil {
			return "", err
		}

		var value string
		err = workflow.DecodeValue(nil, output, &value)

		return value, err
	}, func(entry *workflow.StepBuilder) {
		entry.Retry(handler.Options.Attempts)
		entry.Backoff(handler.Options.Initial, handler.Options.Factor, handler.Options.Max)
		entry.Timeout(handler.Options.Timeout)
	})
}

// ownershipQuery is the existing Host.Query seam, substituted only by tests of
// failure reporting so they need no server or intentional failing CI test.
type ownershipQuery = func(context.Context, string) ([]map[string]any, error)

// registerOwnershipDiagnostics snapshots failed conformance before edge tests
// and the parent fixture's KillInvocations cleanup. The three-second budget applies
// only after failure, jointly bounding both read-only queries. Success performs
// no queries, stack capture or diagnostic output.
func registerOwnershipDiagnostics(t *testing.T, trace *ownershipTrace, query ownershipQuery) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}

		history, dropped := trace.snapshot()
		t.Logf("ownership diagnostics: dropped=%d; successful original entry returns imply increments, not counter reads", dropped)

		for _, event := range history {
			t.Log(event)
		}

		stacks := make([]byte, ownershipStackLimit)
		size := runtime.Stack(stacks, true)
		t.Logf("ownership goroutines (buffer_full=%t):\n%s", size == len(stacks), stacks[:size])

		ctx, cancel := context.WithTimeout(context.Background(), ownershipReportTimeout)
		defer cancel()
		ownershipState(ctx, query, t.Logf)
	})
}

// ownershipState selects only identifiers and execution metadata for the three
// fixture services. Explicit projections exclude inputs, headers, raw journal
// bytes, serialized entries and failure messages. Query failures report their
// type and context state, never a possibly sensitive server response body.
func ownershipState(ctx context.Context, query ownershipQuery, log func(string, ...any)) {
	// targets restricts both projections to this fixture's ownership services.
	const targets = "SELECT id FROM sys_invocation WHERE target_service_name IN (" +
		"'ArkorenWf_conf-ownership-child','ArkorenWf_conf-ownership-parent','ArkorenWf_conf-ownership-timeout')"
	queries := []string{
		"SELECT id, target_service_name, target_service_key, target_handler_name, status " +
			"FROM sys_invocation WHERE id IN (" + targets + ") ORDER BY id LIMIT 16",
		"SELECT id, index, entry_type, name, completed FROM sys_journal " +
			"WHERE id IN (" + targets + ") ORDER BY id, index LIMIT 64",
	}

	for index, statement := range queries {
		rows, err := query(ctx, statement)

		if err != nil {
			log("ownership state query %d: error_type=%T context=%v", index, err, ctx.Err())
			continue
		}

		log("ownership state query %d: rows=%d %v", index, len(rows), rows)
	}
}
