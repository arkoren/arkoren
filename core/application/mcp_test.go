package application

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	appdiagnostic "arkoren.dev/core/application/internal/diagnostic"
	"arkoren.dev/core/event"
	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/internal/mcptest"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// newDiagnosticTools captures tools during synchronous application composition.
// It validates every declaration so inference/schema failures fail immediately.
func newDiagnosticTools(t *testing.T, scope *Composer, configure ...MCPToolsBuilderFunc) []mcp.Tool {
	t.Helper()

	tools, err := scope.MCPTools("app", configure...)

	if err != nil {
		t.Fatal(err)
	}

	for _, tool := range tools {
		if err := tool.Err(); err != nil {
			t.Fatal(err)
		}
	}

	return tools
}

// startDiagnosticApp registers shutdown after successful startup. Cleanup uses
// a fresh context because testing cancellation must not prevent shutdown.
func startDiagnosticApp(t *testing.T, application *Application, composers ...ComposerFunc) {
	t.Helper()

	if err := application.Start(t.Context(), composers...); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := application.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

// TestMCPToolsCallbacksRunOnceOutsideLock checks callback ordering, reentrancy,
// publication isolation, and limits without relying on a protocol adapter.
func TestMCPToolsCallbacksRunOnceOutsideLock(t *testing.T) {
	t.Parallel()

	application := New()
	calls := 0
	var retained *MCPToolsBuilder
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = scope.Must(scope.MCPTools("app", func(tools *MCPToolsBuilder) {
			calls++
			_ = application.State()
			retained = tools

			if tools.Limits(CaptureLimits{Events: -1}) != tools {
				t.Fatal("receiver changed")
			}
		}))
	}
	startDiagnosticApp(t, application, compose)
	retained.Limits(CaptureLimits{Events: 100})

	if calls != 1 || len(tools) != 9 {
		t.Fatalf("calls=%d tools=%d", calls, len(tools))
	}
}

// TestMCPToolsRejectsExpiredComposer checks that expired scopes cannot attach
// diagnostic capture or invoke capture configuration callbacks.
func TestMCPToolsRejectsExpiredComposer(t *testing.T) {
	t.Parallel()

	application := New()
	var retained *Composer
	startDiagnosticApp(t, application, func(scope *Composer) {
		retained = scope
	})

	calls := 0

	_, err := retained.MCPTools("app", func(*MCPToolsBuilder) { calls++; _ = application.State() })

	if !errors.Is(err, ErrInvalidPhase) || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// TestMCPToolsNilCallbackPanics preserves construction misuse behavior while
// composition is active rather than silently omitting a supplied nil callback.
func TestMCPToolsNilCallbackPanics(t *testing.T) {
	t.Parallel()

	application := New()
	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	_ = application.Start(t.Context(), func(scope *Composer) {
		_, _ = scope.MCPTools("app", nil)
	})
}

// TestMCPToolsGeneralValidationFollowsCapture preserves attachment timing;
// declaration errors do not silently roll back an already attached observer.
func TestMCPToolsGeneralValidationFollowsCapture(t *testing.T) {
	t.Parallel()

	application := New()
	startDiagnosticApp(t, application, func(scope *Composer) {
		if _, err := scope.MCPTools("invalid prefix"); !errors.Is(err, mcp.ErrInvalidTool) {
			t.Fatalf("err=%v", err)
		}
	})

	if !application.telemetryConfigured {
		t.Fatal("capture was not attached before declaration validation")
	}
}

// TestMCPToolsPrefixesAndRoles checks discoverable owner metadata without a
// manifest or reserved global command names.
func TestMCPToolsPrefixesAndRoles(t *testing.T) {
	t.Parallel()

	application := New()
	var tools []mcp.Tool
	startDiagnosticApp(t, application, func(scope *Composer) {
		tools = scope.Must(scope.MCPTools("shop"))
	})

	info := mcptest.Find(t, tools, "shop.info").Describe()
	logs := mcptest.Find(t, tools, "shop.logs").Describe()

	if info.CLI.Group != "shop" || info.CLI.Role != "identity" || info.CLI.Operation != "info" {
		t.Fatalf("info=%+v", info.CLI)
	}

	if logs.CLI.Group != "shop" || logs.CLI.Role != "logs" || !logs.CLI.Cursor {
		t.Fatalf("logs=%+v", logs.CLI)
	}
}

// TestMCPToolsUsesOnePrefix checks that all diagnostic tools share the exact
// supplied prefix and remain read-only without duplicate names.
func TestMCPToolsUsesOnePrefix(t *testing.T) {
	t.Parallel()

	application := New()
	var tools []mcp.Tool
	startDiagnosticApp(t, application, func(scope *Composer) {
		tools = scope.Must(scope.MCPTools("app"))
	})

	if !application.Telemetry().Enabled() || len(tools) != 10 {
		t.Fatalf("enabled=%v tools=%d", application.Telemetry().Enabled(), len(tools))
	}

	for _, name := range []string{"app.info", "app.health", "app.started", "app.healthy", "app.ready", "app.events", "app.traces", "app.trace", "app.logs", "app.metrics"} {
		_ = mcptest.Find(t, tools, name)
	}

	names := make(map[string]bool)

	for _, tool := range tools {
		info := tool.Describe()

		if names[info.Name] || info.CLI.Group != "app" || !info.Annotations.ReadOnly {
			t.Fatalf("unexpected tool: %+v", info)
		}

		names[info.Name] = true
	}
}

// TestMCPToolsRejectsEmptyPrefix checks that a missing group is an error before
// capture configuration runs.
func TestMCPToolsRejectsEmptyPrefix(t *testing.T) {
	t.Parallel()

	application := New()
	calls := 0
	startDiagnosticApp(t, application, func(scope *Composer) {
		tools, err := scope.MCPTools("", func(*MCPToolsBuilder) { calls++ })

		if !errors.Is(err, mcp.ErrInvalidTool) || tools != nil || calls != 0 {
			t.Fatalf("tools=%v err=%v calls=%d", tools, err, calls)
		}
	})

	if application.Telemetry().Enabled() || application.telemetryConfigured {
		t.Fatal("invalid prefix enabled telemetry")
	}
}

// TestMCPToolsCallbacksPublishCopiedLimits checks ordered callback
// execution outside the lock and independence from a retained draft.
func TestMCPToolsCallbacksPublishCopiedLimits(t *testing.T) {
	t.Parallel()

	application := New()
	var retained *MCPToolsBuilder
	var tools []mcp.Tool
	calls := 0
	startDiagnosticApp(t, application, func(scope *Composer) {
		tools = scope.Must(scope.MCPTools("app", func(tools *MCPToolsBuilder) {
			calls++
			_ = application.State()
			retained = tools

			if tools.Limits(CaptureLimits{Spans: 1}) != tools {
				t.Fatal("receiver changed")
			}
		}, func(tools *MCPToolsBuilder) {
			calls++
			tools.Limits(CaptureLimits{Spans: 2})
		}))
	})
	retained.Limits(CaptureLimits{Spans: 100})

	for range 3 {
		_, span := application.Telemetry().Tracer("test").Start(t.Context(), "work")
		span.End()
	}

	traces := mcptest.Call[diagnostic.List[map[string]any]](t, mcptest.Find(t, tools, "app.traces"), `{}`)

	if calls != 2 || len(traces.Items) != 2 {
		t.Fatalf("calls=%d traces=%+v", calls, traces)
	}
}

// TestMCPInfoIncludesBuildAndPairedRuntimeSizes preserves the identity data
// formerly attached to the inspection protocol handshake.
func TestMCPInfoIncludesBuildAndPairedRuntimeSizes(t *testing.T) {
	t.Parallel()

	application := New(func(application *Builder) { application.Name("shop").Version("1.2.3").Environment(Staging) })

	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)

	info := mcptest.Call[infoView](t, mcptest.Find(t, tools, "app.info"), `{}`)

	if info.Name != "shop" || info.Version != "1.2.3" || info.Environment != Staging {
		t.Fatalf("info=%+v", info)
	}

	uptime, err := time.ParseDuration(info.Uptime)

	if err != nil || info.UptimeSeconds == nil || int64(uptime/time.Second) != *info.UptimeSeconds {
		t.Fatalf("uptime=%+v, err=%v", info.Info, err)
	}

	if info.Runtime.SysBytes == 0 || info.Runtime.Sys == "" || info.Runtime.Goroutines < 1 {
		t.Fatalf("runtime=%+v", info.Runtime)
	}
}

// TestMCPHealthPendingNeverRunsChecks guards the passive-read boundary.
func TestMCPHealthPendingNeverRunsChecks(t *testing.T) {
	t.Parallel()

	application := New()
	calls := 0
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		scope.Check("database", func(context.Context) error { calls++; return nil })

		tools = newDiagnosticTools(t, scope)
		health := mcptest.Call[appdiagnostic.HealthReport](t, mcptest.Find(t, tools, "app.health"), `{}`)

		if health.Status != diagnostic.StatusPending || calls != 0 {
			t.Fatalf("health=%+v calls=%d", health, calls)
		}
	}
	startDiagnosticApp(t, application, compose)
}

// TestMCPHealthWithoutChecksIsOK avoids pending forever on an empty app.
func TestMCPHealthWithoutChecksIsOK(t *testing.T) {
	t.Parallel()

	application := New()
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)

	health := mcptest.Call[appdiagnostic.HealthReport](t, mcptest.Find(t, tools, "app.health"), `{}`)

	if health.Status != diagnostic.StatusOK || health.Items == nil {
		t.Fatalf("health=%+v", health)
	}
}

// TestMCPHealthFreshRunsChecksAndWithholdsErrors makes fresh checks explicit
// while keeping backend error values out of safe MCP results by default.
func TestMCPHealthFreshRunsChecksAndWithholdsErrors(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		application := New()
		calls := 0
		var tools []mcp.Tool
		startDiagnosticApp(t, application, func(scope *Composer) {
			scope.Check("database", func(context.Context) error {
				calls++

				return errors.New("password=secret")
			})

			tools = newDiagnosticTools(t, scope)
		})
		synctest.Wait()

		before := calls
		health := mcptest.Call[appdiagnostic.HealthReport](t, mcptest.Find(t, tools, "app.health"), `{"fresh":true}`)

		if calls != before+1 || health.Status != diagnostic.StatusFailing || health.Items[0].Error != reasonWithheld {
			t.Fatalf("health=%+v calls=%d", health, calls)
		}
	})
}

// TestMCPHealthExplicitDisclosureHonorsOwnerPolicy checks opt-in diagnostics.
func TestMCPHealthExplicitDisclosureHonorsOwnerPolicy(t *testing.T) {
	t.Parallel()

	application := New(func(application *Builder) { application.HealthDetails(true) })

	var tools []mcp.Tool
	compose := func(scope *Composer) {
		scope.Check("database", func(context.Context) error { return errors.New("approved diagnostic") })

		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)

	health := mcptest.Call[appdiagnostic.HealthReport](t, mcptest.Find(t, tools, "app.health"), `{"fresh":true}`)

	if health.Items[0].Error != "approved diagnostic" {
		t.Fatalf("health=%+v", health)
	}
}

// TestMCPEventsKeepOnlyMetadata checks tracing correlation, payload privacy,
// glob filtering, historical truncation, and negative-limit omission.
func TestMCPEventsKeepOnlyMetadata(t *testing.T) {
	t.Parallel()

	application := New()
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)

	ctx, span := application.Telemetry().Tracer("test").Start(t.Context(), "work")
	topic := event.NewTopic[string]("billing.created")

	application.Events().Emit(ctx, topic, "password=secret")
	span.End()

	page := mcptest.Call[diagnostic.Page[appdiagnostic.EventView]](t, mcptest.Find(t, tools, "app.events"), `{"name":"billing.*"}`)

	if len(page.Items) != 1 || page.Items[0].Name != "billing.created" || page.Items[0].TraceID != span.Context().TraceID.String() {
		t.Fatalf("events=%+v", page)
	}

	encoded, err := json.Marshal(page)

	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("payload leaked: %s", encoded)
	}
}

// TestMCPEventsFilterBounds validates malformed inputs without a protocol layer.
func TestMCPEventsFilterBounds(t *testing.T) {
	t.Parallel()

	application := New()
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)

	tool := mcptest.Find(t, tools, "app.events")

	for _, input := range []string{`{"name":"["}`, `{"limit":-1}`, `{"limit":1001}`, `{"since":"-1s"}`, `{"since":"bad"}`, `{"wait":"26s"}`} {
		if _, err := tool.Call(t.Context(), []byte(input)); !errors.Is(err, mcp.ErrInvalidInput) {
			t.Fatalf("input=%s err=%v", input, err)
		}
	}
}

// TestMCPUnconfiguredTelemetryRemainsOff preserves opt-in capture costs.
func TestMCPUnconfiguredTelemetryRemainsOff(t *testing.T) {
	t.Parallel()

	application := New()
	startDiagnosticApp(t, application)

	if application.Telemetry().Enabled() {
		t.Fatal("ordinary app enabled telemetry")
	}
}

// TestMCPCaptureIncludesStartupAndEarlyLogger preserves observer attachment
// ordering and logger handles obtained before diagnostics are configured.
func TestMCPCaptureIncludesStartupAndEarlyLogger(t *testing.T) {
	t.Parallel()

	application := New()
	logger := application.Logger()
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		scope.Startup(func(ctx context.Context) error {
			ctx, span := application.Telemetry().Tracer("test").Start(ctx, "startup")
			logger.InfoContext(ctx, "early logger")
			application.Telemetry().Meter("test").Counter("startup.count").Add(ctx, 1)
			span.End()

			return nil
		})

		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)

	traces := mcptest.Call[diagnostic.List[map[string]any]](t, mcptest.Find(t, tools, "app.traces"), `{}`)

	if len(traces.Items) != 1 || traces.Items[0]["name"] != "startup" {
		t.Fatalf("traces=%+v", traces)
	}

	logs := mcptest.Call[diagnostic.Page[map[string]any]](t, mcptest.Find(t, tools, "app.logs"), `{}`)

	if len(logs.Items) == 0 {
		t.Fatal("early logger not captured")
	}

	metrics := mcptest.Call[diagnostic.List[map[string]any]](t, mcptest.Find(t, tools, "app.metrics"), `{"name":"startup."}`)

	if len(metrics.Items) != 1 {
		t.Fatalf("metrics=%+v", metrics)
	}
}

// TestMCPCaptureReusesConfiguredStoreWithoutResizing prevents duplicate spans
// and honors capacities supplied by the owner of a memory driver.
func TestMCPCaptureReusesConfiguredStoreWithoutResizing(t *testing.T) {
	t.Parallel()

	store := memorytelemetry.New(func(store *memorytelemetry.Builder) { store.MaxSpans(3) })

	application := New(func(application *Builder) { application.Telemetry(telemetry.New(store)) })

	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope, func(tools *MCPToolsBuilder) { tools.Limits(CaptureLimits{Spans: 1}) })
	}
	startDiagnosticApp(t, application, compose)

	for range 4 {
		_, span := application.Telemetry().Tracer("test").Start(t.Context(), "work")
		span.End()
	}

	traces := mcptest.Call[diagnostic.List[map[string]any]](t, mcptest.Find(t, tools, "app.traces"), `{}`)

	if len(store.Spans()) != 3 || len(traces.Items) != 3 {
		t.Fatalf("spans=%d traces=%+v", len(store.Spans()), traces)
	}
}

// TestMCPCaptureIncludesStartingListener checks local observer-backed capture
// is active for listeners registered before diagnostic capture is attached.
func TestMCPCaptureIncludesStartingListener(t *testing.T) {
	t.Parallel()

	application := New()
	logger := application.Logger()
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		_, err := application.Events().Listen(Starting, func(ctx context.Context, _ event.Void) error {
			logger.InfoContext(ctx, "starting listener")

			return nil
		})

		if err != nil {
			t.Fatal(err)
		}

		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)

	logs := mcptest.Call[diagnostic.Page[map[string]any]](t, mcptest.Find(t, tools, "app.logs"), `{}`)

	if len(logs.Items) != 2 || logs.Items[0]["message"] != "starting listener" || logs.Items[1]["message"] != "app started" {
		t.Fatalf("startup logs=%+v", logs)
	}

	events := mcptest.Call[diagnostic.Page[appdiagnostic.EventView]](t, mcptest.Find(t, tools, "app.events"), `{"name":"app.starting"}`)

	if len(events.Items) != 1 || events.Items[0].Name != Starting.Name() {
		t.Fatalf("starting event not captured: %+v", events)
	}
}

// TestMCPCaptureObservesPrimaryIDs preserves trace and span identity alongside
// an exporting backend that is not itself the memory driver.
func TestMCPCaptureObservesPrimaryIDs(t *testing.T) {
	t.Parallel()

	backend := telemetrytest.New()
	application := New(func(application *Builder) { application.Telemetry(telemetry.New(backend)) })

	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)

	ctx, root := application.Telemetry().Tracer("test").Start(t.Context(), "root")
	_, child := application.Telemetry().Tracer("test").Start(ctx, "child")
	application.Logger().InfoContext(ctx, "inside")
	child.End()
	root.End()

	traces := mcptest.Call[diagnostic.List[map[string]any]](t, mcptest.Find(t, tools, "app.traces"), `{}`)

	if len(backend.Spans()) != 2 || len(traces.Items) != 1 || traces.Items[0]["trace_id"] != root.Context().TraceID.String() {
		t.Fatalf("traces=%+v", traces)
	}

	if traces.Items[0]["span_count"] != float64(2) {
		t.Fatalf("span count=%v", traces.Items[0])
	}
}

// TestMCPCaptureLimitsDisableLogsAndEvents checks explicit negative capacities
// and bounded span retention without changing configured memory drivers.
func TestMCPCaptureLimitsDisableLogsAndEvents(t *testing.T) {
	t.Parallel()

	application := New()
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope, func(tools *MCPToolsBuilder) { tools.Limits(CaptureLimits{Spans: 2, Logs: -1, Events: -1}) })
	}
	startDiagnosticApp(t, application, compose)

	for range 3 {
		_, span := application.Telemetry().Tracer("test").Start(t.Context(), "work")
		span.End()
	}

	application.Logger().InfoContext(t.Context(), "not captured")

	traces := mcptest.Call[diagnostic.List[map[string]any]](t, mcptest.Find(t, tools, "app.traces"), `{}`)
	logs := mcptest.Call[diagnostic.Page[map[string]any]](t, mcptest.Find(t, tools, "app.logs"), `{}`)

	if len(traces.Items) != 2 || len(logs.Items) != 0 || len(tools) != 9 {
		t.Fatalf("traces=%+v logs=%+v tools=%d", traces, logs, len(tools))
	}
}

// TestMCPCaptureConcurrentWrites exercises direct snapshot calls and record
// writes under the race detector without any HTTP or streaming machinery.
func TestMCPCaptureConcurrentWrites(t *testing.T) {
	t.Parallel()

	application := New(func(application *Builder) {
		application.LogHandler(func(level slog.Leveler) slog.Handler { return slog.DiscardHandler })
	})

	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope)
	}
	startDiagnosticApp(t, application, compose)
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 100 {
			_, span := application.Telemetry().Tracer("test").Start(t.Context(), "work")
			application.Logger().InfoContext(t.Context(), "working")
			span.End()
		}
	})

	for _, name := range []string{"app.info", "app.traces", "app.logs", "app.events"} {
		workers.Go(func() {
			tool := mcptest.Find(t, tools, name)

			for range 100 {
				if _, err := tool.Call(t.Context(), []byte(`{}`)); err != nil {
					t.Error(err)
				}
			}
		})
	}

	workers.Wait()
}

// TestStartedToolRetainsStartupAfterStop preserves the historical startup fact
// independently of current readiness and current lifecycle state.
func TestStartedToolRetainsStartupAfterStop(t *testing.T) {
	t.Parallel()

	application := New()
	var tools []mcp.Tool
	var tool mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope)
		tool = mcptest.Find(t, tools, "app.started")
		before := mcptest.Call[appdiagnostic.StartedView](t, tool, `{}`)

		if before.Started || before.State != "starting" || !before.StartedAt.IsZero() {
			t.Fatalf("before=%+v", before)
		}
	}
	startDiagnosticApp(t, application, compose)

	if err := application.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	after := mcptest.Call[appdiagnostic.StartedView](t, tool, `{"wait":true}`)

	if !after.Started || after.State != "stopped" || after.StartedAt.IsZero() {
		t.Fatalf("after=%+v", after)
	}
}

// TestHealthyToolPassivePendingAndNoChecks distinguishes no checks from a
// registered check awaiting its first background pass without running it.
func TestHealthyToolPassivePendingAndNoChecks(t *testing.T) {
	t.Parallel()

	application := New()
	compose := func(scope *Composer) {
		tools := newDiagnosticTools(t, scope)
		tool := mcptest.Find(t, tools, "app.healthy")
		empty := mcptest.Call[appdiagnostic.HealthyView](t, tool, `{}`)

		if empty.Status != diagnostic.StatusOK || empty.Checks == nil {
			t.Fatalf("empty=%+v", empty)
		}

		calls := 0
		scope.Check("database", func(context.Context) error { calls++; return nil })

		pending := mcptest.Call[appdiagnostic.HealthyView](t, tool, `{}`)

		if pending.Status != diagnostic.StatusPending || calls != 0 {
			t.Fatalf("pending=%+v calls=%d", pending, calls)
		}
	}
	startDiagnosticApp(t, application, compose)
}

// TestHealthyToolStalePass preserves freshness semantics after a pass expires.
func TestHealthyToolStalePass(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		app := New(func(app *Builder) { app.ReadinessInterval(time.Second) })

		release := make(chan struct{})
		var calls atomic.Int64
		var tools []mcp.Tool
		startDiagnosticApp(t, app, func(scope *Composer) {
			scope.Check("database", func(context.Context) error {
				if calls.Add(1) > 1 {
					<-release
				}

				return nil
			})

			tools = newDiagnosticTools(t, scope)
		})
		t.Cleanup(func() { close(release) })

		synctest.Wait()
		time.Sleep(4 * time.Second)

		result := mcptest.Call[appdiagnostic.HealthyView](t, mcptest.Find(t, tools, "app.healthy"), `{}`)

		if result.Status != "stale" {
			t.Fatalf("result=%+v", result)
		}
	})
}

// TestReadyToolAgreesWithProbe checks the shared readiness source without
// needing to mount probes or dispatch diagnostic operations through HTTP.
func TestReadyToolAgreesWithProbe(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		application := New()
		var tools []mcp.Tool
		compose := func(scope *Composer) {
			scope.Check("database", func(context.Context) error { return errors.New("down") })

			tools = newDiagnosticTools(t, scope)
			before := mcptest.Call[appdiagnostic.ReadyView](t, mcptest.Find(t, tools, "app.ready"), `{}`)

			if before.Ready || before.Reason != "not live" {
				t.Fatalf("before=%+v", before)
			}
		}
		startDiagnosticApp(t, application, compose)
		synctest.Wait()

		after := mcptest.Call[appdiagnostic.ReadyView](t, mcptest.Find(t, tools, "app.ready"), `{}`)

		if after.Ready || application.ready() || after.Reason != "health checks failed" {
			t.Fatalf("after=%+v", after)
		}
	})
}

// TestStartedWaitObservesStartup uses virtual time to prove a direct call can
// start before the application's startup hooks complete.
func TestStartedWaitObservesStartup(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		application := New()
		var tools []mcp.Tool
		var tool mcp.Tool
		var done chan appdiagnostic.StartedView
		compose := func(scope *Composer) {
			tools = newDiagnosticTools(t, scope)
			tool = mcptest.Find(t, tools, "app.started")
			done = make(chan appdiagnostic.StartedView, 1)

			go func() { done <- mcptest.Call[appdiagnostic.StartedView](t, tool, `{"wait":true}`) }()

			synctest.Wait()

			select {
			case <-done:
				t.Fatal("returned before startup")
			default:
			}
		}
		startDiagnosticApp(t, application, compose)

		if result := <-done; !result.Started || result.State != "running" {
			t.Fatalf("result=%+v", result)
		}
	})
}

// TestReadyWaitObservesRecoveryWithoutExtraChecks verifies that only the
// background runner executes checks while a caller waits passively.
func TestReadyWaitObservesRecoveryWithoutExtraChecks(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		application := New(func(application *Builder) { application.ReadinessInterval(time.Second) })

		var recovered atomic.Bool
		var calls atomic.Int64
		var tools []mcp.Tool
		compose := func(scope *Composer) {
			scope.Check("database", func(context.Context) error {
				calls.Add(1)

				if !recovered.Load() {
					return errors.New("down")
				}

				return nil
			})

			tools = newDiagnosticTools(t, scope)
		}
		startDiagnosticApp(t, application, compose)
		synctest.Wait()

		done := make(chan appdiagnostic.ReadyView, 1)

		go func() {
			done <- mcptest.Call[appdiagnostic.ReadyView](t, mcptest.Find(t, tools, "app.ready"), `{"wait":true,"timeout":"2s"}`)
		}()

		synctest.Wait()
		time.Sleep(500 * time.Millisecond)

		if calls.Load() != 1 {
			t.Fatalf("extra checks=%d", calls.Load())
		}

		recovered.Store(true)

		if result := <-done; !result.Ready || calls.Load() != 2 {
			t.Fatalf("result=%+v calls=%d", result, calls.Load())
		}
	})
}

// TestHealthyWaitObservesFirstPassingPass covers pending-to-healthy startup.
func TestHealthyWaitObservesFirstPassingPass(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		application := New()
		var tools []mcp.Tool
		var done chan appdiagnostic.HealthyView
		compose := func(scope *Composer) {
			scope.Check("database", func(context.Context) error { return nil })

			tools = newDiagnosticTools(t, scope)
			done = make(chan appdiagnostic.HealthyView, 1)

			go func() {
				done <- mcptest.Call[appdiagnostic.HealthyView](t, mcptest.Find(t, tools, "app.healthy"), `{"wait":true}`)
			}()

			synctest.Wait()
		}
		startDiagnosticApp(t, application, compose)

		if result := <-done; result.Status != diagnostic.StatusOK || result.CheckedAt.IsZero() {
			t.Fatalf("result=%+v", result)
		}
	})
}

// TestConditionTimeoutCarriesLastResultAndDefaultBudget checks safe structured
// result metadata and the thirty-second default with no real-time delay.
func TestConditionTimeoutCarriesLastResultAndDefaultBudget(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		application := New()
		compose := func(scope *Composer) {
			tools := newDiagnosticTools(t, scope)
			start := time.Now()

			_, err := mcptest.Find(t, tools, "app.ready").Call(t.Context(), []byte(`{"wait":true}`))
			failure, ok := mcp.ToolFailureInfo(err)

			if !ok || failure.Code != "condition_timeout" || time.Since(start) != 30*time.Second {
				t.Fatalf("failure=%+v err=%v elapsed=%v", failure, err, time.Since(start))
			}

			var details struct {
				Result appdiagnostic.ReadyView `json:"result"`
			}

			if err := json.Unmarshal(failure.Details, &details); err != nil {
				t.Fatal(err)
			}

			if details.Result.Ready || details.Result.Reason != "not live" {
				t.Fatalf("details=%+v", details)
			}
		}
		startDiagnosticApp(t, application, compose)
	})
}

// TestConditionWaitRejectsTerminalLifecycle returns the owner's safe failure,
// not HTTP status metadata, after a lifecycle cannot reach readiness.
func TestConditionWaitRejectsTerminalLifecycle(t *testing.T) {
	t.Parallel()

	application := New()
	var tools []mcp.Tool
	compose := func(scope *Composer) {
		tools = newDiagnosticTools(t, scope)
		scope.Startup(func(context.Context) error { return errors.New("startup secret") })
	}

	if err := application.Start(t.Context(), compose); err == nil {
		t.Fatal("startup succeeded")
	}

	_, err := mcptest.Find(t, tools, "app.started").Call(t.Context(), []byte(`{"wait":true}`))
	failure, ok := mcp.ToolFailureInfo(err)

	if !ok || failure.Code != "condition_stopped" || strings.Contains(string(failure.Details), "secret") {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}
}

// TestConditionWaitCanceled checks error identity rather than treating
// cancellation as an empty successful diagnostic response.
func TestConditionWaitCanceled(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		application := New()
		compose := func(scope *Composer) {
			tools := newDiagnosticTools(t, scope)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)

			go func() { _, err := mcptest.Find(t, tools, "app.ready").Call(ctx, []byte(`{"wait":true}`)); done <- err }()

			synctest.Wait()
			cancel()

			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
		}
		startDiagnosticApp(t, application, compose)
	})
}

// TestConditionInputValidation preserves typed controls and rejects unsupported
// fail-fast on startup before the handler observes application state.
func TestConditionInputValidation(t *testing.T) {
	t.Parallel()

	application := New()
	compose := func(scope *Composer) {
		tools := newDiagnosticTools(t, scope)
		tool := mcptest.Find(t, tools, "app.ready")

		for _, input := range []string{`{"timeout":"-1s"}`, `{"timeout":"bad"}`} {
			if _, err := tool.Call(t.Context(), []byte(input)); !errors.Is(err, mcp.ErrInvalidInput) {
				t.Fatalf("input=%s err=%v", input, err)
			}
		}

		for _, input := range []string{`{"timeout":"1s"}`, `{"fail_fast":true}`} {
			_, err := tool.Call(t.Context(), []byte(input))
			failure, ok := mcp.ToolFailureInfo(err)

			if !ok || failure.Code != "invalid_input" {
				t.Fatalf("input=%s err=%v", input, err)
			}
		}

		if _, err := mcptest.Find(t, tools, "app.started").Call(t.Context(), []byte(`{"fail_fast":true}`)); !errors.Is(err, mcp.ErrInvalidInput) {
			t.Fatalf("err=%v", err)
		}
	}
	startDiagnosticApp(t, application, compose)
}

// TestFailFastCarriesOwnResultAndSafeChecks preserves distinct healthy/ready
// outputs while preventing backend secrets from escaping through safe errors.
func TestFailFastCarriesOwnResultAndSafeChecks(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		application := New()
		var tools []mcp.Tool
		compose := func(scope *Composer) {
			scope.Check("database", func(context.Context) error { return errors.New("password=secret") })

			tools = newDiagnosticTools(t, scope)
		}
		startDiagnosticApp(t, application, compose)
		synctest.Wait()

		_, err := mcptest.Find(t, tools, "app.healthy").Call(t.Context(), []byte(`{"wait":true,"fail_fast":true}`))
		failure, ok := mcp.ToolFailureInfo(err)

		if !ok || failure.Code != "condition_failed" || strings.Contains(string(failure.Details), "secret") {
			t.Fatalf("failure=%+v err=%v", failure, err)
		}

		var details struct {
			Result appdiagnostic.HealthyView       `json:"result"`
			Checks []appdiagnostic.HealthCheckView `json:"checks"`
		}

		if err := json.Unmarshal(failure.Details, &details); err != nil {
			t.Fatal(err)
		}

		if details.Result.Status != diagnostic.StatusFailing || len(details.Checks) != 1 || details.Checks[0].Error != reasonWithheld {
			t.Fatalf("details=%+v", details)
		}
	})
}

// TestFailFastIgnoresStaleAndPendingFailures waits for fresh evidence instead
// of rerunning dependency checks from the diagnostic call.
func TestFailFastIgnoresStaleAndPendingFailures(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		app := New(func(app *Builder) { app.ReadinessInterval(time.Second) })

		release := make(chan struct{})
		var calls atomic.Int64
		var tools []mcp.Tool
		startDiagnosticApp(t, app, func(scope *Composer) {
			scope.Check("database", func(context.Context) error {
				if calls.Add(1) > 1 {
					<-release
				}

				return errors.New("old failure")
			})

			tools = newDiagnosticTools(t, scope)
		})
		t.Cleanup(func() { close(release) })

		synctest.Wait()
		time.Sleep(4 * time.Second)

		tool := mcptest.Find(t, tools, "app.healthy")

		output, err := tool.Call(t.Context(), []byte(`{"wait":true,"fail_fast":true,"timeout":"1s"}`))
		failure, ok := mcp.ToolFailureInfo(err)

		if !ok || failure.Code != "condition_timeout" {
			t.Fatalf("output=%s err=%v", output, err)
		}
	})
}

// histogramPolicy models a backend accepting the application's declared bucket
// policy. Rejected instruments remain absent from the public metric diagnostics.
type histogramPolicy struct {
	// Driver retains accepted measurements and supplies normal lifecycle behavior.
	*memorytelemetry.Driver
}

// Instrument rejects histogram policies other than the declared quarter-second
// and one-second bounds, as a backend with a fixed aggregation policy can do.
func (driver *histogramPolicy) Instrument(spec telemetry.InstrumentSpec) (telemetry.InstrumentRecorder, error) {
	if spec.Kind == telemetry.HistogramKind && !slices.Equal(spec.Buckets, []float64{0.25, 1}) {
		return nil, errors.New("unsupported histogram bounds")
	}

	return driver.Driver.Instrument(spec)
}

// Unwrap lets application diagnostics inspect the backend's existing capture
// instead of attaching a second store with a different instrument policy.
func (driver *histogramPolicy) Unwrap() telemetry.Lifecycle {
	return driver.Driver
}

// TestDiagnosticMetricsPreserveDeclaredHistogramPolicy observes ownership at
// the application MCP seam after the source slice changes before lazy binding.
func TestDiagnosticMetricsPreserveDeclaredHistogramPolicy(t *testing.T) {
	t.Parallel()

	bounds := []float64{0.25, 1}
	instruments := telemetry.NewInstruments(clientop.Build(clientop.Names{
		Scope:    "example.com/billing",
		Noun:     "billing operations",
		Count:    "billing.operations",
		Duration: "billing.duration",
		Buckets:  bounds,
	}))
	bounds[0] = 10
	app := New(func(app *Builder) {
		app.Telemetry(telemetry.New(&histogramPolicy{Driver: memorytelemetry.New()}))
	})

	var tools []mcp.Tool

	if err := app.Start(t.Context(), func(compose *Composer) {
		handle := compose.App().Telemetry()

		tools = compose.Must(compose.MCPTools("app"))
		compose.Startup(func(ctx context.Context) error {
			ctx, operation := clientop.Begin(ctx, handle, instruments, telemetry.Client, "billing", "charge")
			operation.End(ctx, nil)

			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	metrics := mcptest.Call[diagnostic.List[map[string]any]](
		t, mcptest.Find(t, tools, "app.metrics"), `{"name":"billing.duration"}`,
	)

	if len(metrics.Items) != 1 {
		t.Fatalf("declared histogram missing from application diagnostics: %+v", metrics)
	}
}

// TestDiagnosticTraceReportsDeferredOperationFailure exercises the documented
// deferred instrumentation pattern through the public application trace tool.
func TestDiagnosticTraceReportsDeferredOperationFailure(t *testing.T) {
	t.Parallel()

	app := New()
	instruments := telemetry.NewInstruments(clientop.Build(clientop.Names{
		Scope:    "example.com/billing",
		Noun:     "billing operations",
		Count:    "billing.operations",
		Duration: "billing.duration",
	}))
	var tools []mcp.Tool

	if err := app.Start(t.Context(), func(compose *Composer) {
		tools = compose.Must(compose.MCPTools("app"))
	}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	charge := func(ctx context.Context) (err error) {
		ctx, operation := clientop.Begin(ctx, app.Telemetry(), instruments, telemetry.Client, "billing", "charge")
		defer func() {
			operation.End(ctx, err)
		}()

		return context.DeadlineExceeded
	}

	if err := charge(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}

	traces := mcptest.Call[diagnostic.List[map[string]any]](
		t, mcptest.Find(t, tools, "app.traces"), `{"name":"billing charge"}`,
	)

	if len(traces.Items) != 1 {
		t.Fatalf("operation trace missing: %+v", traces)
	}

	if traces.Items[0]["error_count"] != float64(1) {
		t.Fatalf("deferred operation lost its final error: %+v", traces.Items[0])
	}
}
