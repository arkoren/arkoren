package restateschedule_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/scheduletest"
	"arkoren.dev/driver/restate"
	"arkoren.dev/driver/restate/internal/itest"
	"arkoren.dev/driver/restate/internal/protocoltest"
	"arkoren.dev/driver/restate/restateschedule"
)

// endpointPort is the port of the endpoint of this test. The server reaches
// it at host.docker.internal.
const endpointPort = "19081"

// conformancePort is the endpoint port of the conformance suite.
const conformancePort = "19082"

// TestIntegration checks real recurring and deferred dispatch through the
// registered Restate endpoint.
func TestIntegration(t *testing.T) {
	var driver *restateschedule.Driver

	_, client := itest.Serve(t, endpointPort, func(host *restate.Host) {
		var err error

		driver, err = restateschedule.New(host)

		if err != nil {
			t.Fatal(err)
		}
	})

	itest.KillInvocations(t, client, "ArkorenSchedule", "ArkorenScheduleIndex", "ArkorenRunner")

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	var ticks, deferred atomic.Int32

	// A fresh name per run: the object of an earlier run keeps its state.
	entryName := "tick-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	dispatch := func(_ context.Context, name string, _ []byte) error {
		switch name {
		case entryName:
			ticks.Add(1)
		case "later":
			deferred.Add(1)
		}

		return nil
	}

	entry := schedule.NewEntry(entryName, func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Every(time.Second))
	})

	go func() {
		_ = driver.Run(ctx, []schedule.Entry{entry}, dispatch)
	}() //nolint:errcheck // ends with the test

	// Defer fails until the deployment is registered.
	for {
		_, err := driver.Defer(ctx, schedule.Job{
			Name: "later",
			At:   time.Now().Add(time.Second),
		})

		if err == nil {
			break
		}

		if ctx.Err() != nil {
			t.Fatal(err)
		}

		time.Sleep(500 * time.Millisecond)
	}

	deadline := time.Now().Add(60 * time.Second)

	for ticks.Load() < 2 || deferred.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("ticks=%d deferred=%d", ticks.Load(), deferred.Load())
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// TestIntegrationConformance serves one deployment and runs the suite against
// its driver, which every subtest reuses: the deployment is registered once.
func TestIntegrationConformance(t *testing.T) {
	var driver *restateschedule.Driver

	_, client := itest.Serve(t, conformancePort, func(host *restate.Host) {
		var err error

		driver, err = restateschedule.New(host)

		if err != nil {
			t.Fatal(err)
		}
	})

	itest.KillInvocations(t, client, "ArkorenSchedule", "ArkorenScheduleIndex", "ArkorenRunner")

	// Defer fails until the deployment is registered.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	for {
		id, err := driver.Defer(ctx, schedule.Job{
			Name: "ready",
			At:   time.Now().Add(time.Hour),
		})

		if err == nil {
			_ = driver.Cancel(ctx, id)

			break
		}

		if ctx.Err() != nil {
			t.Fatal(err)
		}

		time.Sleep(500 * time.Millisecond)
	}

	scheduletest.Run(t, func(*testing.T) schedule.Driver {
		return driver
	})
}

// TestIntegrationNullDeferredBeforeRun checks native acceptance before local
// dispatch readiness and preserves JSON null through the real ingress/endpoint.
// It follows the existing externally provisioned integration convention and
// never starts Compose or accesses services in short mode.
func TestIntegrationNullDeferredBeforeRun(t *testing.T) {
	var driver *restateschedule.Driver
	served, client := itest.Serve(t, "19084", func(host *restate.Host) {
		var err error

		driver, err = restateschedule.New(host, func(scheduler *restateschedule.Builder) {
			scheduler.Prefix("null-regression")
		})

		if err != nil {
			t.Fatal(err)
		}
	})
	itest.WaitForService(t, served, "NullRegressionRunner")
	itest.KillInvocations(t, client, "NullRegressionRunner", "NullRegressionScheduleIndex")

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)

	if _, err := driver.Defer(ctx, schedule.Job{Name: "remind", Payload: []byte(`null`), At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	payloads := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- driver.Run(ctx, nil, func(ctx context.Context, _ string, payload []byte) error {
			select {
			case payloads <- string(payload):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	select {
	case payload := <-payloads:
		if payload != "null" {
			t.Fatalf("deferred payload = %q, want null", payload)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// TestRunCancellationDuringPublicationIsClean covers cancellation while the
// ingress is unavailable, rather than only cancellation after reconciliation.
func TestRunCancellationDuringPublicationIsClean(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		cancel()
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	host := restate.New(func(host *restate.Builder) {
		host.IngressURL(server.URL)
	})
	driver, err := restateschedule.New(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Run(ctx, nil, nil); err != nil {
		t.Fatalf("Run cancellation = %v, want nil", err)
	}
}

// TestDeferredNullPayloadReachesDispatch preserves an explicit JSON null across
// public deferral, ingress encoding, and the registered endpoint handler.
// Its first delivery precedes Run and must remain retryable, not consume the job.
func TestDeferredNullPayloadReachesDispatch(t *testing.T) {
	t.Parallel()

	published := make(chan struct{}, 1)
	jobs := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)

		if err != nil {
			t.Error(err)

			return
		}

		if request.URL.Path == "/ArkorenRunner/Run/send" {
			jobs <- body
		} else {
			published <- struct{}{}
		}

		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"invocationId":"inv_test","status":"Accepted"}`))
	}))
	t.Cleanup(server.Close)
	host := restate.New(func(host *restate.Builder) {
		host.IngressURL(server.URL)
		host.Addr("127.0.0.1:0")
		host.AllowInsecure(true)
	})
	driver, err := restateschedule.New(host)

	if err != nil {
		t.Fatal(err)
	}

	protocoltest.Start(t, host)

	if _, err := driver.Defer(t.Context(), schedule.Job{Name: "remind", Payload: []byte(`null`), At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	invocation := protocoltest.Invocation{Path: "/invoke/ArkorenRunner/Run", Input: <-jobs}
	first := protocoltest.Invoke(t, host, invocation)

	if first[len(first)-1].Type != 2 {
		t.Fatal("delivery before dispatch readiness was not retryable")
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	payloads := make(chan string, 1)
	go func() {
		done <- driver.Run(ctx, nil, func(_ context.Context, _ string, payload []byte) error {
			payloads <- string(payload)

			return nil
		})
	}()
	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	<-published

	frames := protocoltest.Replay(t, host, invocation, first)

	if frames[len(frames)-1].Type != 3 {
		t.Fatal("deferred handler did not complete")
	}

	select {
	case payload := <-payloads:
		if payload != "null" {
			t.Fatalf("dispatch payload = %q, want JSON null", payload)
		}
	default:
		t.Fatal("completed deferred invocation skipped dispatch")
	}
}

// TestRunEscapesPrefixInIndexKey verifies that a prefix separator cannot select
// another ingress path even though service-name normalization removes it.
func TestRunEscapesPrefixInIndexKey(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths <- request.URL.EscapedPath()
		cancel()
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"invocationId":"inv_test","status":"Accepted"}`))
	}))
	t.Cleanup(server.Close)
	host := restate.New(func(host *restate.Builder) {
		host.IngressURL(server.URL)
	})
	driver, err := restateschedule.New(host, func(scheduler *restateschedule.Builder) {
		scheduler.Prefix("org/a")
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Run(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}

	if path := <-paths; path != "/OrgAScheduleIndex/org%2Fa/Sync/send" {
		t.Fatalf("index request = %q", path)
	}
}

// TestRemovedScheduleCannotReviveOldTick replays the endpoint's state commands
// between invocations, then delivers a tick queued before removal and re-addition.
func TestRemovedScheduleCannotReviveOldTick(t *testing.T) {
	t.Parallel()

	host := restate.New(func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
		host.AllowInsecure(true)
	})

	if _, err := restateschedule.New(host); err != nil {
		t.Fatal(err)
	}

	protocoltest.Start(t, host)

	state := map[string][]byte{}

	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenSchedule/Ensure", Key: "hourly", State: state,
		Input: []byte(`{"Expression":"0 * * * *","Location":"UTC","Hash":"first"}`),
	})
	applyObjectState(t, state, frames)

	frames = protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenSchedule/Remove", Key: "hourly", State: state,
	})
	applyObjectState(t, state, frames)

	frames = protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenSchedule/Ensure", Key: "hourly", State: state,
		Input: []byte(`{"Expression":"30 * * * *","Location":"UTC","Hash":"second"}`),
	})
	applyObjectState(t, state, frames)

	frames = protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenSchedule/Tick", Key: "hourly", State: state,
		Input: []byte(`{"Gen":1,"At":1}`),
	})

	if frames[len(frames)-1].Type != 3 {
		t.Fatal("obsolete tick did not complete cleanly")
	}

	for _, frame := range frames {
		if frame.Type == 0x040e {
			t.Fatal("obsolete tick submitted another scheduled invocation")
		}
	}
}

// applyObjectState applies the public protocol's Set, Clear, and ClearAll
// commands to the next invocation's eager state snapshot. It models engine
// persistence only; no application handler or private driver method is replaced.
func applyObjectState(t *testing.T, state map[string][]byte, frames []protocoltest.Frame) {
	t.Helper()

	if frames[len(frames)-1].Type != 3 {
		t.Fatal("state-changing invocation did not complete")
	}

	for _, frame := range frames {
		switch frame.Type {
		case 0x0403:
			state[string(frame.Bytes(1))] = protocoltest.Frame{Body: frame.Bytes(3)}.Bytes(1)
		case 0x0404:
			delete(state, string(frame.Bytes(1)))
		case 0x0405:
			clear(state)
		}
	}
}

// TestRecurringPayloadStaysNilForLegacyMessages preserves the raw Dispatch
// contract for recurring messages emitted before null task handling was fixed.
func TestRecurringPayloadStaysNilForLegacyMessages(t *testing.T) {
	t.Parallel()

	ensured := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/ArkorenSchedule/hourly/Ensure/send" {
			ensured <- struct{}{}
		}

		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"invocationId":"inv_test","status":"Accepted"}`))
	}))
	t.Cleanup(server.Close)
	host := restate.New(func(host *restate.Builder) {
		host.IngressURL(server.URL)
		host.Addr("127.0.0.1:0")
		host.AllowInsecure(true)
	})
	driver, err := restateschedule.New(host)

	if err != nil {
		t.Fatal(err)
	}

	protocoltest.Start(t, host)
	entry := schedule.NewEntry("hourly", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Every(time.Hour))
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	payloads := make(chan []byte, 1)
	go func() {
		done <- driver.Run(ctx, []schedule.Entry{entry}, func(_ context.Context, _ string, payload []byte) error {
			payloads <- payload

			return nil
		})
	}()
	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	<-ensured

	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
		Path: "/invoke/ArkorenRunner/Run", Input: []byte(`{"Name":"hourly","Payload":null}`),
	})

	if frames[len(frames)-1].Type != 3 {
		t.Fatal("recurring handler did not complete")
	}

	select {
	case payload := <-payloads:
		if payload != nil {
			t.Fatalf("recurring payload = %q, want nil", payload)
		}
	default:
		t.Fatal("completed recurring invocation skipped dispatch")
	}
}
