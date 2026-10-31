package application

import (
	"context"
	"fmt"
	"regexp"
	"runtime"

	appdiagnostic "arkoren.dev/core/application/internal/diagnostic"
	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// CaptureLimits bounds newly attached application diagnostic capture. Zero
// fields select defaults; negative fields disable that kind of capture.
// Existing configured memory telemetry keeps its own capacities.
//
//	tools := composer.Must(composer.MCPTools("app", func(tools *application.MCPToolsBuilder) {
//		tools.Limits(application.CaptureLimits{Spans: 100, Events: -1})
//	}))
type CaptureLimits struct {
	// Spans bounds finished spans; zero selects 2000.
	// Example: limits.Spans = 100
	Spans int
	// Logs bounds log records; zero selects 5000.
	// Example: limits.Logs = 100
	Logs int
	// Series bounds label combinations per instrument; zero selects 1000.
	// Example: limits.Series = 100
	Series int
	// Events bounds event metadata records; zero selects 500, never payloads.
	// Example: limits.Events = -1
	Events int
}

// defaulted substitutes defaults for zero capacities without changing negative
// values, which explicitly disable capture.
func (limits CaptureLimits) defaulted() CaptureLimits {
	if limits.Spans == 0 {
		limits.Spans = 2000
	}

	if limits.Logs == 0 {
		limits.Logs = 5000
	}

	if limits.Series == 0 {
		limits.Series = 1000
	}

	if limits.Events == 0 {
		limits.Events = 500
	}

	return limits
}

// MCPToolsBuilder configures application-owned capture only. Transport,
// authentication, authorization, and other owners' tools belong in composition.
// The completed limits are copied before capture is attached.
//
//	tools := composer.Must(composer.MCPTools("app", func(tools *application.MCPToolsBuilder) {
//		tools.Limits(application.CaptureLimits{Events: 100})
//	}))
type MCPToolsBuilder struct {
	// limits is copied after construction callbacks have run outside the lock.
	limits CaptureLimits
}

// Limits replaces capture capacities and returns the same construction draft.
// Existing configured memory drivers are reused without resizing them.
//
//	tools := composer.Must(composer.MCPTools("app", func(tools *application.MCPToolsBuilder) {
//		tools.Limits(application.CaptureLimits{Logs: 500})
//	}))
func (tools *MCPToolsBuilder) Limits(limits CaptureLimits) *MCPToolsBuilder {
	tools.limits = limits

	return tools
}

// MCPTools creates SDK-independent application and telemetry tools during synchronous
// composition. Prefix must be nonempty and is used directly: "app" produces
// app.info, app.health, app.started, app.healthy, app.ready, app.events,
// app.traces, app.trace, app.logs, and app.metrics. CLI groups use the same prefix.
// A negative Events limit omits the events tool. Other tools remain available
// even when their record/series retention is disabled. Existing memory stores
// keep their limits.
// An empty prefix returns mcp.ErrInvalidTool before callbacks or capture.
// Each supplied callback runs synchronously once, outside the lifecycle lock;
// nil callbacks and callback panics propagate. Retained drafts cannot alter
// published tools. Calling outside composition returns ErrInvalidPhase.
//
// Capture reuses configured memory telemetry or attaches an observer preserving
// the primary backend's trace IDs. An unconfigured app gains trace IDs and log
// capture before its first startup hook. Events retain names, payload types,
// and trace IDs only. Each call attaches its own capture; call once per app.
// Declaration errors are returned after capture attachment without rollback.
// No transport, authentication, or HTTP dispatch is performed here.
//
// Example:
//
//	tools := composer.Must(composer.MCPTools("app"))
func (composer *Composer) MCPTools(prefix string, configure ...MCPToolsBuilderFunc) ([]mcp.Tool, error) {
	application := composer.application
	allowed := composer.scope.Wiring()

	if !allowed {
		return nil, fmt.Errorf("mcp tools: must be called during composition: %w", ErrInvalidPhase)
	}

	if prefix == "" {
		return nil, fmt.Errorf("mcp tools: prefix must be nonempty: %w", mcp.ErrInvalidTool)
	}

	draft := &MCPToolsBuilder{}

	for _, configure := range configure {
		configure(draft)
	}

	limits := draft.limits.defaulted()
	application.telemetryConfigured = true

	store, err := application.diagnosticStore(limits)

	if err != nil {
		return nil, err
	}

	events, err := application.captureEvents(limits.Events)

	if err != nil {
		return nil, err
	}

	tools := append(application.diagnosticTools(prefix, events), store.MCPTools(prefix)...)

	for _, tool := range tools {
		if err := tool.Err(); err != nil {
			return nil, err
		}
	}

	return tools, nil
}

// diagnosticStore reuses configured memory capture or starts an observer before
// app startup. The observer owns no I/O and follows application telemetry.
func (application *Application) diagnosticStore(limits CaptureLimits) (*memorytelemetry.Driver, error) {
	if store, ok := application.telemetry.DriverAs[*memorytelemetry.Driver](); ok {
		return store, nil
	}

	store := memorytelemetry.New(func(store *memorytelemetry.Builder) {
		store.MaxSpans(max(limits.Spans, 0))
		store.MaxLogs(max(limits.Logs, 0))
		store.MaxSeries(max(limits.Series, 0))
	})

	if err := store.Start(context.Background(), application.setup); err != nil {
		return nil, fmt.Errorf("mcp tools: start telemetry store: %w", err)
	}

	application.telemetry.Observe(store)

	return store, nil
}

// captureEvents subscribes for the application's lifetime. No payload value is
// retained, so a diagnostic event cannot disclose application data.
func (application *Application) captureEvents(limit int) (*appdiagnostic.Ring, error) {
	if limit <= 0 {
		return nil, nil
	}

	events := appdiagnostic.NewRing(limit)

	if _, err := application.events.ListenRegex(regexp.MustCompile(""), events.Record); err != nil {
		return nil, fmt.Errorf("mcp tools: subscribe to events: %w", err)
	}

	return events, nil
}

// infoView pairs application identity/build information with runtime statistics.
type infoView struct {
	// Info is the application identity and lifecycle snapshot, embedded on the wire.
	Info

	// Runtime contains a fresh process runtime snapshot.
	Runtime runtimeView `json:"runtime"`
}

// runtimeView supplies machine values alongside human-readable byte counts.
type runtimeView struct {
	// Goroutines counts current goroutines.
	Goroutines int `json:"goroutines"`
	// HeapAllocBytes is allocated heap bytes.
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	// HeapAlloc formats HeapAllocBytes.
	HeapAlloc string `json:"heap_alloc"`
	// SysBytes is memory obtained from the operating system.
	SysBytes uint64 `json:"sys_bytes"`
	// Sys formats SysBytes.
	Sys string `json:"sys"`
	// NumGC counts completed garbage collections.
	NumGC uint32 `json:"num_gc"`
	// GOMAXPROCS is the parallel thread limit.
	GOMAXPROCS int `json:"gomaxprocs"`
}

// diagnosticInfo takes a fresh runtime observation alongside retained build
// and lifecycle information. Runtime sampling does not mutate app state.
func (application *Application) diagnosticInfo(context.Context, mcp.NoInput) (infoView, error) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)

	return infoView{Info: application.Info(), Runtime: runtimeView{
		Goroutines: runtime.NumGoroutine(), HeapAllocBytes: memory.HeapAlloc,
		HeapAlloc: diagnostic.Bytes(memory.HeapAlloc), SysBytes: memory.Sys,
		Sys: diagnostic.Bytes(memory.Sys), NumGC: memory.NumGC, GOMAXPROCS: runtime.GOMAXPROCS(0),
	}}, nil
}

// diagnosticTools binds explicit application observations to private declarations.
func (application *Application) diagnosticTools(group string, events *appdiagnostic.Ring) []mcp.Tool {
	info := mcp.NewTool(group+".info", application.diagnosticInfo, func(tool *mcp.ToolBuilder[mcp.NoInput, infoView]) {
		tool.CLI(group, "info").Role("identity").ReadOnly(true).Idempotent(true).OpenWorld(false)
		tool.GroupSummary("The application: identity, health, events, traces, logs, and metrics")
		tool.Description("Application identity, build, lifecycle state, and Go runtime")
	})

	owner := &appdiagnostic.Owner{
		State: func() string { return application.State().String() },
		Started: func() appdiagnostic.StartedView {
			state, startedAt := application.execution.Snapshot()

			return appdiagnostic.StartedView{Started: !startedAt.IsZero(), State: state, StartedAt: startedAt}
		},
		Ready: func() appdiagnostic.ReadyView {
			report := application.readyReport()
			view := appdiagnostic.ReadyView{Ready: report.Status == statusOK, Reason: report.Reason}

			if !view.Ready && view.Reason == "" {
				view.Reason = "health checks failed"
			}

			return view
		},
		Health: application.health, Details: application.healthDetails,
	}

	return owner.Tools(group, info, events)
}

// MCPToolsBuilderFunc configures application-owned diagnostic capture before
// publication. Callbacks execute synchronously once outside the lifecycle lock.
//
//	tools := composer.Must(composer.MCPTools("app", func(tools *application.MCPToolsBuilder) {
//		tools.Limits(application.CaptureLimits{Events: 100})
//	}))
type MCPToolsBuilderFunc = func(*MCPToolsBuilder)
