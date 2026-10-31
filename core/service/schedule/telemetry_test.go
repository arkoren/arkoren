package schedule_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/scheduletest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// newTraced connects a quiet scheduler to a test telemetry service and returns
// its recorder so callers can inspect completed operations without a backend.
func newTraced(t *testing.T) (*schedule.Scheduler, *telemetrytest.Fake) {
	t.Helper()

	fake := telemetrytest.New()

	return schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
		scheduler.Telemetry(fake.Telemetry(t))
	}), fake
}

// entry matches the run span attribute naming entry or task name.
func entry(name string) telemetry.Attr {
	return telemetry.String("arkoren.schedule.entry", name)
}

// runSpan returns the finished "schedule run" span of the entry or task name.
func runSpan(t *testing.T, fake *telemetrytest.Fake, name string) memorytelemetry.Span {
	t.Helper()

	for _, span := range fake.Spans() {
		if span.Name != "schedule run" {
			continue
		}

		for _, attr := range span.Attrs {
			if attr.Key() == "arkoren.schedule.entry" && attr.Text() == name {
				return span
			}
		}
	}

	t.Fatalf("no schedule run span for %q", name)

	return memorytelemetry.Span{}
}

// TestRunOutcomes checks success, skip, and failure on one telemetry recorder,
// including counts, duration samples, status classification, and redaction.
func TestRunOutcomes(t *testing.T) {
	t.Parallel()

	scheduler, fake := newTraced(t)
	skip := func(context.Context) error {
		return schedule.ErrSkipped
	}

	fail := func(context.Context) error {
		return errors.New("disk full at /srv/alice")
	}

	if err := scheduler.Add(daily("fine", noop), daily("skip", skip), daily("bad", fail)); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"fine", "skip", "bad"} {
		_ = scheduler.RunNow(t.Context(), name)
	}

	outcome := func(name string) telemetry.Attr {
		return telemetry.String("arkoren.schedule.outcome", name)
	}

	fake.AssertSpanCount(t, 3)
	fake.AssertSpan(t, "schedule run", entry("fine"), telemetry.String("arkoren.schedule.kind", "entry"), outcome("ok"))
	fake.AssertSpan(t, "schedule run", entry("skip"), outcome("skipped"))
	fake.AssertSpan(t, "schedule run", entry("bad"), outcome("failed"), telemetry.String("error.type", "*errors.errorString"))
	fake.AssertCounter(t, "arkoren.schedule.run.count", 1, entry("skip"), outcome("skipped"))
	fake.AssertCounter(t, "arkoren.schedule.run.count", 1, entry("bad"), outcome("failed"), telemetry.String("error.type", "*errors.errorString"))
	fake.AssertHistogram(t, "arkoren.schedule.run.duration", 3)

	for name, want := range map[string]telemetry.StatusCode{"fine": telemetry.Unset, "skip": telemetry.Unset, "bad": telemetry.Error} {
		span := runSpan(t, fake, name)

		if span.Status != want {
			t.Errorf("%s status = %v, want %v", name, span.Status, want)
		}

		if len(span.Events) != 0 {
			t.Errorf("%s recorded %d events, want none", name, len(span.Events))
		}

		for _, attr := range span.Attrs {
			if strings.Contains(attr.Text(), "alice") {
				t.Errorf("%s attribute %q carries error text", name, attr.Key())
			}
		}
	}
}

// TestRunPanicIsClassified checks that recovered execution panics use the
// stable schedule.panic classification rather than leaking panic text.
func TestRunPanicIsClassified(t *testing.T) {
	t.Parallel()

	scheduler, fake := newTraced(t)

	if err := scheduler.Add(daily("boom", func(context.Context) error {
		panic("kaboom")
	})); err != nil {
		t.Fatal(err)
	}

	_ = scheduler.RunNow(t.Context(), "boom")

	fake.AssertSpan(t, "schedule run", entry("boom"), telemetry.String("error.type", "schedule.panic"))
}

// TestRunSpanParentsWorkDoneByTheEntry checks that the entry receives the run
// span's context, allowing work inside its body to become a child span.
func TestRunSpanParentsWorkDoneByTheEntry(t *testing.T) {
	t.Parallel()

	scheduler, fake := newTraced(t)
	tracer := fake.Telemetry(t).Tracer("test")
	child := func(ctx context.Context) error {
		_, span := tracer.Start(ctx, "child")
		span.End()

		return nil
	}

	if err := scheduler.Add(daily("parent", child)); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "parent"); err != nil {
		t.Fatal(err)
	}

	run := runSpan(t, fake, "parent")
	span, _ := fake.Span("child")

	if run.Parent.IsValid() || span.Parent.SpanID != run.Context.SpanID {
		t.Errorf("child parent = %v, run span = %v (run parent %v)", span.Parent, run.Context, run.Parent)
	}
}

// TestTaskRunUsesTaskKindAndHidesPayload checks driver-dispatched task
// classification without recording the payload's recipient identifier.
func TestTaskRunUsesTaskKindAndHidesPayload(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	fake := telemetrytest.New()
	scheduler := schedule.New(driver, quiet(), func(scheduler *schedule.Builder) {
		scheduler.Telemetry(fake.Telemetry(t))
	})

	remind := schedule.NewTask[reminder]("remind")

	if err := scheduler.Handle(remind, func(context.Context, reminder) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	start(t, scheduler)

	payload, err := json.Marshal(reminder{
		UserID: 424242,
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := dispatch(t, driver, "remind", payload); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "schedule run", entry("remind"),
		telemetry.String("arkoren.schedule.kind", "task"), telemetry.String("arkoren.schedule.outcome", "ok"))

	span := runSpan(t, fake, "remind")

	for _, attr := range span.Attrs {
		if strings.Contains(attr.Text(), "424242") {
			t.Errorf("attribute %q carries the payload", attr.Key())
		}
	}
}
