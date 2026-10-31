package telemetry

import "testing"

// TestCompletedSettingsAppliesCallbacksAfterReplacement verifies callback ordering.
func TestCompletedSettingsAppliesCallbacksAfterReplacement(t *testing.T) {
	t.Parallel()

	config := newMiddlewareSettings(
		func(middleware *MiddlewareBuilder) {
			middleware.settings = middlewareSettings{ProblemKey: "a"}
		},
		func(middleware *MiddlewareBuilder) {
			middleware.TraceHeader("X-Trace-ID")
		},
		func(middleware *MiddlewareBuilder) {
			middleware.ProblemKey("b")
		},
	)

	if config.TraceResponse || config.TraceHeader != "X-Trace-ID" || config.ProblemKey != "b" {
		t.Fatalf("config = %+v", config)
	}

	defaults := newMiddlewareSettings()

	if !defaults.TraceResponse || defaults.TraceHeader != "" || defaults.ProblemKey != DefaultProblemKey {
		t.Fatalf("defaults = %+v", defaults)
	}
}
