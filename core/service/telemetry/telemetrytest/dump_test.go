package telemetrytest_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// dumpTB stands in for a test: it reports a chosen failed state, defers
// cleanups until run, and captures what the dump logs.
type dumpTB struct {
	// TB forwards test methods not replaced by the diagnostic capture hooks.
	testing.TB

	// failed scripts the result observed when the dump cleanup runs.
	failed bool
	// cleanups retains callbacks until the test explicitly runs them.
	cleanups []func()
	// logged accumulates diagnostic output for assertions.
	logged string
}

// Failed exposes the scripted outcome so DumpOnFailure can decide whether to log.
func (recorder *dumpTB) Failed() bool {
	return recorder.failed
}

// Cleanup retains callbacks for explicit execution after the fixture is populated.
func (recorder *dumpTB) Cleanup(fn func()) {
	recorder.cleanups = append(recorder.cleanups, fn)
}

// Log captures the dump text instead of writing to the real test output.
func (recorder *dumpTB) Log(args ...any) {
	recorder.logged += args[0].(string)
}

// runCleanups executes the retained callbacks after the fixture is populated.
func (recorder *dumpTB) runCleanups() {
	for _, fn := range recorder.cleanups {
		fn()
	}
}

// record starts a parent span with a failing child, an event, and logs in and
// outside the child.
func record(t *testing.T, fake *telemetrytest.Fake) {
	t.Helper()

	facade := fake.Telemetry(t)
	logger := slog.New(facade.Handler(slog.DiscardHandler))
	tracer := facade.Tracer("test")

	ctx, parent := tracer.Start(t.Context(), "http GET /users", telemetry.Attrs(telemetry.String("http.route", "/users")))
	childCtx, child := tracer.Start(ctx, "db SELECT users")
	child.RecordError(errors.New("boom"))
	child.SetStatus(telemetry.Error, "boom")
	logger.ErrorContext(childCtx, "query failed", "id", 7)
	child.End()
	parent.End()
	logger.InfoContext(t.Context(), "afterwards")
}

// TestDumpOnFailurePrintsTree prints spans as a tree with events and logs.
func TestDumpOnFailurePrintsTree(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	tb := &dumpTB{
		TB:     t,
		failed: true,
	}
	fake.DumpOnFailure(tb)
	record(t, fake)
	tb.runCleanups()

	for _, want := range []string{
		"telemetry: 2 spans, 2 logs\n",
		"http GET /users ", "{http.route=/users}\n",
		"\n  db SELECT users ", " error: boom\n",
		"\n    ! exception {exception.type=*errors.errorString, exception.message=boom}\n",
		"\n    log ERROR \"query failed\" {id=7}\n",
		"logs without span\n  log INFO \"afterwards\"\n",
	} {
		if !strings.Contains(tb.logged, want) {
			t.Errorf("dump lacks %q:\n%s", want, tb.logged)
		}
	}
}

// TestDumpOnFailurePrintsNothingWhenPassing keeps passing tests quiet.
func TestDumpOnFailurePrintsNothingWhenPassing(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	tb := &dumpTB{TB: t}
	fake.DumpOnFailure(tb)
	record(t, fake)
	tb.runCleanups()

	if tb.logged != "" {
		t.Fatalf("dump printed for a passing test: %s", tb.logged)
	}
}

// ExampleFake_DumpOnFailure shows how a test requests telemetry diagnostics
// only when it fails. DumpOnFailure registers the report with t.Cleanup.
func ExampleFake_DumpOnFailure() {
	_ = func(t *testing.T) {
		fake := telemetrytest.New()
		fake.DumpOnFailure(t)
	}
}
