package temporalschedule

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/driver/temporal"
)

// fakeBackend is an in-memory backend.
type fakeBackend struct {
	// mu serializes updates to the fake's in-memory schedule records.
	mu sync.Mutex
	// schedules stores the schedule options indexed by schedule identifier.
	schedules map[string]client.ScheduleOptions
	// updated retains the update result for each reconciled schedule.
	updated map[string]*client.ScheduleUpdate
	// deleted records deletion requests in call order for reconciliation
	// assertions.
	deleted []string
	// started records native workflow-start options in submission order.
	started []client.StartWorkflowOptions
	// startedIn records each input passed to the fake workflow-start API.
	startedIn []input
	// startedWf records each workflow type passed to the fake start API.
	startedWf []string
	// terminated records requested workflow terminations in call order.
	terminated []string
	// terminate scripts the error returned by the fake termination API.
	terminate error
}

// newFake creates empty schedule and update indexes so reconciliation tests can
// mutate their own isolated engine state.
func newFake() *fakeBackend {
	return &fakeBackend{
		schedules: map[string]client.ScheduleOptions{},
		updated:   map[string]*client.ScheduleUpdate{},
	}
}

// createSchedule stores the supplied schedule options in the fake index,
// modeling a conflict when the identifier already exists.
func (fake *fakeBackend) createSchedule(_ context.Context, options client.ScheduleOptions) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	if _, ok := fake.schedules[options.ID]; ok {
		return sdktemporal.ErrScheduleAlreadyRunning
	}

	fake.schedules[options.ID] = options

	return nil
}

// updateSchedule supplies a paused native schedule to the update callback and
// retains its result for assertions. Callback failures propagate to
// reconciliation, and the paused state detects accidental state replacement.
func (fake *fakeBackend) updateSchedule(
	_ context.Context, id string, update func(client.ScheduleUpdateInput) (*client.ScheduleUpdate, error),
) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	result, err := update(client.ScheduleUpdateInput{
		Description: client.ScheduleDescription{
			Schedule: client.Schedule{
				State: &client.ScheduleState{
					Paused: true,
				},
			},
		},
	})
	fake.updated[id] = result

	return err
}

// listScheduleIDs returns the identifiers currently stored in the fake schedule
// index under its mutex.
func (fake *fakeBackend) listScheduleIDs(context.Context) ([]string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	var ids []string

	for id := range fake.schedules {
		ids = append(ids, id)
	}

	return ids, nil
}

// deleteSchedule removes the fake schedule and records the identifier for
// stale-resource assertions.
func (fake *fakeBackend) deleteSchedule(_ context.Context, id string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	delete(fake.schedules, id)
	fake.deleted = append(fake.deleted, id)

	return nil
}

// startWorkflow records execution options, workflow type, and input, then
// returns the deterministic fake workflow identifier.
func (fake *fakeBackend) startWorkflow(_ context.Context, options client.StartWorkflowOptions, workflowName string, in input) (string, error) {
	fake.started = append(fake.started, options)
	fake.startedIn = append(fake.startedIn, in)
	fake.startedWf = append(fake.startedWf, workflowName)

	return options.ID, nil
}

// terminateWorkflow records the requested workflow identifier and returns the
// scripted termination result.
func (fake *fakeBackend) terminateWorkflow(_ context.Context, id, _ string) error {
	fake.terminated = append(fake.terminated, id)

	return fake.terminate
}

// newTestDriver constructs a driver on an unstarted host with the supplied
// policies. Tests pass a fake directly to the operation under test; constructor
// failures fail the calling test.
func newTestDriver(t *testing.T, opts ...func(*Builder)) *Driver {
	t.Helper()

	driver, err := New(temporal.New(), opts...)

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// entry declares timing with a successful handler, followed by optional local
// policy. Neither construction nor its callbacks invoke the handler.
func entry(name string, spec schedule.Spec, configure ...func(*schedule.EntryBuilder)) schedule.Entry {
	return schedule.NewEntry(name, func(context.Context) error {
		return nil
	}, append([]func(*schedule.EntryBuilder){func(entry *schedule.EntryBuilder) {
		entry.When(spec)
	}}, configure...)...)
}

// TestCronFiveFieldsUnchanged verifies that ordinary five-field cron
// expressions pass through without adding a seconds or year field.
func TestCronFiveFieldsUnchanged(t *testing.T) {
	t.Parallel()

	got := cron(schedule.UTC().Minute(5).Hour(3))

	if got != "5 3 * * *" {
		t.Fatalf("cron = %q", got)
	}
}

// TestCronSixFieldsGainYear verifies that second-resolution expressions gain
// the year field required by the native schedule API.
func TestCronSixFieldsGainYear(t *testing.T) {
	t.Parallel()

	got := cron(schedule.UTC().Every(time.Second))

	if len(strings.Fields(got)) != 7 || !strings.HasSuffix(got, " *") {
		t.Fatalf("cron %q is not 7 fields", got)
	}
}

// TestOverlapMapping verifies that published overlap policy selects the
// corresponding native allow-all or skip policy.
func TestOverlapMapping(t *testing.T) {
	t.Parallel()

	allow := entry("a", schedule.UTC().Every(time.Minute))

	if got := overlap(allow); got != enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL {
		t.Fatalf("overlap = %v", got)
	}

	exclusive := entry("a", schedule.UTC().Every(time.Minute), func(entry *schedule.EntryBuilder) {
		entry.WithoutOverlapping()
	})

	if got := overlap(exclusive); got != enumspb.SCHEDULE_OVERLAP_POLICY_SKIP {
		t.Fatalf("overlap = %v", got)
	}
}

// TestOptionsRejectLocalZone verifies that machine-local schedule zones fail
// before native options are published.
func TestOptionsRejectLocalZone(t *testing.T) {
	t.Parallel()

	driver := newTestDriver(t)

	_, err := driver.options(entry("a", schedule.In(time.Local).Every(time.Minute)))

	if !errors.Is(err, schedule.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

// TestOptionsMapsEntry verifies that entry identity, workflow type, task queue,
// timeout, zone, and catch-up settings reach native schedule options.
func TestOptionsMapsEntry(t *testing.T) {
	t.Parallel()

	driver := newTestDriver(t, func(scheduler *Builder) {
		scheduler.Prefix("app")
	}, func(scheduler *Builder) {
		scheduler.CatchupWindow(2 * time.Minute)
	})

	options, err := driver.options(entry("job", schedule.UTC().Every(time.Minute), func(entry *schedule.EntryBuilder) {
		entry.Timeout(10 * time.Minute)
	}))

	if err != nil {
		t.Fatal(err)
	}

	action, ok := options.Action.(*client.ScheduleWorkflowAction)

	if !ok {
		t.Fatalf("action = %T", options.Action)
	}

	switch {
	case options.ID != "app-job", action.ID != "app-job":
		t.Fatalf("ids = %q %q", options.ID, action.ID)
	case action.Workflow != "app-schedule":
		t.Fatalf("workflow = %v", action.Workflow)
	case action.TaskQueue != "arkoren":
		t.Fatalf("queue = %q", action.TaskQueue)
	case action.WorkflowExecutionTimeout != 11*time.Minute:
		t.Fatalf("timeout = %v", action.WorkflowExecutionTimeout)
	case options.CatchupWindow != 2*time.Minute:
		t.Fatalf("catchup = %v", options.CatchupWindow)
	case options.Spec.TimeZoneName != "UTC":
		t.Fatalf("zone = %q", options.Spec.TimeZoneName)
	}
}

// TestReconcileCreatesSchedules verifies that missing declared entries create
// their native schedules.
func TestReconcileCreatesSchedules(t *testing.T) {
	t.Parallel()

	fake := newFake()
	driver := newTestDriver(t)
	entries := []schedule.Entry{
		entry("a", schedule.UTC().Every(time.Minute)),
		entry("b", schedule.UTC().Every(time.Second)),
	}

	if err := driver.reconcile(t.Context(), fake, entries); err != nil {
		t.Fatal(err)
	}

	if _, ok := fake.schedules["arkoren-a"]; !ok {
		t.Fatal("arkoren-a missing")
	}

	if _, ok := fake.schedules["arkoren-b"]; !ok {
		t.Fatal("arkoren-b missing")
	}
}

// TestReconcileUpdatesExistingSchedule verifies that reconciliation updates
// timing and policy while retaining the schedule's native state.
func TestReconcileUpdatesExistingSchedule(t *testing.T) {
	t.Parallel()

	fake := newFake()
	fake.schedules["arkoren-a"] = client.ScheduleOptions{
		ID: "arkoren-a",
	}
	driver := newTestDriver(t)

	err := driver.reconcile(t.Context(), fake, []schedule.Entry{
		entry("a", schedule.UTC().Every(time.Minute), func(entry *schedule.EntryBuilder) {
			entry.WithoutOverlapping()
		}),
	})

	if err != nil {
		t.Fatal(err)
	}

	result := fake.updated["arkoren-a"]

	if result == nil {
		t.Fatal("not updated")
	}

	switch {
	case result.Schedule.Spec == nil || len(result.Schedule.Spec.CronExpressions) != 1:
		t.Fatalf("spec = %+v", result.Schedule.Spec)
	case result.Schedule.Action == nil:
		t.Fatal("action missing")
	case result.Schedule.Policy.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_SKIP:
		t.Fatalf("overlap = %v", result.Schedule.Policy.Overlap)
	case result.Schedule.State == nil || !result.Schedule.State.Paused:
		t.Fatalf("state not kept: %+v", result.Schedule.State)
	}
}

// TestReconcileDeletesStaleAndKeepsForeign verifies that reconciliation removes
// stale owned schedules without touching foreign prefixes.
func TestReconcileDeletesStaleAndKeepsForeign(t *testing.T) {
	t.Parallel()

	fake := newFake()
	fake.schedules["arkoren-old"] = client.ScheduleOptions{
		ID: "arkoren-old",
	}
	fake.schedules["other-thing"] = client.ScheduleOptions{
		ID: "other-thing",
	}
	fake.schedules["arkorenx"] = client.ScheduleOptions{
		ID: "arkorenx",
	}
	driver := newTestDriver(t)

	err := driver.reconcile(t.Context(), fake, []schedule.Entry{entry("a", schedule.UTC().Every(time.Minute))})

	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(fake.deleted, []string{"arkoren-old"}) {
		t.Fatalf("deleted = %v", fake.deleted)
	}
}

// TestReconcileRejectsInvalidEntry verifies that invalid entry declarations
// fail before native reconciliation is attempted.
func TestReconcileRejectsInvalidEntry(t *testing.T) {
	t.Parallel()

	driver := newTestDriver(t)

	err := driver.reconcile(t.Context(), newFake(), []schedule.Entry{schedule.NewEntry("a", nil)})

	if !errors.Is(err, schedule.ErrInvalidEntry) {
		t.Fatalf("err = %v", err)
	}
}

// TestDeferStartsDelayedWorkflow verifies that one-shot jobs use a distinct
// identifier and carry their delay and payload to native workflow startup.
func TestDeferStartsDelayedWorkflow(t *testing.T) {
	t.Parallel()

	fake := newFake()
	driver := newTestDriver(t)

	id, err := driver.deferJob(t.Context(), fake, schedule.Job{
		Name:    "task",
		Payload: []byte(`{"a":1}`),
		At:      time.Now().Add(time.Hour),
	})

	if err != nil {
		t.Fatal(err)
	}

	options := fake.started[0]

	switch {
	case id != options.ID || len(id) <= len("arkoren-deferred-"):
		t.Fatalf("id = %q", id)
	case id[:len("arkoren-deferred-")] != "arkoren-deferred-":
		t.Fatalf("id = %q", id)
	case options.StartDelay < 59*time.Minute:
		t.Fatalf("delay = %v", options.StartDelay)
	case fake.startedWf[0] != "arkoren-schedule":
		t.Fatalf("workflow = %q", fake.startedWf[0])
	case fake.startedIn[0].Name != "task" || string(fake.startedIn[0].Payload) != `{"a":1}`:
		t.Fatalf("input = %+v", fake.startedIn[0])
	}
}

// TestDeferPastTimeHasNoDelay verifies that jobs whose requested time has
// passed start without a negative native delay.
func TestDeferPastTimeHasNoDelay(t *testing.T) {
	t.Parallel()

	fake := newFake()
	driver := newTestDriver(t)

	if _, err := driver.deferJob(t.Context(), fake, schedule.Job{
		Name: "t",
		At:   time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if fake.started[0].StartDelay != 0 {
		t.Fatalf("delay = %v", fake.started[0].StartDelay)
	}
}

// TestCancelTerminates verifies that scheduler cancellation terminates the
// addressed one-shot workflow.
func TestCancelTerminates(t *testing.T) {
	t.Parallel()

	fake := newFake()

	if err := newTestDriver(t).cancel(t.Context(), fake, "x"); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(fake.terminated, []string{"x"}) {
		t.Fatalf("terminated = %v", fake.terminated)
	}
}

// TestCancelTreatsNotFoundAsSuccess verifies that an already absent one-shot
// workflow counts as successfully canceled.
func TestCancelTreatsNotFoundAsSuccess(t *testing.T) {
	t.Parallel()

	fake := newFake()
	fake.terminate = serviceerror.NewNotFound("gone")

	if err := newTestDriver(t).cancel(t.Context(), fake, "x"); err != nil {
		t.Fatal(err)
	}
}

// TestCancelReturnsOtherErrors verifies that unexpected native cancellation
// failures are not swallowed.
func TestCancelReturnsOtherErrors(t *testing.T) {
	t.Parallel()

	fake := newFake()
	fake.terminate = errors.New("boom")

	if err := newTestDriver(t).cancel(t.Context(), fake, "x"); err == nil {
		t.Fatal("expected error")
	}
}

// TestNewRejectsNilHost verifies that construction rejects a missing shared
// host instead of registering against nil.
func TestNewRejectsNilHost(t *testing.T) {
	t.Parallel()

	if _, err := New(nil); err == nil {
		t.Fatal("expected error for nil host")
	}
}

// TestNewFailsAfterHostStarted verifies that a scheduler cannot register worker
// handlers after host startup has closed registration.
func TestNewFailsAfterHostStarted(t *testing.T) {
	t.Parallel()

	host := temporal.New()

	if _, err := New(host); err != nil {
		t.Fatal(err)
	}

	if err := host.Register(nil); err == nil {
		t.Fatal("expected error")
	}
}

// TestWorkflowRunsActivity verifies that the native workflow invokes its
// registered activity and forwards the job name and payload to dispatch.
func TestWorkflowRunsActivity(t *testing.T) {
	t.Parallel()

	driver := newTestDriver(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(driver.workflow, workflow.RegisterOptions{
		Name: driver.workflowName(),
	})
	env.RegisterActivityWithOptions(driver.activity, activity.RegisterOptions{
		Name: driver.activityName(),
	})

	var gotName string
	var gotPayload []byte
	driver.dispatch = func(_ context.Context, name string, payload []byte) error {
		gotName, gotPayload = name, payload

		return nil
	}

	env.ExecuteWorkflow(driver.workflowName(), input{
		Name:    "job",
		Payload: []byte("p"),
	})

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow error: %v", env.GetWorkflowError())
	}

	if gotName != "job" || string(gotPayload) != "p" {
		t.Fatalf("dispatched %q %q", gotName, gotPayload)
	}
}

// TestWorkflowFailsWhenDispatchFails verifies that dispatch failure reaches the
// workflow result without an unintended activity retry.
func TestWorkflowFailsWhenDispatchFails(t *testing.T) {
	t.Parallel()

	driver := newTestDriver(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(driver.workflow, workflow.RegisterOptions{
		Name: driver.workflowName(),
	})
	env.RegisterActivityWithOptions(driver.activity, activity.RegisterOptions{
		Name: driver.activityName(),
	})

	calls := 0
	driver.dispatch = func(context.Context, string, []byte) error {
		calls++

		return errors.New("boom")
	}

	env.ExecuteWorkflow(driver.workflowName(), input{
		Name: "job",
	})

	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error")
	}

	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry)", calls)
	}
}
