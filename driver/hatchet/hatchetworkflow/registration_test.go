package hatchetworkflow_test

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	hatchetsdk "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/workflowtest"
	hatchethost "arkoren.dev/driver/hatchet"
	"arkoren.dev/driver/hatchet/hatchetworkflow"
	"arkoren.dev/driver/hatchet/internal/itest"
)

// TestIntegrationChildAndSpawnUnsupported proves containment against the real
// engine, not a memo fake: rejected parents leave no physical child or effect.
// An external Start of that same child is the positive control for registration,
// result decoding, the counter, and eventual visibility in Hatchet's run listing.
func TestIntegrationChildAndSpawnUnsupported(t *testing.T) {
	t.Parallel()

	var effects atomic.Int32
	child := workflow.New("contained-child", func(c *workflow.Context, _ string) (string, error) {
		effects.Add(1)

		return c.Raw().(hatchetsdk.DurableContext).WorkflowRunId(), nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})
	parent := workflow.New("contained-parent", func(c *workflow.Context, operation string) (string, error) {
		if operation == "child" {
			return c.Child(child, c.ID()+"-child", "rejected")
		}

		_, err := c.Spawn(child, c.ID()+"-child", "rejected")

		return "", err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(parent, child)

	if err != nil {
		t.Fatal(err)
	}

	uniquePrefix := "sec013-" + uuid.NewString()
	runner, host := startRunner(t, registry, func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(uniquePrefix)
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	api := workflow.NewClient(runner.Driver())

	for _, operation := range []string{"child", "spawn"} {
		run, err := api.Start(ctx, parent, operation, operation)

		if err != nil {
			t.Fatal(err)
		}

		if _, err := run.Result(ctx); !errors.Is(err, workflow.ErrUnsupported) {
			t.Fatalf("%s parent Result = %v; want decoded ErrUnsupported", operation, err)
		}
	}

	if got := effects.Load(); got != 0 {
		t.Fatalf("rejected child effects = %d; want 0", got)
	}

	// Resolve the actual child workflow UUID so parent and marker executions
	// cannot conceal a physical child behind a name or idempotency-key filter.
	name := uniquePrefix + "-wf-" + child.Name()

	definitions, err := host.Client().Workflows().List(ctx, &rest.WorkflowListParams{
		Name: &name,
	})

	if err != nil {
		t.Fatal(err)
	}

	var workflowIDs []uuid.UUID

	if definitions.Rows != nil {
		for _, row := range *definitions.Rows {
			if row.Name == name {
				id, err := uuid.Parse(row.Metadata.Id)

				if err != nil {
					t.Fatal(err)
				}

				workflowIDs = append(workflowIDs, id)
			}
		}
	}

	if len(workflowIDs) != 1 {
		t.Fatalf("exact child workflow IDs = %v; want one", workflowIDs)
	}

	filter := rest.V1WorkflowRunListParams{
		Since:       time.Now().Add(-time.Hour),
		WorkflowIds: &workflowIDs,
	}

	rows, err := host.Client().Runs().List(ctx, filter)

	if err != nil || len(rows.Rows) != 0 {
		t.Fatalf("physical children after rejection = %v, %v; want none", rows, err)
	}

	control, err := api.Start(ctx, child, "external-control", "allowed")

	if err != nil {
		t.Fatal(err)
	}

	physicalID, err := control.Result(ctx)

	if err != nil {
		t.Fatal(err)
	}

	// Listing is eventually consistent. Wait for the completed positive control,
	// then require it to be the only child execution, including queued runs.
	ticker := time.NewTicker(25 * time.Millisecond)
	t.Cleanup(ticker.Stop)

	for {
		rows, err := host.Client().Runs().List(ctx, filter)

		if err != nil {
			t.Fatal(err)
		}

		if len(rows.Rows) > 1 {
			t.Fatalf("physical children = %d; want only the external control", len(rows.Rows))
		}

		if len(rows.Rows) == 1 {
			row := rows.Rows[0]

			if row.WorkflowRunExternalId.String() != physicalID && row.TaskExternalId.String() != physicalID {
				t.Fatalf("unexpected child execution: %+v; want %s", row, physicalID)
			}

			break
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("external control did not reach the listing: %v", ctx.Err())
		}
	}

	if got := effects.Load(); got != 1 {
		t.Fatalf("effects after external Start = %d; want only the control's effect", got)
	}

	t.Logf("Child and Spawn: rejected parents=2, submitted children=0, effects=0; external control=%s, physical runs=1, effects=1", physicalID)
}

// prefix names the workflows of the tests. Run ids are unique per run, so the
// engine can keep its history between runs.
const prefix = "arkoren-it"

// startRunner starts one worker for registry and returns the runner.
func startRunner(t *testing.T, registry *workflow.Registry, options ...func(*hatchetworkflow.RunnerBuilder)) (*hatchetworkflow.Runner, *hatchethost.Host) {
	t.Helper()

	return startNamespaced(t, registry, "", options...)
}

// startNamespaced is startRunner with a Hatchet namespace.
func startNamespaced(t *testing.T, registry *workflow.Registry, namespace string, options ...func(*hatchetworkflow.RunnerBuilder)) (*hatchetworkflow.Runner, *hatchethost.Host) {
	t.Helper()

	host := hatchethost.New(itest.Options(t, func(host *hatchethost.Builder) {
		host.Namespace(namespace)
	})...)

	runner, err := hatchetworkflow.NewRunner(host, registry, append([]func(*hatchetworkflow.RunnerBuilder){func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(prefix)
	}}, options...)...)

	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	if err := host.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		_ = host.Stop(stop)
	})

	return runner, host
}

// startClientOnly owns a query/command client independently of replaceable worker
// hosts. Run handles retain their originating driver, so recovery tests must not
// keep querying through a stopped worker's terminal lifecycle.
func startClientOnly(t *testing.T, namespace, prefix string) *workflow.Client {
	t.Helper()

	host := hatchethost.New(itest.Options(t, func(host *hatchethost.Builder) {
		host.Namespace(namespace)
		host.NoWorker(true)
	})...)

	driver, err := hatchetworkflow.NewDriver(host, func(driver *hatchetworkflow.DriverBuilder) {
		driver.Prefix(prefix)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return workflow.NewClient(driver)
}

// echo is the shared integration workflow used to check submission from a
// client-only process.
var echo = workflow.New("it-echo", func(_ *workflow.Context, in string) (string, error) {
	return in + "!", nil
}, func(definition *workflow.Builder) {
	definition.MaxDuration(time.Minute)
})

// TestIntegration starts one worker for the conformance definitions and runs
// the suite and the client-only checks against it.
func TestIntegration(t *testing.T) {
	runner, _ := startRunner(t, workflowtest.Registry(t, echo))

	t.Run("conformance", func(t *testing.T) {
		workflowtest.Run(t, func(*testing.T) workflow.Driver {
			return runner.Driver()
		},
			workflowtest.WithoutChild(), workflowtest.WithoutSpawn())
	})
	t.Run("client only", func(t *testing.T) {
		testClientOnly(t)
	})
}

// testClientOnly starts runs from a host without a worker
// and finds them again by id, as a separate web process would.
func testClientOnly(t *testing.T) {
	id := fmt.Sprintf("echo-%d", time.Now().UnixNano())

	client := hatchethost.New(itest.Options(t, func(host *hatchethost.Builder) {
		host.NoWorker(true)
	})...)

	driver, err := hatchetworkflow.NewDriver(client, func(driver *hatchetworkflow.DriverBuilder) {
		driver.Prefix(prefix)
	})

	if err != nil {
		t.Fatalf("NewDriver() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	defer func() {
		_ = client.Stop(context.Background())
	}()

	api := workflow.NewClient(driver)

	run, err := api.Start(ctx, echo, id, "hi")

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if got, err := run.Result(ctx); err != nil || got != "hi!" {
		t.Fatalf("Result() = %q, %v; want hi!", got, err)
	}

	// A fresh driver has no cache and must find the run through the listing.
	other, err := hatchetworkflow.NewDriver(client, func(driver *hatchetworkflow.DriverBuilder) {
		driver.Prefix(prefix)
	})

	if err != nil {
		t.Fatalf("NewDriver() error = %v", err)
	}

	if status, err := workflow.NewClient(other).Run(echo, id).Status(ctx); err != nil || status != workflow.Completed {
		t.Fatalf("Status() = %v, %v; want completed", status, err)
	}

	if _, err := workflow.NewClient(other).Start(ctx, echo, id, "again"); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("second Start() error = %v; want ErrAlreadyStarted after completion", err)
	}

	if _, err := workflow.NewClient(other).Run(echo, "missing").Status(ctx); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status() error = %v; want ErrNotFound", err)
	}

	later, err := api.Start(ctx, echo, id+"-later", "x", workflow.Delay(300*time.Millisecond))

	if err != nil {
		t.Fatalf("Start(Delay) error = %v", err)
	}

	if got, err := later.Result(ctx); err != nil || got != "x!" {
		t.Fatalf("Result(Delay) = %q, %v; want x!", got, err)
	}
}

// TestIntegrationEvictionReplay lets a run wait long enough to leave its
// worker slot and checks that the replay after the signal keeps its steps,
// clock, and identifiers.
func TestIntegrationEvictionReplay(t *testing.T) {
	t.Parallel()

	var stepCalls, bodyCalls atomic.Int32

	step := workflow.NewStep("it-count", func(_ context.Context, in string) (string, error) {
		stepCalls.Add(1)

		return in + "-step", nil
	})
	wake := workflow.NewSignal[string]("wake")
	long := workflow.New("it-long", func(c *workflow.Context, in string) (string, error) {
		bodyCalls.Add(1)

		first := c.UUID()
		started := c.Now()

		out, err := c.Step(step, in)

		if err != nil {
			return "", err
		}

		value, err := c.Wait(wake)

		if err != nil {
			return "", err
		}

		if c.UUID() == first {
			return "", errors.New("uuid repeated")
		}

		return fmt.Sprintf("%s/%s/%v", out, value, !c.Now().Before(started)), nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(5 * time.Minute)
	})

	registry, err := workflow.Handlers(long, step)

	if err != nil {
		t.Fatalf("Handlers() error = %v", err)
	}

	runner, _ := startRunner(t, registry, func(runner *hatchetworkflow.RunnerBuilder) {
		runner.EvictionTTL(2 * time.Second)
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	id := fmt.Sprintf("long-%d", time.Now().UnixNano())
	api := workflow.NewClient(runner.Driver())

	run, err := api.Start(ctx, long, id, "in")

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(6 * time.Second)

	if err := api.Signal(ctx, long, id, wake, "go"); err != nil {
		t.Fatalf("Signal() error = %v", err)
	}

	if got, err := run.Result(ctx); err != nil || got != "in-step/go/true" {
		t.Fatalf("Result() = %q, %v; want in-step/go/true", got, err)
	}

	t.Logf("body ran %d times, step ran %d times", bodyCalls.Load(), stepCalls.Load())

	if stepCalls.Load() != 1 {
		t.Fatalf("step ran %d times; want 1", stepCalls.Load())
	}
}

// TestIntegrationNamespace checks that a signal reaches a run when the client
// has a namespace, which Hatchet applies to pushed events but not to waits.
func TestIntegrationNamespace(t *testing.T) {
	t.Parallel()

	ping := workflow.NewSignal[string]("ping")
	flow := workflow.New("it-ns", func(c *workflow.Context, _ string) (string, error) {
		return c.Wait(ping, workflow.Timeout(20*time.Second))
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatalf("Handlers() error = %v", err)
	}

	runner, _ := startNamespaced(t, registry, "itns")

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	api := workflow.NewClient(runner.Driver())
	id := fmt.Sprintf("ns-%d", time.Now().UnixNano())

	run, err := api.Start(ctx, flow, id, "")

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := api.Signal(ctx, flow, id, ping, "pong"); err != nil {
		t.Fatalf("Signal() error = %v", err)
	}

	if got, err := run.Result(ctx); err != nil || got != "pong" {
		t.Fatalf("Result() = %q, %v; want pong", got, err)
	}
}

// TestIntegrationNamespaceIsolation uses one tenant and identical workflow,
// prefix, and logical IDs under two namespaces. Readiness comes from the actual
// handler, so this tests namespace routing rather than queued-signal timing.
// Engine UUID filters are also checked with a deliberately foreign key.
func TestIntegrationNamespaceIsolation(t *testing.T) {
	t.Parallel()

	started := make(chan [2]string, 8)
	ping := workflow.NewSignal[string]("isolation-ping")
	flow := workflow.New("it-isolation", func(c *workflow.Context, in string) (string, error) {
		started <- [2]string{in, c.Raw().(hatchetsdk.DurableContext).WorkflowRunId()}
		value, err := c.Wait(ping, workflow.Timeout(40*time.Second))

		return in + "/" + value, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatal(err)
	}

	uniquePrefix := fmt.Sprintf("sec010-%d", time.Now().UnixNano())
	options := []func(*hatchetworkflow.RunnerBuilder){func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(uniquePrefix)
	}}
	driverPolicies := []func(*hatchetworkflow.DriverBuilder){func(driver *hatchetworkflow.DriverBuilder) {
		driver.Prefix(uniquePrefix)
	}}

	production, productionHost := startNamespaced(t, registry, "sec010prod", options...)
	staging, stagingHost := startNamespaced(t, registry, "sec010stage", options...)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	productionClient := workflow.NewClient(production.Driver())
	stagingClient := workflow.NewClient(staging.Driver())
	id := "same-logical-id"

	productionRun, err := productionClient.Start(ctx, flow, id, "production")

	if err != nil {
		t.Fatal(err)
	}

	stagingRun, err := stagingClient.Start(ctx, flow, id, "staging")

	if err != nil {
		t.Fatalf("second namespace Start() = %v", err)
	}

	physicalIDs := map[string]string{}

	for range 2 {
		select {
		case run := <-started:
			physicalIDs[run[0]] = run[1]
			t.Logf("%s physical execution: %s", run[0], run[1])
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	if physicalIDs["production"] == "" || physicalIDs["production"] == physicalIDs["staging"] {
		t.Fatal("namespaces shared one physical execution")
	}

	for _, run := range []*workflow.Run[string]{productionRun, stagingRun} {
		if status, err := run.Status(ctx); err != nil || status != workflow.Running {
			t.Fatalf("warm Status() = %v, %v", status, err)
		}
	}

	coldProduction, err := hatchetworkflow.NewDriver(productionHost, driverPolicies...)

	if err != nil {
		t.Fatal(err)
	}

	coldStaging, err := hatchetworkflow.NewDriver(stagingHost, driverPolicies...)

	if err != nil {
		t.Fatal(err)
	}

	for _, driver := range []*hatchetworkflow.Driver{coldProduction, coldStaging} {
		if status, err := driver.Status(ctx, flow.Name(), id); err != nil || status != workflow.Running {
			t.Fatalf("cold Status() = %v, %v", status, err)
		}
	}

	if err := productionClient.Signal(ctx, flow, id, ping, "production-only"); err != nil {
		t.Fatal(err)
	}

	if got, err := productionRun.Result(ctx); err != nil || got != "production/production-only" {
		t.Fatalf("production warm Result() = %q, %v", got, err)
	}

	if status, err := stagingRun.Status(ctx); err != nil || status != workflow.Running {
		t.Fatalf("staging after production signal = %v, %v", status, err)
	}

	if err := stagingClient.Signal(ctx, flow, id, ping, "staging-only"); err != nil {
		t.Fatal(err)
	}

	if got, err := stagingRun.Result(ctx); err != nil || got != "staging/staging-only" {
		t.Fatalf("staging warm Result() = %q, %v", got, err)
	}

	// Recreate both caches to exercise cold Result independently of Status.
	coldProduction, err = hatchetworkflow.NewDriver(productionHost, driverPolicies...)

	if err != nil {
		t.Fatal(err)
	}

	coldStaging, err = hatchetworkflow.NewDriver(stagingHost, driverPolicies...)

	if err != nil {
		t.Fatal(err)
	}

	if got, err := workflow.NewClient(coldProduction).Run(flow, id).Result(ctx); err != nil || got != "production/production-only" {
		t.Fatalf("production cold Result() = %q, %v", got, err)
	}

	if got, err := workflow.NewClient(coldStaging).Run(flow, id).Result(ctx); err != nil || got != "staging/staging-only" {
		t.Fatalf("staging cold Result() = %q, %v", got, err)
	}

	if _, err := productionClient.Start(ctx, flow, id, "duplicate"); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("within-namespace duplicate = %v", err)
	}

	// An explicit wrong workflow UUID must exclude the production key even
	// when the same tenant can see both namespaces through the native API.
	productionDetail, err := productionHost.Client().Runs().Get(ctx, physicalIDs["production"])

	if err != nil {
		t.Fatal(err)
	}

	productionInput, ok := productionDetail.Run.Input["input"].(map[string]any)

	if !ok {
		t.Fatalf("production input wrapper = %v", productionDetail.Run.Input)
	}

	productionKey, ok := productionInput["key"].(string)

	if !ok || productionKey == "" {
		t.Fatalf("production input has no execution key: %v", productionDetail.Run.Input)
	}

	stagingDetail, err := stagingHost.Client().Runs().Get(ctx, physicalIDs["staging"])

	if err != nil {
		t.Fatal(err)
	}

	if stagingDetail.Run.WorkflowId == productionDetail.Run.WorkflowId {
		t.Fatal("namespaces shared one engine workflow UUID")
	}

	keys := []string{productionKey}
	workflowIDs := []uuid.UUID{stagingDetail.Run.WorkflowId}

	rows, err := stagingHost.Client().Runs().List(ctx, rest.V1WorkflowRunListParams{
		Since:           time.Now().Add(-time.Hour),
		WorkflowIds:     &workflowIDs,
		IdempotencyKeys: &keys,
	})

	if err != nil || len(rows.Rows) != 0 {
		t.Fatalf("foreign-key/exact-workflow intersection = %v, %v", rows, err)
	}

	// Warm cancellation must not stop the other namespace. Then a fresh
	// driver cancels that second execution through cold discovery.
	productionCanceled, err := productionClient.Start(ctx, flow, "cancel-id", "production-cancel")

	if err != nil {
		t.Fatal(err)
	}

	stagingCanceled, err := stagingClient.Start(ctx, flow, "cancel-id", "staging-cancel")

	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	if err := productionCanceled.Cancel(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := productionCanceled.Result(ctx); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("production cancel Result() = %v", err)
	}

	if status, err := stagingCanceled.Status(ctx); err != nil || status != workflow.Running {
		t.Fatalf("staging after production cancel = %v, %v", status, err)
	}

	coldStaging, err = hatchetworkflow.NewDriver(stagingHost, driverPolicies...)

	if err != nil {
		t.Fatal(err)
	}

	if err := coldStaging.Cancel(ctx, flow.Name(), "cancel-id"); err != nil {
		t.Fatal(err)
	}

	if _, err := stagingCanceled.Result(ctx); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("cold staging cancel Result() = %v", err)
	}
}

// queuedSignalStart captures the first-worker timeout origin and deterministic
// values alongside engine identity. Comparing actual invocations proves a
// queued signal survives restoration without resetting clocks or physical scope.
type queuedSignalStart struct {
	// physical and invocation identify an execution and its durable restores.
	physical string
	// invocation counts actual worker invocations so restoration is
	// distinguishable from a new execution.
	invocation int32

	// startedAt is the existing arkoren-start memo, read without creating a key.
	startedAt time.Time

	// now and id are application-facing deterministic values replay must retain.
	now time.Time
	// id retains the application UUID that must stay unchanged on durable
	// restoration.
	id string
}

// ingestedSignalEvents waits for the engine's event read API to expose every
// acknowledged send in the exact physical scope, including their SeenAt times.
// The SDK exposes only event writes, so this uses its generated REST request
// with the integration token and decodes responses with encoding/json/v2.
// Polls and requests are bounded; a missing event fails rather than counting
// as a successful queued-delivery test.
func ingestedSignalEvents(t *testing.T, ctx context.Context, tenant uuid.UUID, key, scope string, count int) []rest.V1Event {
	t.Helper()

	api := os.Getenv("ARKOREN_HATCHET_API")

	if api == "" {
		api = "localhost:18888"
	}

	token := itest.Token(t)
	keys, scopes := []rest.EventKey{key}, []string{scope}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	t.Cleanup(ticker.Stop)

	for {
		req, err := rest.NewV1EventListRequest("http://"+api, tenant,
			&rest.V1EventListParams{
				Keys:   &keys,
				Scopes: &scopes,
			})

		if err != nil {
			t.Fatal(err)
		}

		req = req.WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)

		if err != nil {
			t.Fatal(err)
		}

		var events rest.V1EventList
		err = json.UnmarshalRead(resp.Body, &events)
		err = errors.Join(err, resp.Body.Close())

		if resp.StatusCode != http.StatusOK || err != nil {
			t.Fatalf("event ingestion read: status=%s error=%v", resp.Status, err)
		}

		if events.Rows != nil && len(*events.Rows) == count {
			for _, event := range *events.Rows {
				if event.Key != key || event.Scope == nil || *event.Scope != scope || event.SeenAt == nil {
					t.Fatalf("foreign or untimestamped event: %+v", event)
				}
			}

			return *events.Rows
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("accepted signals were not ingested: %v", ctx.Err())
		}
	}
}

// TestIntegrationQueuedSignalSurvivesDelayedWorkerAndRestore holds the only
// durable slot busy until two acknowledged, engine-ingested events are older
// than the former one-minute cutoff. After admission, a confirmed eviction and
// worker replacement precede the first approval wait: only retained history can
// deliver the first value. Queue time exceeds MaxDuration to check that its
// origin remains the first worker invocation rather than workflow submission.
func TestIntegrationQueuedSignalSurvivesDelayedWorkerAndRestore(t *testing.T) {
	t.Parallel()

	options := itest.Options(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	t.Cleanup(cancel)

	started := make(chan queuedSignalStart, 16)
	busy := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once

	blocker := workflow.New("queued-busy", func(c *workflow.Context, _ string) (string, error) {
		busy <- struct{}{}

		select {
		case <-release:
			return "released", nil
		case <-c.Done():
			return "", c.Err()
		}
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(3 * time.Minute)
	})
	approved := workflow.NewSignal[string]("approved")
	gate := workflow.NewSignal[bool]("gate")
	flow := workflow.New("queued-approval", func(c *workflow.Context, _ string) (string, error) {
		raw := c.Raw().(hatchetsdk.DurableContext)
		memo, err := raw.Memo("arkoren-start", func() (any, error) {
			return nil, errors.New("missing existing first-worker memo")
		})

		if err != nil {
			return "", err
		}

		var start time.Time

		if err := json.Unmarshal(memo, &start); err != nil {
			return "", err
		}

		started <- queuedSignalStart{
			physical:   raw.WorkflowRunId(),
			invocation: raw.InvocationCount(),
			startedAt:  start,
			now:        c.Now(),
			id:         c.UUID(),
		}

		if _, err := c.Wait(gate); err != nil {
			return "", err
		}

		first, err := c.Wait(approved, workflow.Timeout(10*time.Second))

		if err != nil {
			return "", err
		}

		again, err := c.Wait(approved)

		if err != nil {
			return "", err
		}

		selected := ""

		if err := c.Select(workflow.On(approved, func(value string) {
			selected = value
		})); err != nil {
			return "", err
		}

		return first + "/" + again + "/" + selected, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(flow, blocker)

	if err != nil {
		t.Fatal(err)
	}

	unique := fmt.Sprintf("sec012-%d", time.Now().UnixNano())
	host := hatchethost.New(append(options, func(host *hatchethost.Builder) {
		host.Namespace(unique)
	},
		func(host *hatchethost.Builder) {
			host.WorkerName(unique)
		}, func(host *hatchethost.Builder) {
			host.Slots(1)
		}, func(host *hatchethost.Builder) {
			host.DurableSlots(1)
		})...)

	_, err = hatchetworkflow.NewRunner(host, registry, func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(unique)
	},
		func(runner *hatchetworkflow.RunnerBuilder) {
			runner.EvictionTTL(time.Second)
		})

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		releaseOnce.Do(func() {
			close(release)
		})
		stop, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		if err := host.Stop(stop); err != nil {
			t.Error(err)
		}
	})

	api := startClientOnly(t, unique, unique)

	blocking, err := api.Start(ctx, blocker, unique+"-busy", "")

	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-busy:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	id := unique + "-queued"
	triggeredAt := time.Now()

	run, err := api.Start(ctx, flow, id, "")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		for _, own := range []*workflow.Run[string]{run, blocking} {
			if err := own.Cancel(cleanup); err != nil {
				t.Error(err)
			}
		}
	})

	encode := base64.RawURLEncoding.EncodeToString
	key := "arkoren.workflow.v2." + encode([]byte(host.Namespace())) + "." + encode([]byte(unique)) +
		"." + encode([]byte(flow.Name())) + "." + encode([]byte(id))
	keys := []string{key}

	rows, err := host.Client().Runs().List(ctx, rest.V1WorkflowRunListParams{
		Since:           triggeredAt.Add(-time.Minute),
		IdempotencyKeys: &keys,
	})

	if err != nil || len(rows.Rows) != 1 {
		t.Fatalf("queued execution listing = %v, %v", rows, err)
	}

	physical := rows.Rows[0].WorkflowRunExternalId.String()

	detail, err := host.Client().Runs().Get(ctx, physical)

	if err != nil || detail.Run.Status != rest.V1TaskStatusQUEUED || detail.Run.StartedAt != nil {
		t.Fatalf("busy capacity did not queue execution: %v, %v", detail, err)
	}

	for _, value := range []string{"first", "second"} {
		if err := api.Signal(ctx, flow, id, approved, value); err != nil {
			t.Fatal(err)
		}
	}

	scope := "arkoren.workflow.signal.v1." + encode([]byte(key)) + "." + encode([]byte(physical))

	events := ingestedSignalEvents(t, ctx, detail.Run.TenantId, host.Namespace()+unique+".sig.approved", scope, 2)
	latestSeenAt := *events[0].SeenAt

	for _, event := range events[1:] {
		if event.SeenAt.After(latestSeenAt) {
			latestSeenAt = *event.SeenAt
		}
	}

	t.Logf("queued physical=%s: two accepted events ingested, latest=%s", physical, latestSeenAt)
	// This is the regression's real age boundary, not a readiness substitute.
	waitSignalBoundary(t, ctx, time.Now().Add(65*time.Second))

	detail, err = host.Client().Runs().Get(ctx, physical)

	if err != nil || detail.Run.Status != rest.V1TaskStatusQUEUED || detail.Run.StartedAt != nil {
		t.Fatalf("execution escaped busy capacity before admission: %v, %v", detail, err)
	}

	select {
	case unexpected := <-started:
		t.Fatalf("handler ran while queued: %+v", unexpected)
	default:
	}

	releaseOnce.Do(func() {
		close(release)
	})

	if got, err := blocking.Result(ctx); err != nil || got != "released" {
		t.Fatalf("busy worker result = %q, %v", got, err)
	}

	var first queuedSignalStart

	select {
	case first = <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if first.physical != physical || !first.startedAt.After(latestSeenAt.Add(time.Minute)) {
		t.Fatalf("old cutoff was not crossed: start=%+v latest event=%s", first, latestSeenAt)
	}

	t.Logf("admitted physical=%s invocation=%d start=%s queued-event-age=%s",
		physical, first.invocation, first.startedAt, first.startedAt.Sub(latestSeenAt))

	ticker := time.NewTicker(250 * time.Millisecond)
	t.Cleanup(ticker.Stop)

	for {
		detail, err := host.Client().Runs().Get(ctx, physical)

		if err != nil {
			t.Fatal(err)
		}

		if len(detail.Tasks) == 1 && detail.Tasks[0].IsEvicted != nil && *detail.Tasks[0].IsEvicted {
			t.Logf("engine confirmed eviction physical=%s invocation=%d", physical, first.invocation)
			break
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("queued execution never evicted: %v", ctx.Err())
		}
	}

	stop, stopCancel := context.WithTimeout(ctx, 20*time.Second)
	t.Cleanup(stopCancel)

	if err := host.Stop(stop); err != nil {
		t.Fatal(err)
	}

	startNamespaced(t, registry, unique, func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(unique)
	},
		func(runner *hatchetworkflow.RunnerBuilder) {
			runner.EvictionTTL(time.Second)
		})

	// Only the separate gate is signaled after restoration. Approval is never
	// resent, so successful delivery must come from the aged queued events.
	if err := api.Signal(ctx, flow, id, gate, true); err != nil {
		t.Fatal(err)
	}

	if got, err := run.Result(ctx); err != nil || got != "first/first/first" {
		t.Fatalf("queued signal after restore = %q, %v", got, err)
	}

	select {
	case replay := <-started:
		if replay.physical != physical || replay.invocation <= first.invocation ||
			!replay.startedAt.Equal(first.startedAt) || !replay.now.Equal(first.now) || replay.id != first.id {
			t.Fatalf("restoration changed identity/clock: first=%+v replay=%+v", first, replay)
		}

		t.Logf("queued first value preserved without resend: physical=%s invocation=%d -> %d",
			physical, first.invocation, replay.invocation)
	default:
		t.Fatal("engine did not reenter queued handler after confirmed eviction")
	}
}

// TestIntegrationImmediateRetryHonorsDerivedZero distinguishes completed
// per-call zero backoff from the registered one-hour delay on a real engine.
// There is no orchestration retry loop: one native step gets three attempts.
func TestIntegrationImmediateRetryHonorsDerivedZero(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	step := workflow.NewStep("it-immediate-policy", func(context.Context, string) (string, error) {
		if calls.Add(1) < 3 {
			return "", errors.New("retry this native step")
		}

		return "retried", nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(time.Hour, 1, time.Hour)
	})

	flow := workflow.New("it-immediate-flow", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step.Derive(func(step *workflow.StepBuilder) {
			step.Backoff(0, 1, 0)
		}), in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(flow, step)

	if err != nil {
		t.Fatal(err)
	}

	runner, _ := startRunner(t, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)

	client := workflow.NewClient(runner.Driver())

	run, err := client.Start(ctx, flow, fmt.Sprintf("immediate-%d", time.Now().UnixNano()), "input")

	if err != nil {
		t.Fatal(err)
	}

	if got, err := run.Result(ctx); err != nil || got != "retried" {
		t.Fatalf("derived zero retry Result = %q, %v; native calls = %d", got, err, calls.Load())
	}

	if calls.Load() != 3 {
		t.Fatalf("native step calls = %d; want exactly 3", calls.Load())
	}
}

// TestIntegrationImmediateRetryStopsAtBudget checks exhausted native retries on
// the disposable engine. An immediate policy must not become an unlimited loop
// or retry the orchestration rather than the affected step.
func TestIntegrationImmediateRetryStopsAtBudget(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	step := workflow.NewStep("it-immediate-exhaust", func(context.Context, string) (string, error) {
		calls.Add(1)

		return "", errors.New("native retry budget exhausted")
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
	})

	flow := workflow.New("it-immediate-exhaust-flow", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step.Derive(func(step *workflow.StepBuilder) {
			step.Backoff(0, 1, 0)
		}), in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(flow, step)

	if err != nil {
		t.Fatal(err)
	}

	runner, _ := startRunner(t, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)

	client := workflow.NewClient(runner.Driver())

	run, err := client.Start(ctx, flow, fmt.Sprintf("exhaust-%d", time.Now().UnixNano()), "input")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := run.Result(ctx); err == nil || !strings.Contains(err.Error(), "native retry budget exhausted") {
		t.Fatalf("exhausted immediate retry Result = %v", err)
	}

	if calls.Load() != 3 {
		t.Fatalf("exhausted native calls = %d; want exactly 3", calls.Load())
	}
}

// TestIntegrationLateStepSuccessIsTimeout checks the final engine-visible error,
// not only the worker's outbound event. A cooperative body returning nil after
// its local timeout must remain a portable timeout through native child joining.
func TestIntegrationLateStepSuccessIsTimeout(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("it-late-success", func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()

		return "too late", nil
	}, func(step *workflow.StepBuilder) {
		step.Timeout(100 * time.Millisecond)
	})

	flow := workflow.New("it-late-flow", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(flow, step)

	if err != nil {
		t.Fatal(err)
	}

	runner, _ := startRunner(t, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)

	client := workflow.NewClient(runner.Driver())

	run, err := client.Start(ctx, flow, fmt.Sprintf("late-%d", time.Now().UnixNano()), "input")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := run.Result(ctx); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("late native success Result = %v; want workflow.ErrTimeout", err)
	}
}

// TestIntegrationWorkflowDeadlineBoundsNativeStepJoin checks parent deadline
// cancellation while the native child is still running. Stop with an expired
// context must then cancel and join the actual cooperative child callback.
func TestIntegrationWorkflowDeadlineBoundsNativeStepJoin(t *testing.T) {
	t.Parallel()

	entered, finished := make(chan struct{}), make(chan struct{})
	step := workflow.NewStep("it-join-blocked", func(ctx context.Context, _ string) (string, error) {
		close(entered)
		<-ctx.Done()
		close(finished)

		return "", ctx.Err()
	})

	flow := workflow.New("it-join-flow", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(3 * time.Second)
	})

	registry, err := workflow.Handlers(flow, step)

	if err != nil {
		t.Fatal(err)
	}

	runner, host := startRunner(t, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)

	client := workflow.NewClient(runner.Driver())

	run, err := client.Start(ctx, flow, fmt.Sprintf("join-%d", time.Now().UnixNano()), "input")

	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native child never started")
	}

	if _, err := run.Result(ctx); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("native join Result = %v; want workflow.ErrTimeout", err)
	}

	stop, release := context.WithCancel(ctx)
	release()

	if err := host.Stop(stop); err != nil {
		t.Fatal(err)
	}

	select {
	case <-finished:
	default:
		t.Fatal("Stop did not join the cooperative child")
	}
}

// physicalSignalStart records the identity observed inside the actual engine
// handler. Physical IDs and invocation counts distinguish new executions from
// durable restores of one execution without relying on elapsed time.
type physicalSignalStart struct {
	// physical is the immutable WorkflowRunId supplied by the engine context.
	physical string

	// invocation increases on durable restore without changing physical identity.
	invocation int32
}

// waitSignalBoundary waits for a real engine TTL or historical-event boundary.
// Fake time cannot expire Postgres-backed Hatchet records. Every wait is bounded
// by the test context and operates only on this test's unique namespace/data.
func waitSignalBoundary(t *testing.T, ctx context.Context, at time.Time) {
	t.Helper()

	timer := time.NewTimer(time.Until(at))
	t.Cleanup(func() {
		timer.Stop()
	})

	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// TestIntegrationPhysicalSignalIsolation reuses two logical IDs after their real
// engine idempotency TTL. One new execution must time out rather than accept an
// old retained event. Another receives fresh false after the former one-minute
// worker lookback, with its old true marker demonstrably kept.
// Repeated typed Wait/Select calls also check same-execution first-value reuse.
func TestIntegrationPhysicalSignalIsolation(t *testing.T) {
	t.Parallel()

	started := make(chan physicalSignalStart, 16)
	approved := workflow.NewSignal[bool]("approved")
	flow := workflow.New("physical-isolation", func(c *workflow.Context, mode string) (string, error) {
		raw := c.Raw().(hatchetsdk.DurableContext)
		started <- physicalSignalStart{
			physical:   raw.WorkflowRunId(),
			invocation: raw.InvocationCount(),
		}
		value, err := c.Wait(approved, workflow.Timeout(4*time.Second))

		if mode == "probe" && errors.Is(err, workflow.ErrTimeout) {
			return "no-signal", nil
		}

		if err != nil {
			return "", err
		}

		again, err := c.Wait(approved)

		if err != nil {
			return "", err
		}

		selected := !value

		if err := c.Select(workflow.On(approved, func(first bool) {
			selected = first
		})); err != nil {
			return "", err
		}

		return fmt.Sprintf("%t/%t/%t", value, again, selected), nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(12 * time.Second)
		definition.Retention(time.Second)
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatal(err)
	}

	unique := fmt.Sprintf("sec011-%d", time.Now().UnixNano())
	runner, host := startNamespaced(t, registry, unique, func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(unique)
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	api := workflow.NewClient(runner.Driver())
	physical := map[string]string{}
	firstTriggeredAt := time.Now()
	firstSignaledAt := time.Time{}

	for _, id := range []string{"historical-event", "retained-marker"} {
		run, err := api.Start(ctx, flow, id, "value")

		if err != nil {
			t.Fatal(err)
		}

		select {
		case execution := <-started:
			physical[id] = execution.physical
			t.Logf("first %s physical=%s invocation=%d", id, execution.physical, execution.invocation)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}

		if err := api.Signal(ctx, flow, id, approved, true); err != nil {
			t.Fatal(err)
		}

		if id == "historical-event" {
			firstSignaledAt = time.Now()
		}

		if got, err := run.Result(ctx); err != nil || got != "true/true/true" {
			t.Fatalf("first %s Result = %q, %v", id, got, err)
		}
	}

	// Workflow TTL is 12s + 1s, measured by the engine from trigger. Starting
	// before and after the boundary proves real reclamation, not fake scoping.
	if _, err := api.Start(ctx, flow, "historical-event", "probe"); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("before real TTL Start = %v", err)
	}

	waitSignalBoundary(t, ctx, firstTriggeredAt.Add(14*time.Second))

	probe, err := api.Start(ctx, flow, "historical-event", "probe")

	if err != nil {
		t.Fatalf("after real TTL Start = %v", err)
	}

	select {
	case execution := <-started:
		if execution.physical == physical["historical-event"] {
			t.Fatal("engine reused the first physical execution")
		}

		if time.Since(firstSignaledAt) >= 55*time.Second {
			t.Fatal("second execution started too late to exercise the historical-event overlap")
		}

		t.Logf("second historical-event physical=%s", execution.physical)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if got, err := probe.Result(ctx); err != nil || got != "no-signal" {
		t.Fatalf("old true event approved second execution: %q, %v", got, err)
	}

	// Both first events were pushed before this point. Waiting 62 seconds
	// crosses the former worker-start-minus-1m cutoff. Both events remain
	// eligible by time; only their physical scopes exclude them now.
	waitSignalBoundary(t, ctx, time.Now().Add(62*time.Second))

	second, err := api.Start(ctx, flow, "retained-marker", "value")

	if err != nil {
		t.Fatal(err)
	}

	var secondID string

	select {
	case execution := <-started:
		secondID = execution.physical

		if secondID == physical["retained-marker"] {
			t.Fatal("engine reused the first physical execution")
		}

		t.Logf("second retained-marker physical=%s", secondID)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if err := api.Signal(ctx, flow, "retained-marker", approved, false); err != nil {
		t.Fatal(err)
	}

	if got, err := second.Result(ctx); err != nil || got != "false/false/false" {
		t.Fatalf("fresh false reused old true marker: %q, %v", got, err)
	}

	// Inspect real stored inputs, not just successful facade results. The old
	// marker remains present and true; the new physical owner has a false marker.
	for _, owner := range []string{physical["retained-marker"], secondID} {
		detail, err := host.Client().Runs().Get(ctx, owner)

		if err != nil {
			t.Fatal(err)
		}

		input, ok := detail.Run.Input["input"].(map[string]any)

		if !ok {
			t.Fatalf("engine input = %v", detail.Run.Input)
		}

		key, ok := input["key"].(string)

		if !ok {
			t.Fatalf("engine logical key = %v", input)
		}

		encode := base64.RawURLEncoding.EncodeToString
		markerKey := "arkoren.workflow.signal.v1." + encode([]byte(key)) + "." + encode([]byte(owner)) + "." + encode([]byte("approved"))
		keys := []string{markerKey}

		rows, err := host.Client().Runs().List(ctx, rest.V1WorkflowRunListParams{
			Since:           firstTriggeredAt.Add(-time.Minute),
			IdempotencyKeys: &keys,
		})

		if err != nil || len(rows.Rows) != 1 {
			t.Fatalf("retained marker for %s = %v, %v", owner, rows, err)
		}

		markerID := rows.Rows[0].WorkflowRunExternalId

		marker, err := host.Client().Runs().GetDetails(ctx, markerID)

		if err != nil {
			t.Fatal(err)
		}

		var stored struct {
			// ExecutionID identifies the physical execution that owns this signal
			// marker.
			ExecutionID string `json:"execution_id"`
			// Version identifies the signal wire protocol stored in the marker input.
			Version int `json:"signal_version"`
			// Data contains the first accepted signal value as an opaque JSON string.
			Data string `json:"data"`
		}

		if err := json.Unmarshal(marker.Input, &stored); err != nil {
			t.Fatal(err)
		}

		want, err := workflow.EncodeValue(nil, owner != secondID)

		if err != nil {
			t.Fatal(err)
		}

		if stored.ExecutionID != owner || stored.Version != 2 || stored.Data != string(want) {
			t.Fatalf("retained marker owner=%s input=%s", owner, marker.Input)
		}

		t.Logf("retained marker=%s owner=%s value=%s", markerID, owner, stored.Data)
	}
}

// TestIntegrationPhysicalSignalConcurrentSenders lets concurrent true/false
// senders contend on one real marker. A second signal keeps the workflow alive
// until both sends complete, so end-of-run races do not masquerade as collisions.
func TestIntegrationPhysicalSignalConcurrentSenders(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 8)
	approved := workflow.NewSignal[bool]("approved")
	finish := workflow.NewSignal[bool]("finish")
	flow := workflow.New("physical-concurrent", func(c *workflow.Context, _ string) (bool, error) {
		started <- struct{}{}
		value, err := c.Wait(approved)

		if err != nil {
			return false, err
		}

		if _, err := c.Wait(finish); err != nil {
			return false, err
		}

		again, err := c.Wait(approved)

		if err != nil || value != again {
			return false, fmt.Errorf("first value changed: %t to %t: %w", value, again, err)
		}

		return value, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatal(err)
	}

	unique := fmt.Sprintf("sec011race-%d", time.Now().UnixNano())
	runner, _ := startNamespaced(t, registry, unique, func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(unique)
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)

	api := workflow.NewClient(runner.Driver())

	run, err := api.Start(ctx, flow, "race", "")

	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	var senders sync.WaitGroup

	for _, value := range []bool{true, false} {
		senders.Go(func() {
			if err := api.Signal(ctx, flow, "race", approved, value); err != nil {
				t.Errorf("concurrent Signal(%t) = %v", value, err)
			}
		})
	}

	senders.Wait()

	if err := api.Signal(ctx, flow, "race", finish, true); err != nil {
		t.Fatal(err)
	}

	if _, err := run.Result(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationPhysicalSignalEvictionRestore waits for confirmed engine
// eviction, then stops its own worker and registers a replacement. The restored
// handler must retain the physical ID and first false value through replay and
// later Wait/Select calls, even after a new true send from the replacement client.
// An invocation count increase is required, not inferred from elapsed time.
func TestIntegrationPhysicalSignalEvictionRestore(t *testing.T) {
	t.Parallel()

	started := make(chan physicalSignalStart, 16)
	resolved := make(chan struct{}, 16)
	approved := workflow.NewSignal[bool]("approved")
	wake := workflow.NewSignal[bool]("wake")
	flow := workflow.New("physical-restore", func(c *workflow.Context, _ string) (bool, error) {
		raw := c.Raw().(hatchetsdk.DurableContext)
		started <- physicalSignalStart{
			physical:   raw.WorkflowRunId(),
			invocation: raw.InvocationCount(),
		}
		first, err := c.Wait(approved)

		if err != nil {
			return false, err
		}

		resolved <- struct{}{}

		if _, err := c.Wait(wake); err != nil {
			return false, err
		}

		again, err := c.Wait(approved)

		if err != nil {
			return false, err
		}

		selected := true

		if err := c.Select(workflow.On(approved, func(value bool) {
			selected = value
		})); err != nil {
			return false, err
		}

		return first || again || selected, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(4 * time.Minute)
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatal(err)
	}

	unique := fmt.Sprintf("sec011restore-%d", time.Now().UnixNano())
	_, host := startNamespaced(t, registry, unique, func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(unique)
	},
		func(runner *hatchetworkflow.RunnerBuilder) {
			runner.EvictionTTL(time.Second)
		})

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)

	api := startClientOnly(t, unique, unique)

	run, err := api.Start(ctx, flow, "restore", "")

	if err != nil {
		t.Fatal(err)
	}

	var first physicalSignalStart

	select {
	case first = <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if err := api.Signal(ctx, flow, "restore", approved, false); err != nil {
		t.Fatal(err)
	}

	select {
	case <-resolved:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	t.Cleanup(ticker.Stop)

	for {
		detail, err := host.Client().Runs().Get(ctx, first.physical)

		if err != nil {
			t.Fatal(err)
		}

		if len(detail.Tasks) == 1 && detail.Tasks[0].IsEvicted != nil && *detail.Tasks[0].IsEvicted {
			t.Logf("engine confirmed eviction physical=%s invocation=%d", first.physical, first.invocation)
			break
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("engine never confirmed durable eviction: %v", ctx.Err())
		}
	}

	stop, stopCancel := context.WithTimeout(ctx, 20*time.Second)
	t.Cleanup(stopCancel)

	if err := host.Stop(stop); err != nil {
		t.Fatal(err)
	}

	startNamespaced(t, registry, unique, func(runner *hatchetworkflow.RunnerBuilder) {
		runner.Prefix(unique)
	},
		func(runner *hatchetworkflow.RunnerBuilder) {
			runner.EvictionTTL(time.Second)
		})

	if err := api.Signal(ctx, flow, "restore", approved, true); err != nil {
		t.Fatal(err)
	}

	if err := api.Signal(ctx, flow, "restore", wake, true); err != nil {
		t.Fatal(err)
	}

	if got, err := run.Result(ctx); err != nil || got {
		t.Fatalf("restored first false = %t, %v", got, err)
	}

	select {
	case replay := <-started:
		if replay.physical != first.physical || replay.invocation <= first.invocation {
			t.Fatalf("restore identity = %+v; initial = %+v", replay, first)
		}

		t.Logf("actual engine restore physical=%s invocation=%d -> %d", replay.physical, first.invocation, replay.invocation)
	default:
		t.Fatal("engine did not reenter the handler after reported eviction")
	}
}

// TestIntegrationDuplicateStartUsesRemainingLookupGrace delays discovery of an
// already visible native run. Establishing that precondition separates the
// adapter's retry window from unbounded native indexing latency. The hiding
// deadline begins before the duplicate command, not after its first response:
// response latency must not move visibility beyond the driver's grace. The
// protocol companion covers first submission and the final backoff window.
func TestIntegrationDuplicateStartUsesRemainingLookupGrace(t *testing.T) {
	// grace includes a tail beyond the first 310ms of exponential backoff.
	const grace = 500 * time.Millisecond
	// visibilityDelay exposes real inventory inside that tail, not beyond grace.
	const visibilityDelay = 400 * time.Millisecond

	if testing.Short() {
		t.Skip("integration test")
	}

	target, err := url.Parse("http://" + cmp.Or(os.Getenv("ARKOREN_HATCHET_API"), "localhost:18888"))

	if err != nil {
		t.Fatal(err)
	}

	var visibleAt atomic.Int64
	var hidden atomic.Int32

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(response *http.Response) error {
		if !strings.HasSuffix(response.Request.URL.Path, "/workflow-runs") {
			return nil
		}

		if response.StatusCode != http.StatusOK {
			return nil
		}

		if time.Now().UnixNano() >= visibleAt.Load() {
			return nil
		}

		hidden.Add(1)

		if err := response.Body.Close(); err != nil {
			return err
		}

		const empty = `{"rows":[]}`
		response.Body = io.NopCloser(strings.NewReader(empty))
		response.ContentLength = int64(len(empty))
		response.Header.Set("Content-Length", strconv.Itoa(len(empty)))
		response.Header.Del("Content-Encoding")

		return nil
	}

	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	t.Setenv("HATCHET_CLIENT_SERVER_URL", server.URL)

	registry, err := workflow.Handlers(echo)

	if err != nil {
		t.Fatal(err)
	}

	runner, host := startRunner(t, registry)

	client := workflow.NewClient(runner.Driver())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)

	id := fmt.Sprintf("visibility-%d", time.Now().UnixNano())
	run, err := client.Start(ctx, echo, id, "visible")

	if err != nil {
		t.Fatalf("establish visible native run: %v", err)
	}

	driver, err := hatchetworkflow.NewDriver(host, func(driver *hatchetworkflow.DriverBuilder) {
		driver.Prefix(prefix)
		driver.LookupGrace(grace)
	})

	if err != nil {
		t.Fatal(err)
	}

	duplicate := workflow.NewClient(driver)
	visibleAt.Store(time.Now().Add(visibilityDelay).UnixNano())

	if _, err := duplicate.Start(ctx, echo, id, "replacement"); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("duplicate Start missed retained native run: %v (hidden listings=%d)", err, hidden.Load())
	}

	if output, err := run.Result(ctx); err != nil || output != "visible!" {
		t.Fatalf("native Result = %q, %v; want visible!", output, err)
	}

	if hidden.Load() == 0 {
		t.Fatal("test did not delay native inventory visibility")
	}
}
