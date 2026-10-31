package restateschedule

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/driver/restate"
)

// sent records one send.
type sent struct {
	// service identifies the target Restate service recorded by the fake transport.
	service string

	// key identifies the resource within its owning workflow or signal scope.
	key string

	// handler identifies the target handler within the recorded service call.
	handler string
	// input retains the invocation input supplied by the driver.
	input any
	// options retains the delay and idempotency settings supplied with the
	// invocation.
	options sendOptions
}

// fakeGateway records sends.
type fakeGateway struct {
	// sends records submitted invocations in call order.
	sends []sent
}

// sendObject records the keyed-object invocation and its delivery options, then
// returns a deterministic invocation identifier.
func (fake *fakeGateway) sendObject(_ context.Context, service, key, handler string, input any, options sendOptions) (string, error) {
	fake.sends = append(fake.sends, sent{
		service,
		key,
		handler,
		input,
		options,
	})

	return "id", nil
}

// sendService records the stateless-service invocation and its delivery
// options, then returns a deterministic invocation identifier.
func (fake *fakeGateway) sendService(_ context.Context, service, handler string, input any, options sendOptions) (string, error) {
	fake.sends = append(fake.sends, sent{
		service,
		"",
		handler,
		input,
		options,
	})

	return "id", nil
}

// cancel accepts every cancellation without contacting an engine. Tests of
// administrative response mapping use a real HTTP gateway instead of this fake.
func (*fakeGateway) cancel(context.Context, string) error {
	return nil
}

// newDriver constructs a scheduler and installs a recording gateway, returning
// both for assertions. Invalid construction fails the calling test.
func newDriver(t *testing.T, opts ...func(*Builder)) (*Driver, *fakeGateway) {
	t.Helper()

	driver, err := New(restate.New(), opts...)

	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeGateway{}
	driver.gateway = fake

	return driver, fake
}

// TestServiceNames verifies the ordinary prefix's schedule, index, and runner
// identities so all three native registrations use the same namespace.
func TestServiceNames(t *testing.T) {
	t.Parallel()

	prefix := "arkoren"
	want := serviceNames{
		schedule: "ArkorenSchedule",
		index:    "ArkorenScheduleIndex",
		runner:   "ArkorenRunner",
	}

	if got := newServiceNames(prefix); got != want {
		t.Errorf("newServiceNames(%q) = %v, want %v", prefix, got, want)
	}
}

// TestServiceNamesPunctuatedPrefix verifies that punctuation is normalized
// consistently across the schedule, index, and runner service identities.
func TestServiceNamesPunctuatedPrefix(t *testing.T) {
	t.Parallel()

	prefix := "my-app.v2"
	want := serviceNames{
		schedule: "MyAppV2Schedule",
		index:    "MyAppV2ScheduleIndex",
		runner:   "MyAppV2Runner",
	}

	if got := newServiceNames(prefix); got != want {
		t.Errorf("newServiceNames(%q) = %v, want %v", prefix, got, want)
	}
}

// TestServiceNamesUnderscorePrefix verifies that an underscore is normalized
// identically in each native service name derived from the same prefix.
func TestServiceNamesUnderscorePrefix(t *testing.T) {
	t.Parallel()

	prefix := "already_ok"
	want := serviceNames{
		schedule: "AlreadyOkSchedule",
		index:    "AlreadyOkScheduleIndex",
		runner:   "AlreadyOkRunner",
	}

	if got := newServiceNames(prefix); got != want {
		t.Errorf("newServiceNames(%q) = %v, want %v", prefix, got, want)
	}
}

// TestHashStable verifies that the schedule digest is repeatable and
// distinguishes changes to timing, zone, and overlap policy.
func TestHashStable(t *testing.T) {
	t.Parallel()

	a := specHash("* * * * *", "UTC", false)

	if a != specHash("* * * * *", "UTC", false) {
		t.Fatal("hash is not stable")
	}

	for _, other := range []string{specHash("* * * * *", "UTC", true), specHash("* * * * *", "Europe/Paris", false), specHash("0 * * * *", "UTC", false)} {
		if other == a {
			t.Fatal("hash collision between different specs")
		}
	}
}

// TestNextAfterMatchesEntry verifies that native serialized schedule settings
// produce the same next occurrence as the published entry.
func TestNextAfterMatchesEntry(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("Europe/Paris")

	if err != nil {
		t.Skip("no tzdata")
	}

	entry := schedule.NewEntry("job", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.In(location).
			Weekdays().
			Hour(9).
			Minute(30))
	})
	spec := entry.Spec()
	input := specInput{
		Expression: spec.Expression(),
		Location:   spec.Location().String(),
	}

	after := time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC)

	got, err := nextAfter(input, after)

	if err != nil {
		t.Fatal(err)
	}

	if want := entry.Next(after); !got.Equal(want) {
		t.Fatalf("nextAfter = %v, want %v", got, want)
	}
}

// TestNextAfterErrors verifies that invalid cron expressions and unknown time
// zones fail before a next occurrence is returned.
func TestNextAfterErrors(t *testing.T) {
	t.Parallel()

	if _, err := nextAfter(specInput{
		Expression: "bad",
		Location:   "UTC",
	}, time.Now()); err == nil {
		t.Fatal("expected error for bad expression")
	}

	if _, err := nextAfter(specInput{
		Expression: "* * * * *",
		Location:   "Nowhere/Land",
	}, time.Now()); err == nil {
		t.Fatal("expected error for bad location")
	}
}

// TestRunRejectsLocal verifies that machine-local time zones are rejected
// before any durable schedule invocation is submitted.
func TestRunRejectsLocal(t *testing.T) {
	t.Parallel()

	driver, fake := newDriver(t)
	entry := schedule.NewEntry("job", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.In(time.Local).Every(time.Minute))
	})

	err := driver.Run(t.Context(), []schedule.Entry{entry}, nil)

	if !errors.Is(err, schedule.ErrUnsupported) {
		t.Fatalf("Run = %v, want ErrUnsupported", err)
	}

	if len(fake.sends) != 0 {
		t.Fatal("Run sent before validating")
	}
}

// TestRunSends verifies that Run publishes the declaration index and per-entry
// ensure calls before blocking on cancellation.
func TestRunSends(t *testing.T) {
	t.Parallel()

	driver, fake := newDriver(t)
	entry := schedule.NewEntry("job", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Every(time.Minute))
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Run(ctx, []schedule.Entry{entry}, func(context.Context, string, []byte) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(fake.sends) != 2 || fake.sends[0].handler != "Sync" || fake.sends[1].handler != "Ensure" {
		t.Fatalf("sends = %+v", fake.sends)
	}

	input, ok := fake.sends[1].input.(specInput)

	if !ok || fake.sends[1].options.idempotencyKey != "" || input.Hash == "" || fake.sends[1].service != "ArkorenSchedule" {
		t.Fatalf("ensure = %+v", fake.sends[1])
	}

	if driver.currentDispatch() == nil {
		t.Fatal("dispatch not stored")
	}
}

// TestDeferSends verifies that one-shot submission carries the job payload and
// requested delay to the stateless runner service.
func TestDeferSends(t *testing.T) {
	t.Parallel()

	driver, fake := newDriver(t, func(scheduler *Builder) {
		scheduler.Prefix("x")
	})

	id, err := driver.Defer(t.Context(), schedule.Job{
		Name:    "t",
		Payload: []byte(`1`),
		At:      time.Now().Add(-time.Hour),
	})

	if err != nil || id != "id" {
		t.Fatalf("Defer = %q, %v", id, err)
	}

	if got := fake.sends[0]; got.service != "XRunner" || got.handler != "Run" || got.options.delay != 0 {
		t.Fatalf("send = %+v", got)
	}
}

// TestCancelStatusMapping verifies that accepted, missing, and completed native
// invocation responses all count as successful cancellation.
func TestCancelStatusMapping(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNotFound, http.StatusConflict} {
		admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPatch || r.URL.Path != "/invocations/inv_1/cancel" {
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			w.WriteHeader(status)
		}))

		driver, err := New(restate.New(func(host *restate.Builder) {
			host.AdminURL(admin.URL)
		}))

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Cancel(t.Context(), "inv_1"); err != nil {
			t.Errorf("status %d: err = %v", status, err)
		}

		admin.Close()
	}
}

// TestCancelStatusMappingFailure verifies that an unexpected administrative
// failure remains an error instead of being normalized to successful deletion.
func TestCancelStatusMappingFailure(t *testing.T) {
	t.Parallel()

	status := http.StatusInternalServerError
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/invocations/inv_1/cancel" {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		w.WriteHeader(status)
	}))
	t.Cleanup(admin.Close)

	driver, err := New(restate.New(func(host *restate.Builder) {
		host.AdminURL(admin.URL)
	}))

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Cancel(t.Context(), "inv_1"); err == nil {
		t.Errorf("status %d: err = %v", status, err)
	}
}

// TestInvoke verifies that dispatch fails before initialization, reports
// application errors, and preserves unknown-name handling.
func TestInvoke(t *testing.T) {
	t.Parallel()

	driver, _ := newDriver(t)

	if err := driver.invoke(t.Context(), "a", nil); err == nil {
		t.Fatal("nil dispatch must fail")
	}

	driver.dispatch = func(context.Context, string, []byte) error {
		return schedule.ErrUnknownName
	}

	if err := driver.invoke(t.Context(), "a", nil); !errors.Is(err, schedule.ErrUnknownName) {
		t.Fatalf("unknown = %v", err)
	}

	driver.dispatch = func(context.Context, string, []byte) error {
		return errors.New("boom")
	}

	if err := driver.invoke(t.Context(), "a", nil); err == nil || errors.Is(err, schedule.ErrUnknownName) {
		t.Fatalf("boom = %v", err)
	}
}
