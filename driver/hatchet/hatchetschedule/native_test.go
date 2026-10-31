package hatchetschedule_test

import (
	"context"
	"encoding/json/v2"
	"reflect"
	"testing"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/scheduletest"
	hatchethost "arkoren.dev/driver/hatchet"
	"arkoren.dev/driver/hatchet/hatchetschedule"
	"arkoren.dev/driver/hatchet/internal/itest"
)

// prefix names the workflows of the test. Every run adds its own deferred
// job, so the engine can keep its history between runs.
const prefix = "arkoren-it-schedule"

// deferredNumbers exercises typed dispatch after the real scheduled-run API,
// engine storage, and SDK worker have each processed the task envelope.
type deferredNumbers struct {
	// Signed includes precision neighbors, a Snowflake ID, and int64 limits.
	Signed []int64 `json:"signed"`

	// Unsigned retains the uint64 upper boundary independently of signed data.
	Unsigned uint64 `json:"unsigned"`

	// Nested checks that opaque transport also protects numbers inside arrays.
	Nested [][]int64 `json:"nested"`

	// Null remains a JSON null rather than an omitted or string-coerced value.
	Null *string `json:"null"`

	// Text contains JSON-looking text which must remain application data.
	Text string `json:"text"`
}

// TestIntegrationDeferredTaskPreservesIntegers runs the SEC-014 regression
// against the local Hatchet engine, not the SDK-equivalent unit-test seam.
// It uses Scheduler.At and a typed handler so overflow and wrong-ID changes
// both fail, with a bounded wait if the worker cannot decode the payload.
func TestIntegrationDeferredTaskPreservesIntegers(t *testing.T) {
	host := hatchethost.New(itest.Options(t)...)
	driver, err := hatchetschedule.New(host, func(scheduler *hatchetschedule.Builder) {
		scheduler.Prefix(prefix + "-precision")
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)

	if err := host.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})

	scheduler := schedule.New(driver)
	task := schedule.NewTask[deferredNumbers]("precision")
	received := make(chan deferredNumbers, 1)

	if err := scheduler.Handle(task, func(_ context.Context, payload deferredNumbers) error {
		select {
		case received <- payload:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)

	go func() {
		done <- scheduler.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})

	want := deferredNumbers{
		Signed: []int64{
			42, 9007199254740991, 9007199254740992, 9007199254740993,
			-9007199254740991, -9007199254740992, -9007199254740993,
			1541815603606036481, -9223372036854775808, 9223372036854775807,
		},
		Unsigned: 18446744073709551615,
		Nested: [][]int64{{
			9007199254740993,
		}},
		Text: "{\"id\":9007199254740993}\n\"quoted\"",
	}

	if id, err := scheduler.At(ctx, task, want, time.Now().Add(2*time.Second)); err != nil || id == "" {
		t.Fatalf("At() = %q, %v", id, err)
	}

	select {
	case got := <-received:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("payload = %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatalf("typed task did not fire: %v", ctx.Err())
	}
}

// TestIntegrationEntryAndDeferFire checks that the driver registers the cron
// of an entry and fires a deferred job. The cron fires at most once a minute,
// so the test does not wait for it.
func TestIntegrationEntryAndDeferFire(t *testing.T) {
	host := hatchethost.New(itest.Options(t)...)

	driver, err := hatchetschedule.New(host, func(scheduler *hatchetschedule.Builder) {
		scheduler.Prefix(prefix)
	})

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	if err := host.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	defer func() {
		_ = host.Stop(context.Background())
	}()

	fired := make(chan string, 16)
	dispatch := func(_ context.Context, name string, _ []byte) error {
		fired <- name

		return nil
	}

	entries := []schedule.Entry{
		schedule.NewEntry("it-entry", func(context.Context) error {
			return nil
		}, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Cron("* * * * *"))
		}),
	}

	go func() {
		_ = driver.Run(ctx, entries, dispatch)
	}()

	payload, err := json.Marshal(map[string]string{"k": "v"})

	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if _, err := driver.Defer(ctx, schedule.Job{
		Name:    "it-deferred",
		Payload: payload,
		At:      time.Now().Add(time.Second),
	}); err != nil {
		t.Fatalf("Defer() error = %v", err)
	}

	for name := range fired {
		if name == "it-deferred" {
			break
		}
	}

	workflowName := prefix + "-run"

	for {
		page, err := host.Client().Crons().List(ctx, rest.CronWorkflowListParams{
			WorkflowName: &workflowName,
		})

		if err == nil && page != nil && page.Rows != nil && len(*page.Rows) > 0 {
			return
		}

		if ctx.Err() != nil {
			t.Fatalf("cron of the entry was not registered: %v", err)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// TestIntegrationConformance runs the conformance suite. Hatchet fires crons
// at most once a minute, so the suite waits for whole minutes.
func TestIntegrationConformance(t *testing.T) {
	scheduletest.Run(t, func(t *testing.T) schedule.Driver {
		host := hatchethost.New(itest.Options(t)...)

		driver, err := hatchetschedule.New(host, func(scheduler *hatchetschedule.Builder) {
			scheduler.Prefix(prefix + "-conformance")
		})

		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := host.Start(t.Context()); err != nil {
			t.Fatalf("Start() error = %v", err)
		}

		t.Cleanup(func() {
			_ = host.Stop(context.Background())
		})

		return driver
	}, scheduletest.WithMinInterval(time.Minute))
}
