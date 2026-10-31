package application

import (
	"log/slog"
	"testing"
	"time"

	"arkoren.dev/core/http/server"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
)

// TestApplicationSettingsDefaults checks default resolution before lifecycle
// wiring, including the default deployment environment and shutdown budget.
func TestApplicationSettingsDefaults(t *testing.T) {
	config := newSettings()

	if config.Environment != Development {
		t.Fatalf("Environment = %q, want %q", config.Environment, Development)
	}

	if config.ShutdownTimeout != defaultShutdownTimeout {
		t.Fatalf("ShutdownTimeout = %v, want %v", config.ShutdownTimeout, defaultShutdownTimeout)
	}

	if config.Debug || config.LogHandler != nil {
		t.Fatalf("unexpected non-zero config: %+v", config)
	}
}

// TestApplicationSettingsApplyCallbacks verifies completed policy values are
// used for logging, environment, and shutdown without invoking the factory.
func TestApplicationSettingsApplyCallbacks(t *testing.T) {
	build := func(level slog.Leveler) slog.Handler {
		return slog.DiscardHandler
	}

	config := newSettings(func(application *Builder) {
		application.Environment(Production)
		application.Debug(true)
		application.ShutdownTimeout(time.Second)
		application.LogHandler(build)
	})

	if config.Environment != Production || !config.Debug || config.ShutdownTimeout != time.Second || config.LogHandler == nil {
		t.Fatalf("options not applied: %+v", config)
	}
}

// TestConstructionThenOverride checks that a later policy replaces only the
// setting it touches, preserving earlier settings on the same draft.
func TestConstructionThenOverride(t *testing.T) {
	config := newSettings(func(application *Builder) {
		application.Environment(Staging)
		application.Debug(true)
		application.ShutdownTimeout(time.Minute)
	}, func(application *Builder) {
		application.Environment(Production)
	})

	if config.Environment != Production {
		t.Fatalf("Environment = %q, want %q", config.Environment, Production)
	}

	if !config.Debug || config.ShutdownTimeout != time.Minute {
		t.Fatalf("earlier callback settings lost: %+v", config)
	}
}

// TestNewUsesConfig checks that New wires runtime fields from completed
// private settings instead of retaining a writable construction draft.
func TestNewUsesConfig(t *testing.T) {
	application := New(func(application *Builder) {
		application.Environment(Staging)
		application.Debug(true)
	})

	if application.Environment() != Staging || !application.Debug() {
		t.Fatalf("Environment() = %q, Debug() = %v", application.Environment(), application.Debug())
	}
}

// TestApplicationSettingsIdentityAndTelemetry preserves identity defaults and
// pointer sharing for an explicitly configured telemetry dependency.
func TestApplicationSettingsIdentityAndTelemetry(t *testing.T) {
	handle := telemetry.New(nil)
	config := newSettings(func(application *Builder) {
		application.Name("billing")
		application.Version("1.2.3")
		application.Telemetry(handle)
	})

	if config.Name != "billing" || config.Version != "1.2.3" || config.Telemetry != handle {
		t.Fatalf("options not applied: %+v", config)
	}

	if newSettings().Name != "" || newSettings().Version != "" || newSettings().Telemetry != nil {
		t.Fatal("identity and telemetry must default to empty")
	}
}

// TestApplicationSettingsReadinessDefaults checks interval-derived timeout
// defaults and explicit timeout precedence after all callbacks.
func TestApplicationSettingsReadinessDefaults(t *testing.T) {
	config := newSettings()

	if config.ReadinessInterval != 5*time.Second || config.ReadinessTimeout != 2*time.Second {
		t.Fatalf("readiness = %v/%v, want 5s/2s", config.ReadinessInterval, config.ReadinessTimeout)
	}

	config = newSettings(func(application *Builder) {
		application.ReadinessInterval(time.Second)
		application.ReadinessTimeout(time.Minute)
	})

	if config.ReadinessInterval != time.Second || config.ReadinessTimeout != time.Minute {
		t.Fatalf("options not applied: %+v", config)
	}
}

// TestExplicitVersionWins checks explicit identity configuration.
// Build metadata defaults must not overwrite the callback's version.
func TestExplicitVersionWins(t *testing.T) {
	if got := newSettings(func(application *Builder) {
		application.Version("2.0.0")
	}).Version; got != "2.0.0" {
		t.Fatalf("Version = %q, want 2.0.0", got)
	}
}

// TestApplicationSettingsHealthDetails keeps probe detail disclosure opt-in,
// with configuration applied before any readiness response can be served.
func TestApplicationSettingsHealthDetails(t *testing.T) {
	if newSettings().HealthDetails || !newSettings(func(application *Builder) {
		application.HealthDetails(true)
	}).HealthDetails {
		t.Fatal("HealthDetails must default to false and follow its callback setting")
	}
}

// TestApplicationCallbacksShareOneDetachedDraft checks ordered policy edits
// and root publication without copying service pointers or handler captures.
func TestApplicationCallbacksShareOneDetachedDraft(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	handle := telemetry.New(nil)
	application := New(func(application *Builder) {
		calls++
		retained = application
		application.Name("first")
		application.Telemetry(handle)
	}, func(application *Builder) {
		calls++

		if application != retained {
			t.Fatal("callbacks received different drafts")
		}

		application.Name("final")
		application.Debug(true)
	})
	retained.Name("changed").Telemetry(nil)
	retained.Debug(false)

	if calls != 2 || application.name != "final" || !application.Debug() {
		t.Fatalf("calls=%d, application=%+v", calls, application)
	}

	if application.Telemetry() != handle {
		t.Fatal("telemetry dependency was not shared")
	}
}

// TestApplicationIdentitySettingsReturnTheirReceiver checks mutation even
// when callers ignore fluent return values; no copied builder is returned.
func TestApplicationIdentitySettingsReturnTheirReceiver(t *testing.T) {
	t.Parallel()

	application := &Builder{}

	if application.Name("billing") != application || application.Version("1") != application {
		t.Fatal("identity settings returned another receiver")
	}

	if application.Environment(Production) != application || application.Debug(true) != application {
		t.Fatal("logging settings returned another receiver")
	}

	if application.Telemetry(nil) != application || application.LogHandler(nil) != application {
		t.Fatal("dependencies returned another receiver")
	}
}

// TestApplicationReadinessSettingsReturnTheirReceiver covers the remaining
// settings independently of application lifecycle startup.
func TestApplicationReadinessSettingsReturnTheirReceiver(t *testing.T) {
	t.Parallel()

	application := &Builder{}

	if application.ShutdownTimeout(time.Second) != application || application.HealthDetails(true) != application {
		t.Fatal("shutdown/probe settings returned another receiver")
	}

	if application.ReadinessInterval(time.Second) != application || application.ReadinessTimeout(time.Second) != application {
		t.Fatal("readiness settings returned another receiver")
	}
}

// TestApplicationEmptyCallbackExpansionsKeepDefaults distinguishes an empty
// callback list from a supplied nil entry, which is programmer misuse.
func TestApplicationEmptyCallbackExpansionsKeepDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)

	for _, application := range []*Application{New(), New(omitted...), New([]func(*Builder){}...), New(func(application *Builder) {
	})} {
		if application.Environment() != Development || application.shutdownTimeout != defaultShutdownTimeout {
			t.Fatal("empty callbacks changed defaults")
		}
	}
}

// TestApplicationNilCallbackPanics verifies direct invocation rather than
// filtering; the first edit runs and later edits do not run.
func TestApplicationNilCallbackPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil callback did not panic at its position: calls=%d", calls)
		}
	}()
	New(func(application *Builder) {
		calls++
	}, nil, func(application *Builder) {
		calls++
	})
}

// TestApplicationCallbackPanicPropagates preserves the original panic value
// and confirms the application does not proceed to handler construction.
func TestApplicationCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "policy failed" {
			t.Fatalf("panic = %v", got)
		}
	}()
	New(func(application *Builder) {
		application.LogHandler(func(slog.Leveler) slog.Handler {
			t.Fatal("handler factory ran after policy panic")

			return slog.DiscardHandler
		})
		panic("policy failed")
	})
}

// TestApplicationLogHandlerFactoryRunsOnceAfterPolicies keeps runtime handler
// construction separate from policy application and publication.
func TestApplicationLogHandlerFactoryRunsOnceAfterPolicies(t *testing.T) {
	t.Parallel()

	policies, factories := 0, 0
	application := New(func(application *Builder) {
		policies++
		application.LogHandler(func(level slog.Leveler) slog.Handler {
			factories++

			if policies != 2 || level.Level() != slog.LevelDebug {
				t.Fatal("handler factory ran before final policy settings")
			}

			return slog.DiscardHandler
		})
	}, func(application *Builder) {
		policies++
		application.Debug(true)
	})
	application.Logger().InfoContext(t.Context(), "record")

	if policies != 2 || factories != 1 {
		t.Fatalf("policies=%d, factories=%d", policies, factories)
	}
}

func TestBuilderFunctionAliasesPreserveFunctionAndSliceIdentity(t *testing.T) {
	t.Parallel()

	calls := 0
	configure := builderFunctionIdentity[BuilderFunc](func(app *Builder) {
		calls++
		app.Name("alias-test")
	})

	callbacks := builderFunctionIdentity[[]func(*Builder)]([]BuilderFunc{configure})

	newApplication := builderFunctionIdentity[func(...func(*Builder)) *Application](New)
	app := newApplication(callbacks...)

	if app == nil || calls != 1 {
		t.Fatalf("application = %v, callback calls = %d, want non-nil and 1", app, calls)
	}

	serverCallbacks := builderFunctionIdentity[[]server.BuilderFunc]([]func(*server.Builder){})

	toolCallbacks := builderFunctionIdentity[[]MCPToolsBuilderFunc]([]func(*MCPToolsBuilder){})

	readCallbacks := builderFunctionIdentity[[]mcp.ToolBuilderFunc[string, int]]([]func(*mcp.ToolBuilder[string, int]){})

	if len(serverCallbacks)+len(toolCallbacks)+len(readCallbacks) != 0 {
		t.Fatal("empty callback slices must remain empty")
	}
}

// builderFunctionIdentity checks assignment compatibility without conversions.
func builderFunctionIdentity[T any](value T) T {
	return value
}
