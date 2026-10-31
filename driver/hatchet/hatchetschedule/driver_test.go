package hatchetschedule

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	"github.com/hatchet-dev/hatchet/sdks/go/features"

	"arkoren.dev/core/service/schedule"
	hatchethost "arkoren.dev/driver/hatchet"
)

// fakeCron stores one native cron row, including the ownership metadata used by
// reconciliation assertions.
type fakeCron struct {
	// id identifies the engine resource represented by this record.
	id string
	// workflow names the engine workflow that owns this cron trigger.
	workflow string
	// name identifies the application definition represented by this record.
	name string
	// metadata retains the trigger metadata used to check ownership during
	// reconciliation.
	metadata map[string]any
	// expression is the engine-ready cron expression, including any explicit time
	// zone.
	expression string
}

// fakeCrons records and pages cron operations in memory, allowing
// reconciliation tests to control existing resources and failures.
type fakeCrons struct {
	// mutex serializes access to the mutable state below it.
	mutex sync.Mutex
	// rows supplies the stored cron records returned by the fake listing API.
	rows []fakeCron
	// next allocates distinct identifiers for successfully created cron records.
	next int
	// deleted records deletion requests in call order for reconciliation
	// assertions.
	deleted []string
	// createErr scripts the error returned before the fake creates a resource.
	createErr error
	// created records successful creation requests in call order.
	created []features.CreateCronTrigger
	// createdFor records the workflow name corresponding to each creation request.
	createdFor []string
}

// Create records the native creation request and returns a deterministic
// resource identifier. The cron fake can fail before it mutates its records.
func (fake *fakeCrons) Create(_ context.Context, workflow string, cron features.CreateCronTrigger) (*rest.CronWorkflows, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	if fake.createErr != nil {
		return nil, fake.createErr
	}

	fake.next++
	fake.created = append(fake.created, cron)
	fake.createdFor = append(fake.createdFor, workflow)
	fake.rows = append(fake.rows, fakeCron{
		id:         fmt.Sprintf("new-%d", fake.next),
		workflow:   workflow,
		name:       cron.Name,
		metadata:   cron.AdditionalMetadata,
		expression: cron.Expression,
	})

	return &rest.CronWorkflows{}, nil
}

// List filters stored cron rows by workflow and applies the requested page
// bounds. Returned rows carry the metadata reconciliation uses to establish
// ownership.
func (fake *fakeCrons) List(_ context.Context, params rest.CronWorkflowListParams) (*rest.CronWorkflowsList, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	var matching []rest.CronWorkflows

	for _, row := range fake.rows {
		if row.workflow != *params.WorkflowName {
			continue
		}

		metadata := row.metadata
		name := row.name
		item := rest.CronWorkflows{
			Name:               &name,
			AdditionalMetadata: &metadata,
		}
		item.Metadata.Id = row.id
		matching = append(matching, item)
	}

	start := min(int(*params.Offset), len(matching))
	end := min(start+int(*params.Limit), len(matching))
	page := matching[start:end]

	return &rest.CronWorkflowsList{
		Rows: &page,
	}, nil
}

// Delete records the requested resource identifier for later assertions and
// returns any scripted deletion failure.
func (fake *fakeCrons) Delete(_ context.Context, id string) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.deleted = append(fake.deleted, id)

	return nil
}

// fakeSchedules records one-shot creation and deletion calls without requiring
// a running Hatchet engine.
type fakeSchedules struct {
	// created records successful creation requests in call order.
	created []features.CreateScheduledRunTrigger
	// createdFor records the workflow name corresponding to each creation request.
	createdFor []string
	// deleted records deletion requests in call order for reconciliation
	// assertions.
	deleted []string
	// deleteErr scripts the error returned after the deletion request is recorded.
	deleteErr error
}

// Create records the native creation request and returns a deterministic
// resource identifier. The cron fake can fail before it mutates its records.
func (fake *fakeSchedules) Create(_ context.Context, workflow string, trigger features.CreateScheduledRunTrigger) (*rest.ScheduledWorkflows, error) {
	fake.created = append(fake.created, trigger)
	fake.createdFor = append(fake.createdFor, workflow)

	run := &rest.ScheduledWorkflows{}
	run.Metadata.Id = "run-1"

	return run, nil
}

// Delete records the requested resource identifier for later assertions and
// returns any scripted deletion failure.
func (fake *fakeSchedules) Delete(_ context.Context, id string) error {
	fake.deleted = append(fake.deleted, id)

	return fake.deleteErr
}

// newTestDriver constructs a driver with the supplied policies and replaces its
// native clients with recording fakes. Construction failures fail the calling
// test; returned fakes let each scenario control its own engine state.
func newTestDriver(t *testing.T, opts ...func(*Builder)) (*Driver, *fakeCrons, *fakeSchedules) {
	t.Helper()

	driver, err := New(hatchethost.New(), opts...)

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	crons, schedules := &fakeCrons{}, &fakeSchedules{}
	driver.clients = func() (cronAPI, scheduleAPI, error) {
		return crons, schedules, nil
	}

	return driver, crons, schedules
}

// entryEvery declares the supplied timing and applies any local overlap policy
// after it. The successful handler is retained for dispatch, not run here.
func entryEvery(name string, spec schedule.Spec, configure ...func(*schedule.EntryBuilder)) schedule.Entry {
	return schedule.NewEntry(name, func(context.Context) error {
		return nil
	}, append([]func(*schedule.EntryBuilder){func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}}, configure...)...)
}

// managed builds ownership metadata with the same prefix and definition digest
// as reconciliation, so test cron rows can be recognized as managed.
func managed(prefix, workflow, expression string) map[string]any {
	return map[string]any{metadataPrefix: prefix, metadataHash: hashCron(workflow, expression)}
}

// TestDeferCreatesScheduledRun preserves scheduling metadata while exposing
// business JSON only as an opaque string to Hatchet's generic-map APIs.
func TestDeferCreatesScheduledRun(t *testing.T) {
	t.Parallel()

	driver, _, schedules := newTestDriver(t)
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 7200))

	id, err := driver.Defer(t.Context(), schedule.Job{
		Name:    "remind",
		Payload: []byte(`{"id":12345678901234567890}`),
		At:      at,
	})

	if err != nil || id != "run-1" {
		t.Fatalf("Defer() = %q, %v", id, err)
	}

	trigger := schedules.created[0]

	if schedules.createdFor[0] != "arkoren-deferred" || !trigger.TriggerAt.Equal(at) || trigger.TriggerAt.Location() != time.UTC {
		t.Fatalf("trigger = %+v for %v", trigger, schedules.createdFor)
	}

	if trigger.Input["name"] != "remind" || trigger.Input["payload_json_v1"] != `{"id":12345678901234567890}` {
		t.Fatalf("input = %v", trigger.Input)
	}

	if _, ok := trigger.Input["payload"]; ok {
		t.Fatalf("legacy payload still exposed to SDK maps: %v", trigger.Input)
	}
}

// TestDeferRequiresStartedHost verifies that deferred submission fails with the
// host readiness sentinel before a native client exists.
func TestDeferRequiresStartedHost(t *testing.T) {
	t.Parallel()

	driver, err := New(hatchethost.New())

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := driver.Defer(t.Context(), schedule.Job{
		Name: "x",
	}); !errors.Is(err, hatchethost.ErrNotStarted) {
		t.Fatalf("Defer() error = %v, want ErrNotStarted", err)
	}
}

// TestCancelDeletesScheduledRun keeps cancellation keyed by the unchanged
// scheduled-run identifier rather than any field in the payload envelope.
func TestCancelDeletesScheduledRun(t *testing.T) {
	t.Parallel()

	driver, _, schedules := newTestDriver(t)

	if err := driver.Cancel(t.Context(), "run-1"); err != nil || len(schedules.deleted) != 1 {
		t.Fatalf("Cancel() = %v, deleted %v", err, schedules.deleted)
	}

	if schedules.deleted[0] != "run-1" {
		t.Fatalf("deleted ID = %q, want run-1", schedules.deleted[0])
	}
}

// TestCancelIgnoresNotFound verifies that deleting a nonexistent deferred run
// is treated as successful cancellation.
func TestCancelIgnoresNotFound(t *testing.T) {
	t.Parallel()

	driver, _, schedules := newTestDriver(t)
	schedules.deleteErr = errors.New("received non-200 response from server. got status 404 with body ''")

	if err := driver.Cancel(t.Context(), "run-1"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
}

// TestCancelAcceptsNoContent verifies that the SDK's rejection of a successful
// HTTP 204 delete response is normalized to success.
func TestCancelAcceptsNoContent(t *testing.T) {
	t.Parallel()

	driver, _, schedules := newTestDriver(t)
	schedules.deleteErr = errors.New("received non-200 response from server. got status 204 with body ''")

	if err := driver.Cancel(t.Context(), "run-1"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
}

// TestCancelReturnsOtherErrors verifies that unexpected native cancellation
// failures are not swallowed.
func TestCancelReturnsOtherErrors(t *testing.T) {
	t.Parallel()

	driver, _, schedules := newTestDriver(t)
	schedules.deleteErr = errors.New("status 500")

	if err := driver.Cancel(t.Context(), "run-1"); err == nil {
		t.Fatal("Cancel() error = nil")
	}
}

// TestHandleDispatchesNameAndPayload verifies that the task handler forwards
// both the registered name and opaque payload to application dispatch.
func TestHandleDispatchesNameAndPayload(t *testing.T) {
	t.Parallel()

	driver, _, _ := newTestDriver(t)

	var gotName, gotPayload string

	driver.dispatch = func(_ context.Context, name string, payload []byte) error {
		gotName, gotPayload = name, string(payload)

		return nil
	}

	input := taskInput{
		Name:    "remind",
		Payload: []byte(`{"a":1}`),
	}

	if _, err := driver.handle(t.Context(), input); err != nil || gotName != "remind" || gotPayload != `{"a":1}` {
		t.Fatalf("handle() = %q %q, %v", gotName, gotPayload, err)
	}
}

// TestHandleTreatsSkippedAsSuccess verifies that an application overlap skip is
// not reported as an engine task failure.
func TestHandleTreatsSkippedAsSuccess(t *testing.T) {
	t.Parallel()

	driver, _, _ := newTestDriver(t)
	driver.dispatch = func(context.Context, string, []byte) error {
		return schedule.ErrSkipped
	}

	if _, err := driver.handle(t.Context(), taskInput{
		Name: "x",
	}); err != nil {
		t.Fatalf("handle() error = %v", err)
	}
}

// TestHandleWrapsDispatchError checks that opaque decoding leaves dispatch
// error identity intact, so callers can still classify handler failures.
func TestHandleWrapsDispatchError(t *testing.T) {
	t.Parallel()

	driver, _, _ := newTestDriver(t)
	boom := errors.New("boom")
	driver.dispatch = func(context.Context, string, []byte) error {
		return boom
	}

	input := taskInput{
		Name:          "x",
		PayloadJSONV1: []byte(`"null"`),
	}

	if _, err := driver.handle(t.Context(), input); !errors.Is(err, boom) {
		t.Fatalf("handle() error = %v", err)
	}
}

// TestNewRejectsNilHost verifies that construction rejects a missing shared
// host instead of registering against nil.
func TestNewRejectsNilHost(t *testing.T) {
	t.Parallel()

	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) error = nil")
	}
}
