package scheduletest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/schedule"
)

// Option changes how [Run] executes the suite. Options describe the
// capabilities of the driver under test, never the expected behavior.
type Option func(*options)

// options holds the settings of one [Run].
type options struct {
	// synctest runs scenarios with a virtual clock inside isolated bubbles.
	synctest bool

	// withoutDefer requires explicit rejection of deferred-job submission.
	withoutDefer bool

	// minInterval is the shortest calendar cadence supported by the driver.
	minInterval time.Duration
}

// WithSynctest runs every subtest inside a testing/synctest bubble, so waits
// take no real time. Use it for drivers that live in the process and use the
// time package, such as the memory driver. Never use it with a driver that
// talks to an external engine.
func WithSynctest() Option {
	return func(opts *options) {
		opts.synctest = true
	}
}

// WithoutDefer declares that the driver has no deferred jobs. The suite then
// checks that Defer returns [schedule.ErrUnsupported] instead of running the
// subtests about deferred jobs.
func WithoutDefer() Option {
	return func(opts *options) {
		opts.withoutDefer = true
	}
}

// WithMinInterval sets the shortest period at which the driver can fire an
// entry, for engines with a coarse clock, such as one minute for a cron
// engine. The default is one second. The entry subtest waits for several
// periods.
func WithMinInterval(d time.Duration) Option {
	return func(opts *options) {
		opts.minInterval = d
	}
}

// suite is the state of one subtest.
type suite struct {
	// t reports failures and owns cleanup for the current conformance scenario.
	t *testing.T

	// driver is the fresh implementation exercised by this scenario.
	driver schedule.Driver

	// opts holds the capability and timing settings selected by Run.
	opts options

	// name returns a name for an entry or job that is unique to this run of
	// the suite, so the suite can run again against an engine that keeps its
	// schedules.
	name string
}

// call is one dispatch seen by a driver under test.
type call struct {
	// name identifies the calendar entry or deferred task that fired.
	name string

	// payload is a detached copy of the bytes delivered by the driver.
	payload []byte
}

// dispatcher collects the calls of a driver per job name and can fail chosen
// jobs.
type dispatcher struct {
	// mu guards per-name queues and scripted dispatch failures.
	mu sync.Mutex

	// calls retains a buffered arrival queue for each observed name.
	calls map[string]chan call

	// fail selects the error returned when a given name is dispatched.
	fail map[string]error
}

// newDispatcher returns an empty dispatcher.
func newDispatcher() *dispatcher {
	return &dispatcher{
		calls: map[string]chan call{},
		fail:  map[string]error{},
	}
}

// channel returns the buffered channel that receives the calls of name.
func (dispatcher *dispatcher) channel(name string) chan call {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()

	if dispatcher.calls[name] == nil {
		dispatcher.calls[name] = make(chan call, 64)
	}

	return dispatcher.calls[name]
}

// dispatch is the [schedule.Dispatch] given to the driver.
func (dispatcher *dispatcher) dispatch(_ context.Context, name string, payload []byte) error {
	dispatcher.mu.Lock()

	err := dispatcher.fail[name]
	dispatcher.mu.Unlock()

	select {
	case dispatcher.channel(name) <- call{
		name:    name,
		payload: append([]byte(nil), payload...),
	}:
	default:
	}

	return err
}

// Run runs the conformance suite against the drivers that newDriver returns.
// It calls newDriver once per subtest, and subtests run one after another,
// because every driver has a single dispatch function. Register cleanup of the
// driver, such as stopping a host, with t.Cleanup.
//
// The suite covers: Run blocks until its context is done and then returns nil;
// an entry fires and receives a nil payload; Defer returns a distinct
// non-empty identifier, also before Run starts; a deferred job is dispatched
// with its payload, also when it is already due; Cancel prevents a dispatch
// and accepts unknown identifiers; and an error from dispatch does not end
// Run.
//
// # Examples
//
//	scheduletest.Run(t, func(t *testing.T) schedule.Driver {
//		return memoryschedule.New()
//	}, scheduletest.WithSynctest())
func Run(t *testing.T, newDriver func(t *testing.T) schedule.Driver, opts ...Option) {
	t.Helper()

	settings := options{
		minInterval: time.Second,
	}

	for _, opt := range opts {
		opt(&settings)
	}

	var token [4]byte

	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		// name describes the contract assertion shown in test output.
		name string

		// run executes the assertion against a fresh scenario state.
		run func(*suite)
	}{
		{
			name: "run blocks until canceled",
			run:  testRunBlocks,
		},
		{
			name: "entry fires without payload",
			run:  testEntryFires,
		},
		{
			name: "dispatch error does not end run",
			run:  testDispatchError,
		},
	}

	if settings.withoutDefer {
		cases = append(cases, struct {
			// name describes the unsupported-operation assertion.
			name string

			// run checks rejection instead of deferred execution.
			run func(*suite)
		}{
			name: "defer is unsupported",
			run:  testDeferUnsupported,
		})
	} else {
		cases = append(cases, deferCases()...)
	}

	for index, tc := range cases {
		name := fmt.Sprintf("conf-%s-%d", hex.EncodeToString(token[:]), index)

		t.Run(tc.name, func(t *testing.T) {
			if settings.synctest {
				synctest.Test(t, func(t *testing.T) {
					tc.run(&suite{
						t:      t,
						driver: newDriver(t),
						opts:   settings,
						name:   name,
					})
				})

				return
			}

			tc.run(&suite{
				t:      t,
				driver: newDriver(t),
				opts:   settings,
				name:   name,
			})
		})
	}
}

// deferCases returns the subtests about deferred jobs.
func deferCases() []struct {
	// name describes a deferred-job contract assertion.
	name string

	// run executes the assertion with its scenario's driver.
	run func(*suite)
} {
	return []struct {
		// name is the deferred-job scenario label used in test output.
		name string

		// run checks one deferred-job operation or ordering guarantee.
		run func(*suite)
	}{
		{
			name: "defer fires with payload",
			run:  testDeferFires,
		},
		{
			name: "defer before run",
			run:  testDeferBeforeRun,
		},
		{
			name: "defer in the past fires",
			run:  testDeferPast,
		},
		{
			name: "defer ids are distinct",
			run:  testDeferIDs,
		},
		{
			name: "cancel prevents dispatch",
			run:  testCancel,
		},
		{
			name: "cancel unknown id",
			run:  testCancelUnknown,
		},
	}
}

// running is a driver whose Run is in progress.
type running struct {
	// stop cancels the active Run context; callers then wait on done.
	stop func()

	// done receives Run's single return value after dispatches finish.
	done chan error
}

// start runs the driver with entries in a goroutine until stop is called or
// the subtest ends. The goroutine ends once its context is canceled.
func (suite *suite) start(dispatched *dispatcher, entries ...schedule.Entry) *running {
	suite.t.Helper()

	ctx, cancel := context.WithCancel(suite.t.Context())
	running := &running{
		stop: cancel,
		done: make(chan error, 1),
	}

	go func() {
		running.done <- suite.driver.Run(ctx, entries, dispatched.dispatch)
	}()

	suite.t.Cleanup(cancel)

	return running
}

// stopped cancels Run and fails the test unless it returns nil in time.
func (suite *suite) stopped(running *running) {
	suite.t.Helper()

	running.stop()

	select {
	case err := <-running.done:
		if err != nil {
			suite.t.Fatalf("Run returned %v after cancel; want nil", err)
		}
	case <-time.After(30 * time.Second):
		suite.t.Fatal("Run did not return after cancel")
	}
}

// expect waits for the next dispatch of name, failing the test after timeout.
func (suite *suite) expect(dispatched *dispatcher, name string, timeout time.Duration) call {
	suite.t.Helper()

	select {
	case got := <-dispatched.channel(name):
		return got
	case <-time.After(timeout):
		suite.t.Fatalf("no dispatch of %q within %s", name, timeout)

		return call{}
	}
}

// expectNone fails the test when name is dispatched within d.
func (suite *suite) expectNone(dispatched *dispatcher, name string, d time.Duration) {
	suite.t.Helper()

	select {
	case <-dispatched.channel(name):
		suite.t.Fatalf("unexpected dispatch of %q", name)
	case <-time.After(d):
	}
}

// entry declares a successful handler named after the subtest, firing every
// interval. Conformance tests pass the immutable entry to the raw driver's Run;
// no handler or timer runs during construction.
func (suite *suite) entry(interval time.Duration) schedule.Entry {
	return schedule.NewEntry(suite.name, func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Every(interval))
	})
}

// fireTimeout is how long a firing may take, a margin over real engines.
func (suite *suite) fireTimeout(delay time.Duration) time.Duration {
	return delay + 30*time.Second
}

// testRunBlocks checks that the driver remains active without registrations
// until its context ends, then returns successfully within the suite bound.
func testRunBlocks(s *suite) {
	r := s.start(newDispatcher())

	select {
	case err := <-r.done:
		s.t.Fatalf("Run returned %v before its context ended", err)
	case <-time.After(200 * time.Millisecond):
	}

	s.stopped(r)
}

// testEntryFires checks that a calendar registration dispatches by name and
// carries no task payload at the driver's supported minimum cadence.
func testEntryFires(s *suite) {
	d := newDispatcher()
	r := s.start(d, s.entry(s.opts.minInterval))

	got := s.expect(d, s.name, s.fireTimeout(3*s.opts.minInterval))

	if len(got.payload) != 0 {
		s.t.Fatalf("entry payload = %q; want none", got.payload)
	}

	s.stopped(r)
}

// testDispatchError submits failing and successful jobs to the same running
// driver and checks that dispatch failure does not terminate Run.
func testDispatchError(s *suite) {
	d := newDispatcher()
	boom := s.name + "-boom"
	d.fail[boom] = errors.New("boom")

	r := s.start(d)

	for _, name := range []string{boom, s.name} {
		if _, err := s.driver.Defer(s.t.Context(), schedule.Job{
			Name: name,
			At:   time.Now().Add(time.Second),
		}); err != nil {
			s.t.Fatalf("Defer: %v", err)
		}
	}

	s.expect(d, boom, s.fireTimeout(time.Second))
	s.expect(d, s.name, s.fireTimeout(time.Second))

	select {
	case err := <-r.done:
		s.t.Fatalf("Run returned %v after a dispatch error", err)
	default:
	}

	s.stopped(r)
}

// testDeferUnsupported requires the contract sentinel from drivers declaring
// no deferred-job support instead of silently omitting that assertion.
func testDeferUnsupported(s *suite) {
	_, err := s.driver.Defer(s.t.Context(), schedule.Job{
		Name: s.name,
		At:   time.Now().Add(time.Hour),
	})

	if !errors.Is(err, schedule.ErrUnsupported) {
		s.t.Fatalf("Defer error = %v; want ErrUnsupported", err)
	}
}

// testDeferFires checks that a newly deferred job receives an identifier and
// delivers its exact bytes when its due time arrives.
func testDeferFires(s *suite) {
	d := newDispatcher()
	r := s.start(d)

	id, err := s.driver.Defer(s.t.Context(), schedule.Job{
		Name:    s.name,
		Payload: []byte(`{"k":"v"}`),
		At:      time.Now().Add(time.Second),
	})

	if err != nil || id == "" {
		s.t.Fatalf("Defer = %q, %v; want an identifier", id, err)
	}

	got := s.expect(d, s.name, s.fireTimeout(time.Second))

	if string(got.payload) != `{"k":"v"}` {
		s.t.Fatalf("payload = %q", got.payload)
	}

	s.stopped(r)
}

// testDeferBeforeRun checks that deferred registrations are accepted before
// Run installs the callback and are later delivered to that callback.
func testDeferBeforeRun(s *suite) {
	d := newDispatcher()

	if _, err := s.driver.Defer(s.t.Context(), schedule.Job{
		Name: s.name,
		At:   time.Now().Add(time.Second),
	}); err != nil {
		s.t.Fatalf("Defer before Run: %v", err)
	}

	r := s.start(d)

	s.expect(d, s.name, s.fireTimeout(time.Second))
	s.stopped(r)
}

// testDeferPast checks that a job whose due time is already past is dispatched
// promptly rather than lost or delayed until another calendar tick.
func testDeferPast(s *suite) {
	d := newDispatcher()
	r := s.start(d)

	if _, err := s.driver.Defer(s.t.Context(), schedule.Job{
		Name: s.name,
		At:   time.Now().Add(-time.Hour),
	}); err != nil {
		s.t.Fatalf("Defer: %v", err)
	}

	s.expect(d, s.name, s.fireTimeout(0))
	s.stopped(r)
}

// testDeferIDs checks that repeated submissions receive distinct nonempty
// identifiers and cleans up both pending registrations afterward.
func testDeferIDs(s *suite) {
	job := schedule.Job{
		Name: s.name,
		At:   time.Now().Add(24 * time.Hour),
	}

	first, err := s.driver.Defer(s.t.Context(), job)

	if err != nil {
		s.t.Fatalf("Defer: %v", err)
	}

	second, err := s.driver.Defer(s.t.Context(), job)

	if err != nil {
		s.t.Fatalf("Defer: %v", err)
	}

	if first == "" || first == second {
		s.t.Fatalf("identifiers %q and %q; want distinct and non-empty", first, second)
	}

	for _, id := range []string{first, second} {
		if err := s.driver.Cancel(s.t.Context(), id); err != nil {
			s.t.Fatalf("Cancel: %v", err)
		}
	}
}

// testCancel checks that canceling a known pending job prevents its dispatch
// after the original due time has passed.
func testCancel(s *suite) {
	d := newDispatcher()
	r := s.start(d)

	id, err := s.driver.Defer(s.t.Context(), schedule.Job{
		Name: s.name,
		At:   time.Now().Add(3 * time.Second),
	})

	if err != nil {
		s.t.Fatalf("Defer: %v", err)
	}

	if err := s.driver.Cancel(s.t.Context(), id); err != nil {
		s.t.Fatalf("Cancel: %v", err)
	}

	s.expectNone(d, s.name, 8*time.Second)
	s.stopped(r)
}

// testCancelUnknown checks repeatable successful cancellation of an identifier
// that was never deferred, as required for idempotent cleanup.
func testCancelUnknown(s *suite) {
	for range 2 {
		if err := s.driver.Cancel(s.t.Context(), "unknown-"+s.name); err != nil {
			s.t.Fatalf("Cancel of an unknown id: %v", err)
		}
	}
}
