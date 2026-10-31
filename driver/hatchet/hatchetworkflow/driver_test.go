package hatchetworkflow

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client" //nolint:staticcheck // SDK collision errors expose this legacy type
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"arkoren.dev/core/service/workflow"
)

// fake is an in-memory [backend].
type fake struct {
	// mutex serializes access to the mutable state below it.
	mutex sync.Mutex

	// unregistered makes the fake report that the requested workflow is absent.
	unregistered bool
	// triggerErr scripts a submission failure before any execution is recorded.
	triggerErr error
	// triggered retains submitted wire inputs in call order.
	triggered []wire
	// metadata records submitted metadata in the same order as triggered inputs.
	metadata []map[string]string
	// names retains submitted workflow names in call order.
	names []string

	// keys models Hatchet's tenant-wide idempotency table, deliberately
	// without adding the workflow name to the lookup key.
	keys map[string]string

	// runTTL and markerTTL model independently expiring engine idempotency
	// records. Zero keeps a key indefinitely; expiry never deletes history.
	runTTL time.Duration
	// markerTTL controls expiry of signal-marker idempotency keys independently of
	// ordinary runs; zero retains keys indefinitely.
	markerTTL time.Duration

	// expires records trigger-relative deadlines in the tenant-wide key
	// table. Retained run input and events outlive an expired key.
	expires map[string]time.Time

	// namespace simulates the SDK prefix at the transport boundary. runs
	// contains the engine-owned workflow identity rather than the filter.
	namespace string
	// runs retains engine-owned execution identities independently of the
	// requested discovery filter.
	runs []listedRun

	// listed overrides discovered rows when non-nil, allowing tests to return
	// foreign or stale identities.
	listed []listedRun
	// listCalls counts discovery requests, including requests hidden by listAfter.
	listCalls int
	// listAfter hides discovery results for this many initial requests.
	listAfter int

	// details_ stores execution input, output, and status by physical engine
	// identifier.
	details_ map[string]runDetail
	// notFound scripts this many initial detail lookups as missing before normal
	// lookup resumes.
	notFound int

	// canceled records the identifiers sent to the cancellation API.
	canceled []string
	// pushed retains successfully accepted events independently of later
	// consumption.
	pushed []pushed
	// pushErr scripts an event-write failure before the event is retained.
	pushErr error
}

// pushed retains an event independently of its producer or consumer execution.
// Matching tests must apply the engine's scope/time rules to this shared history.
type pushed struct {
	// key is the native event name; scope separately identifies its physical
	// execution, so the same signal name can have independent histories.
	key string
	// payload retains the exact signal envelope submitted to the fake event API.
	payload wire
	// scope restricts event matching to one physical workflow execution.
	scope string

	// at is the engine event timestamp used for historical matching. Tests
	// advance synctest time without deleting an already-consumed event.
	at time.Time
}

// trigger atomically claims a tenant-wide key until its trigger-relative TTL
// expires. Reclamation allocates a new physical ID without removing old run
// input or events, matching the engine behavior behind logical-ID reuse.
func (fake *fake) trigger(_ context.Context, name string, in wire, metadata map[string]string) (string, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	if fake.triggerErr != nil {
		return "", fake.triggerErr
	}

	if expires := fake.expires[in.Key]; !expires.IsZero() && !time.Now().Before(expires) {
		delete(fake.keys, in.Key)
		delete(fake.expires, in.Key)
	}

	if existing, ok := fake.keys[in.Key]; ok {
		return "", &hatchet.IdempotencyCollisionError{
			ExistingRunExternalId: existing,
		}
	}

	fake.names = append(fake.names, name)
	fake.triggered = append(fake.triggered, in)
	fake.metadata = append(fake.metadata, metadata)

	id := "run-" + fmt.Sprint(len(fake.triggered))

	if fake.keys != nil {
		fake.keys[in.Key] = id
		ttl := fake.runTTL

		if strings.HasSuffix(name, "-signal") {
			ttl = fake.markerTTL
		}

		if ttl > 0 {
			if fake.expires == nil {
				fake.expires = map[string]time.Time{}
			}

			fake.expires[in.Key] = time.Now().Add(ttl)
		}
	}

	fake.record(fake.namespace+name, id, in)

	return id, nil
}

// exists reports the fake registration state without contacting an engine,
// allowing missing-definition tests to isolate submission validation.
func (fake *fake) exists(context.Context, string) (bool, error) {
	return !fake.unregistered, nil
}

// record stores immutable execution identity while retaining any scripted
// status/output. Callers serialize access (test setup or mutex); wire contains
// no values that can fail JSON encoding.
func (fake *fake) record(name, id string, in wire) {
	if fake.details_ == nil {
		fake.details_ = map[string]runDetail{}
	}

	detail := fake.details_[id]

	if len(detail.Input) == 0 {
		detail.Input, _ = json.Marshal(in) // wire has only strings and integers.
	}

	fake.details_[id] = detail
	fake.runs = append(fake.runs, listedRun{
		ID:       id,
		Created:  time.Now(),
		Workflow: name,
	})
}

// list returns scripted discovery rows or filters retained execution
// identities. Initial calls can be hidden to model delayed engine visibility.
func (fake *fake) list(_ context.Context, name, key string, _ time.Time) ([]listedRun, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.listCalls++

	if fake.listCalls <= fake.listAfter {
		return nil, nil
	}

	if fake.listed != nil {
		return slices.Clone(fake.listed), nil
	}

	var runs []listedRun

	for _, run := range fake.runs {
		var input wire

		if err := json.Unmarshal(fake.details_[run.ID].Input, &input); err != nil {
			return nil, err
		}

		if run.Workflow == name && input.Key == key {
			runs = append(runs, run)
		}
	}

	return runs, nil
}

// details returns retained run state after any scripted transient not-found
// responses. Unknown physical identifiers use the workflow not-found sentinel.
func (fake *fake) details(_ context.Context, id string) (runDetail, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	if fake.notFound > 0 {
		fake.notFound--

		return runDetail{}, workflow.ErrNotFound
	}

	detail, ok := fake.details_[id]

	if !ok {
		return runDetail{}, workflow.ErrNotFound
	}

	return detail, nil
}

// mark uses the same tenant-wide idempotency table as trigger, including its
// independent marker TTL. A collision returns the retained run's ID without
// replacing its input with the newly supplied value.
func (fake *fake) mark(ctx context.Context, name string, in wire) (string, error) {
	fake.mutex.Lock()

	if fake.keys == nil {
		fake.keys = map[string]string{}
	}

	fake.mutex.Unlock()

	id, err := fake.trigger(ctx, name, in, nil)

	if existing, ok := collisionRun(err); ok {
		return existing, nil
	}

	return id, err
}

// cancel records the physical execution identifier and succeeds. Tests inspect
// the retained call to verify logical-to-physical resolution before cancellation.
func (fake *fake) cancel(_ context.Context, id string) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	fake.canceled = append(fake.canceled, id)

	return nil
}

// push retains accepted scoped events with their insertion time. A scripted
// write failure leaves the retained event history unchanged.
func (fake *fake) push(_ context.Context, key string, payload wire, scope string) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	if fake.pushErr != nil {
		return fake.pushErr
	}

	fake.pushed = append(fake.pushed, pushed{
		key:     key,
		payload: payload,
		scope:   scope,
		at:      time.Now(),
	})

	return nil
}

// newTestDriver constructs a driver with short polling intervals and installs
// the supplied in-memory backend. Caller policies run after the test defaults.
func newTestDriver(f *fake, opts ...func(*DriverBuilder)) *Driver {
	driver := newDriver(driverSettings(append([]func(*DriverBuilder){func(driver *DriverBuilder) {
		driver.LookupGrace(300 * time.Millisecond)
	}, func(driver *DriverBuilder) {
		driver.PollInterval(5 * time.Millisecond)
	}}, opts...)...))
	driver.backend = func() (backend, error) {
		return f, nil
	}

	return driver
}

// output encodes application data in the wire envelope. Encoding failures fail
// the calling test instead of producing malformed fixture bytes.
func output(t *testing.T, data string) []byte {
	t.Helper()

	raw, err := json.Marshal(wire{
		Data: data,
	})

	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	return raw
}

// TestStartTriggersWithKeyAndMetadata verifies that submission carries the
// logical key, physical-signal protocol, and application metadata to the
// engine.
func TestStartTriggersWithKeyAndMetadata(t *testing.T) {
	t.Parallel()

	f := &fake{}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.Prefix("app")
	})

	err := driver.Start(t.Context(), "order", "o-1", []byte(`{"v":1}`), workflow.StartOptions{
		Metadata: map[string]string{"tenant": "t1", "workflow_id": "spoof"},
	})

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if f.names[0] != "app-wf-order" {
		t.Fatalf("workflow name = %q", f.names[0])
	}

	got := f.triggered[0]

	if got.Data != `{"v":1}` || got.ID != "o-1" || got.Key != "arkoren.workflow.v2..YXBw.b3JkZXI.by0x" {
		t.Fatalf("input = %+v", got)
	}

	if f.metadata[0]["workflow_id"] != "o-1" || f.metadata[0]["tenant"] != "t1" {
		t.Fatalf("metadata = %v", f.metadata[0])
	}
}

// TestNamespaceStartsAreIndependent models two deployments sharing one
// tenant-wide key table. Their identical logical IDs must not deduplicate.
func TestNamespaceStartsAreIndependent(t *testing.T) {
	t.Parallel()

	f := &fake{
		keys: map[string]string{},
	}
	production := newTestDriver(f)
	production.namespace = "production_"

	staging := newTestDriver(f)
	staging.namespace = "staging_"

	f.namespace = production.namespace

	if err := production.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); err != nil {
		t.Fatal(err)
	}

	f.namespace = staging.namespace

	if err := staging.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); err != nil {
		t.Fatalf("staging Start() = %v; different namespaces must start independently", err)
	}

	if len(f.keys) != 2 {
		t.Fatalf("physical starts = %d; want 2", len(f.keys))
	}

	if err := staging.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("same namespace Start() = %v; want ErrAlreadyStarted", err)
	}
}

// requireStartError verifies Start maps a trigger failure to want.
func requireStartError(t *testing.T, triggerErr, want error) {
	t.Helper()

	f := &fake{
		triggerErr: triggerErr,
	}
	driver := newTestDriver(f)

	if id, ok := collisionRun(triggerErr); ok {
		if id == "" {
			id = "existing"
		}

		f.record(driver.workflowName("order"), id, wire{
			ID:  "o-1",
			Key: driver.runKey("order", "o-1"),
		})
	}

	if err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); !errors.Is(err, want) {
		t.Fatalf("Start() error = %v; want %v", err, want)
	}
}

// TestStartMapsSDKCollision verifies that the current SDK collision error maps
// to ErrAlreadyStarted.
func TestStartMapsSDKCollision(t *testing.T) {
	t.Parallel()

	requireStartError(t, &hatchet.IdempotencyCollisionError{
		ExistingRunExternalId: "abc",
	}, workflow.ErrAlreadyStarted)
}

// TestStartMapsWrappedCollision verifies that wrapping a native collision does
// not hide the ErrAlreadyStarted mapping.
func TestStartMapsWrappedCollision(t *testing.T) {
	t.Parallel()

	requireStartError(t, fmt.Errorf("trigger: %w", &hatchet.IdempotencyCollisionError{}), workflow.ErrAlreadyStarted)
}

// TestStartMapsLegacyCollision verifies that the supported legacy SDK collision
// type still maps to ErrAlreadyStarted.
func TestStartMapsLegacyCollision(t *testing.T) {
	t.Parallel()

	requireStartError(t, &client.IdempotencyViolationErr{
		ExistingRunExternalId: "abc",
	}, workflow.ErrAlreadyStarted)
}

// TestStartMapsUnknownWorkflow verifies that a native missing-definition
// response maps to the workflow not-found sentinel.
func TestStartMapsUnknownWorkflow(t *testing.T) {
	t.Parallel()

	requireStartError(t, status.Error(codes.NotFound, "workflow"), workflow.ErrNotFound)
}

// TestStartKeepsOtherErrors verifies that unexpected submission errors remain
// discoverable through the driver's wrapping.
func TestStartKeepsOtherErrors(t *testing.T) {
	t.Parallel()

	other := errors.New("boom")

	if err := newTestDriver(&fake{
		triggerErr: other,
	}).Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); !errors.Is(err, other) || errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("Start() error = %v", err)
	}
}

// TestStartDelay verifies that submission retains the requested delay and
// rejects delays beyond the configured run duration.
func TestStartDelay(t *testing.T) {
	t.Parallel()

	f := &fake{}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.TimeoutMargin(time.Minute)
	})

	if err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{
		Delay: time.Second,
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if f.triggered[0].Delay != int64(time.Second) {
		t.Fatalf("delay = %d", f.triggered[0].Delay)
	}

	err := driver.Start(t.Context(), "order", "o-2", nil, workflow.StartOptions{
		Delay: time.Hour,
	})

	if !errors.Is(err, workflow.ErrUnsupported) {
		t.Fatalf("Start(long delay) error = %v", err)
	}
}

// TestCacheEvictsLeastRecentlyUsed verifies that bounded execution caching
// discards the oldest unused logical key.
func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	driver := newDriver(driverSettings())

	for i := range cacheLimit {
		driver.remember(fmt.Sprint(i), "r")
	}

	driver.cached("0")

	driver.remember("new", "r")

	if _, ok := driver.cached("0"); !ok {
		t.Error("recently used entry was evicted")
	}

	if _, ok := driver.cached("1"); ok {
		t.Error("least recently used entry was kept")
	}
}

// TestStatusForgetsVanishedRun verifies that a missing physical execution
// invalidates its cached logical-key mapping.
func TestStatusForgetsVanishedRun(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{},
	}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	driver.remember(driver.runKey("order", "o-1"), "gone")

	if _, err := driver.Status(t.Context(), "order", "o-1"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status() error = %v", err)
	}

	if _, ok := driver.cached(driver.runKey("order", "o-1")); ok {
		t.Fatal("vanished run is still cached")
	}
}

// TestUnknownRunListsOnce verifies that an unknown logical execution performs
// one bounded discovery instead of repeated listing.
func TestUnknownRunListsOnce(t *testing.T) {
	t.Parallel()

	f := &fake{}

	if _, err := newTestDriver(f).Status(t.Context(), "order", "x"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status() error = %v", err)
	}

	if f.listCalls != 1 {
		t.Fatalf("list called %d times; want 1", f.listCalls)
	}
}

// TestCollisionRemembersExistingRun verifies that collision resolution
// validates the existing execution identity before retaining its physical
// identifier.
func TestCollisionRemembersExistingRun(t *testing.T) {
	t.Parallel()

	f := &fake{
		triggerErr: &hatchet.IdempotencyCollisionError{
			ExistingRunExternalId: "existing",
		},
		details_: map[string]runDetail{"existing": {
			Status: statusRunning,
		}},
	}
	driver := newTestDriver(f)
	f.record(driver.workflowName("order"), "existing", wire{
		ID:  "o-1",
		Key: driver.runKey("order", "o-1"),
	})

	if err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("Start() error = %v", err)
	}

	if runStatus, err := driver.Status(t.Context(), "order", "o-1"); err != nil || runStatus != workflow.Running {
		t.Fatalf("Status() = %v, %v", runStatus, err)
	}

}

// TestFindPicksLatestAndWaitsForListing verifies known trigger responses can
// wait for engine visibility and unconstrained discovery picks the latest run.
func TestFindPicksLatestAndWaitsForListing(t *testing.T) {
	t.Parallel()

	now := time.Now()
	f := &fake{
		listAfter: 2,
		listed: []listedRun{{
			ID:      "old",
			Created: now.Add(-time.Hour),
		}, {
			ID:      "new",
			Created: now,
		}, {
			ID:      "mid",
			Created: now.Add(-time.Minute),
		}},
		details_: map[string]runDetail{"new": {
			Status: statusQueued,
		}},
	}

	driver := newTestDriver(f)

	for index := range f.listed {
		f.listed[index].Workflow = driver.workflowName("order")
	}

	f.record(driver.workflowName("order"), "new", wire{
		ID:  "o-1",
		Key: driver.runKey("order", "o-1"),
	})

	runID, _, err := driver.find(t.Context(), f, runIdentity{
		workflow: driver.workflowName("order"), key: driver.runKey("order", "o-1"), logical: "o-1",
	}, time.Now().Add(driver.config.LookupGrace))

	if err != nil || runID != "new" {
		t.Fatalf("find() = %q, %v", runID, err)
	}

	if f.listCalls != 3 {
		t.Fatalf("list called %d times; want 3", f.listCalls)
	}
}

// TestUnknownRun verifies that result, status, cancel, and signal consistently
// reject an undiscovered logical execution.
func TestUnknownRun(t *testing.T) {
	t.Parallel()

	driver := newTestDriver(&fake{}, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	ctx := t.Context()

	if _, err := driver.Result(ctx, "order", "x"); !errors.Is(err, workflow.ErrNotFound) {
		t.Errorf("Result() error = %v", err)
	}

	if _, err := driver.Status(ctx, "order", "x"); !errors.Is(err, workflow.ErrNotFound) {
		t.Errorf("Status() error = %v", err)
	}

	if err := driver.Cancel(ctx, "order", "x"); !errors.Is(err, workflow.ErrNotFound) {
		t.Errorf("Cancel() error = %v", err)
	}

	if err := driver.Signal(ctx, "order", "x", "go", nil); !errors.Is(err, workflow.ErrNotFound) {
		t.Errorf("Signal() error = %v", err)
	}
}

// TestStatusRetriesInvisibleRun verifies that transient detail invisibility
// after submission is retried within the lookup grace period.
func TestStatusRetriesInvisibleRun(t *testing.T) {
	t.Parallel()

	f := &fake{
		notFound: 2,
		details_: map[string]runDetail{"run-1": {
			Status: statusCompleted,
		}},
	}
	driver := newTestDriver(f)

	if err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if runStatus, err := driver.Status(t.Context(), "order", "o-1"); err != nil || runStatus != workflow.Completed {
		t.Fatalf("Status() = %v, %v", runStatus, err)
	}
}

// requireMappedStatus verifies mapStatus translates hatchetStatus to want.
func requireMappedStatus(t *testing.T, hatchetStatus string, want workflow.Status) {
	t.Helper()

	if got, err := mapStatus(hatchetStatus); err != nil || got != want {
		t.Errorf("mapStatus(%s) = %v, %v; want %v", hatchetStatus, got, err, want)
	}
}

// TestStatusMappingQueued verifies that an accepted queued native execution is
// exposed as Running.
func TestStatusMappingQueued(t *testing.T) {
	t.Parallel()

	requireMappedStatus(t, statusQueued, workflow.Running)
}

// TestStatusMappingRunning verifies that an active native execution is exposed
// as Running.
func TestStatusMappingRunning(t *testing.T) {
	t.Parallel()

	requireMappedStatus(t, statusRunning, workflow.Running)
}

// TestStatusMappingCompleted verifies that a successful native execution is
// exposed as Completed.
func TestStatusMappingCompleted(t *testing.T) {
	t.Parallel()

	requireMappedStatus(t, statusCompleted, workflow.Completed)
}

// TestStatusMappingFailed verifies that a failed native execution is exposed as
// Failed.
func TestStatusMappingFailed(t *testing.T) {
	t.Parallel()

	requireMappedStatus(t, statusFailed, workflow.Failed)
}

// TestStatusMappingCanceled verifies that a canceled native execution is
// exposed as Canceled.
func TestStatusMappingCanceled(t *testing.T) {
	t.Parallel()

	requireMappedStatus(t, statusCanceled, workflow.Canceled)
}

// TestStatusMappingUnknown verifies that an unrecognized native state returns
// an error instead of inventing a contract status.
func TestStatusMappingUnknown(t *testing.T) {
	t.Parallel()

	if _, err := mapStatus("WEIRD"); err == nil {
		t.Error("mapStatus(WEIRD) error = nil")
	}
}

// startedDriver creates a fake-backed driver and starts the standard test run
// so result tests exercise an existing physical execution.
func startedDriver(t *testing.T, f *fake, opts ...func(*DriverBuilder)) *Driver {
	t.Helper()

	driver := newTestDriver(f, opts...)

	if err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return driver
}

// TestResultCompleted verifies that a completed task's wire envelope is decoded
// to the application payload.
func TestResultCompleted(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusCompleted,
			Output: output(t, `{"v":"done"}`),
		}},
	}

	got, err := startedDriver(t, f).Result(t.Context(), "order", "o-1")

	if err != nil || string(got) != `{"v":"done"}` {
		t.Fatalf("Result() = %q, %v", got, err)
	}
}

// failedWithEnvelope is a workflow error envelope for a permanent custom failure.
func failedWithEnvelope() string {
	return string(workflow.EncodeError(workflow.Permanent(workflow.NewError("custom", "boom"))))
}

// requireResultError verifies Result on a run with detail fails with want.
func requireResultError(t *testing.T, detail runDetail, want error) {
	t.Helper()

	f := &fake{
		details_: map[string]runDetail{"run-1": detail},
	}

	if _, err := startedDriver(t, f).Result(t.Context(), "order", "o-1"); !errors.Is(err, want) {
		t.Fatalf("Result() error = %v; want %v", err, want)
	}
}

// TestResultFailureCanceled verifies that native cancellation becomes the
// workflow cancellation sentinel.
func TestResultFailureCanceled(t *testing.T) {
	t.Parallel()

	requireResultError(t, runDetail{
		Status: statusCanceled,
	}, workflow.ErrCanceled)
}

// TestResultFailureEnvelope verifies that a native failure carrying an
// application envelope retains its error kind.
func TestResultFailureEnvelope(t *testing.T) {
	t.Parallel()

	requireResultError(t, runDetail{
		Status: statusFailed,
		Error:  failedWithEnvelope(),
	}, workflow.NewError("custom", ""))
}

// TestResultFailureQuotedEnvelope verifies that a quoted application envelope
// can still be recovered from native failure text.
func TestResultFailureQuotedEnvelope(t *testing.T) {
	t.Parallel()

	requireResultError(t, runDetail{
		Status: statusFailed,
		Error:  "task failed: " + failedWithEnvelope(),
	}, workflow.NewError("custom", ""))
}

// TestResultFailureEngineTimeout verifies that native execution-timeout text
// becomes the workflow timeout sentinel.
func TestResultFailureEngineTimeout(t *testing.T) {
	t.Parallel()

	requireResultError(t, runDetail{
		Status: statusFailed,
		Error:  "task execution timed out",
	}, workflow.ErrTimeout)
}

// TestResultFailureFacadeTimeout verifies that a serialized facade timeout
// retains the workflow timeout identity.
func TestResultFailureFacadeTimeout(t *testing.T) {
	t.Parallel()

	requireResultError(t, runDetail{
		Status: statusFailed,
		Error:  string(workflow.EncodeError(workflow.ErrTimeout)),
	}, workflow.ErrTimeout)
}

// TestResultFailurePermanentEnvelope verifies that a permanent application
// failure retains both terminal status and its original message.
func TestResultFailurePermanentEnvelope(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusFailed,
			Error:  failedWithEnvelope(),
		}},
	}

	_, err := startedDriver(t, f).Result(t.Context(), "order", "o-1")

	if !errors.Is(err, workflow.ErrPermanent) {
		t.Fatalf("Result() error = %v; want terminal", err)
	}

	var decoded *workflow.Error

	if !errors.As(err, &decoded) || decoded.Message != "boom" {
		t.Fatalf("Result() error = %#v; want message boom", err)
	}
}

// TestResultHonorsContext verifies that result polling stops when the caller's
// context is canceled.
func TestResultHonorsContext(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
	}
	driver := startedDriver(t, f)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if _, err := driver.Result(ctx, "order", "o-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Result() error = %v", err)
	}
}

// TestResultPollsUntilDone verifies that result polling waits through an active
// state and returns the eventual application payload.
func TestResultPollsUntilDone(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
	}
	driver := startedDriver(t, f)

	go func() {
		time.Sleep(30 * time.Millisecond)

		f.mutex.Lock()
		detail := f.details_["run-1"]
		detail.Status = statusCompleted
		detail.Output = output(t, "x")
		f.details_["run-1"] = detail
		f.mutex.Unlock()
	}()

	if got, err := driver.Result(t.Context(), "order", "o-1"); err != nil || string(got) != "x" {
		t.Fatalf("Result() = %q, %v", got, err)
	}
}

// TestCancel verifies that checks that cancellation resolves the physical
// execution before issuing the native cancel request.
func TestCancel(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
	}
	driver := startedDriver(t, f)

	if err := driver.Cancel(t.Context(), "order", "o-1"); err != nil || len(f.canceled) != 1 || f.canceled[0] != "run-1" {
		t.Fatalf("Cancel() = %v; canceled %v", err, f.canceled)
	}

}

// TestSignal verifies that checks physical signal delivery and first-value
// reuse without allowing duplicate values to replace the accepted marker.
func TestSignal(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
	}
	driver := startedDriver(t, f, func(driver *DriverBuilder) {
		driver.Prefix("app")
	})

	if err := driver.Signal(t.Context(), "order", "o-1", "go", []byte(`{"v":"a"}`)); err != nil {
		t.Fatalf("Signal() error = %v", err)
	}

	scope := signalScope(driver.runKey("order", "o-1"), "run-1")

	if len(f.pushed) != 1 || f.pushed[0].key != "app.sig.go" || f.pushed[0].scope != scope || f.pushed[0].payload.Marker != "run-2" {
		t.Fatalf("pushed = %+v", f.pushed)
	}

	if err := driver.Signal(t.Context(), "order", "o-1", "go", []byte(`{"v":"b"}`)); err != nil || len(f.pushed) != 2 {
		t.Fatalf("second Signal() = %v; pushed %d", err, len(f.pushed))
	}

	first, err := driver.firstValue(t.Context(), f.pushed[1].payload.Marker, signalMarkerKey(scope, "go"), "run-1")

	if err != nil || string(first) != `{"v":"a"}` {
		t.Fatalf("firstValue() = %q, %v; want the first value", first, err)
	}
}

// TestSignalNotRunning verifies that a signal cannot be delivered to a
// completed native execution.
func TestSignalNotRunning(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusCompleted,
		}},
	}

	if err := startedDriver(t, f).Signal(t.Context(), "order", "o-1", "go", nil); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Signal() error = %v", err)
	}

	if len(f.pushed) != 0 {
		t.Fatalf("pushed = %+v", f.pushed)
	}
}

// TestSignalRetriesAfterPushError verifies that a failed event write can be
// retried without losing the first-value marker.
func TestSignalRetriesAfterPushError(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
		pushErr: errors.New("down"),
	}
	driver := startedDriver(t, f)

	if err := driver.Signal(t.Context(), "order", "o-1", "go", nil); err == nil {
		t.Fatal("Signal() error = nil")
	}

	f.pushErr = nil

	if err := driver.Signal(t.Context(), "order", "o-1", "go", nil); err != nil || len(f.pushed) != 1 {
		t.Fatalf("retry Signal() = %v; pushed %d", err, len(f.pushed))
	}
}

// TestBackendUnavailable verifies that workflow operations return transport
// readiness failures rather than touching a missing backend.
func TestBackendUnavailable(t *testing.T) {
	t.Parallel()

	driver := newDriver(driverSettings())
	driver.backend = func() (backend, error) {
		return nil, errors.New("not started")
	}

	if err := driver.Start(t.Context(), "a", "b", nil, workflow.StartOptions{}); err == nil {
		t.Error("Start() error = nil")
	}

	if _, err := driver.Result(t.Context(), "a", "b"); err == nil {
		t.Error("Result() error = nil")
	}
}

// TestDriverSettingsDefaults verifies that empty configured settings normalize
// to the driver's documented defaults.
func TestDriverSettingsDefaults(t *testing.T) {
	t.Parallel()

	config := driverSettings(func(driver *DriverBuilder) {
		driver.Prefix("x")
	}, func(driver *DriverBuilder) {
		driver.Lookback(time.Hour)
	})

	if config.Prefix != "x" || config.Lookback != time.Hour {
		t.Fatalf("config = %+v", config)
	}

	driver := newDriver(driverSettings(func(driver *DriverBuilder) {
		driver.Prefix("")
	}))

	if driver.config.Prefix != DefaultPrefix || driver.config.Lookback != DefaultLookback || driver.config.EvictionTTL != DefaultEvictionTTL {
		t.Fatalf("defaults = %+v", driver.config)
	}
}

// TestConstructorsRejectNil verifies that driver and runner construction reject
// missing required host or registry values.
func TestConstructorsRejectNil(t *testing.T) {
	t.Parallel()

	if _, err := NewDriver(nil); err == nil {
		t.Error("NewDriver(nil) error = nil")
	}

	if _, err := NewRunner(nil, nil); err == nil {
		t.Error("NewRunner(nil) error = nil")
	}
}
