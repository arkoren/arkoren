package prometheustelemetry

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestBuilderCallbacksShareOneDraft verifies policies run once in order and
// publication detaches the root without cloning the caller's registry.
func TestBuilderCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		calls++
		retained = metrics
		metrics.Registry(registry)
		metrics.Namespace("first")
	}, func(metrics *Builder) {
		calls++

		if metrics != retained || metrics.config.Namespace != "first" {
			t.Fatal("callbacks did not share the ordered draft")
		}

		metrics.Namespace("final")
	})

	retained.Namespace("later")
	retained.Registry(prometheus.NewRegistry())

	if calls != 2 || driver.config.Namespace != "final" {
		t.Fatalf("calls = %d, namespace = %q", calls, driver.config.Namespace)
	}

	if driver.config.Registerer != registry || driver.config.Gatherer != registry {
		t.Fatal("native registry was cloned or root was retained")
	}
}

// TestBuilderExplicitOffSurvivesLimitNormalization distinguishes initial
// recommendations from final zero-limit normalization.
func TestBuilderExplicitOffSurvivesLimitNormalization(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
		metrics.TargetInfo(false)
		metrics.MaxSeries(0)
		metrics.MaxInstruments(0)
	})

	if driver.config.RuntimeMetrics || driver.config.TargetInfo {
		t.Fatal("explicit false was overwritten")
	}

	if driver.config.MaxSeries != 1000 || driver.config.MaxInstruments != 1000 {
		t.Fatal("zero limits did not normalize to finite defaults")
	}
}

// TestBuilderEmptyExpansionsPublishFreshDefaults checks nil and empty callback
// slices omit edits and each construction owns an isolated default registry.
func TestBuilderEmptyExpansionsPublishFreshDefaults(t *testing.T) {
	t.Parallel()

	var callbacks []func(*Builder)
	first := New(callbacks...)
	second := New([]func(*Builder){}...)

	if !first.config.RuntimeMetrics || !second.config.TargetInfo {
		t.Fatal("recommended defaults are disabled")
	}

	if first.config.Registerer == second.config.Registerer {
		t.Fatal("default registry was shared")
	}
}

// TestBuilderNilCallbackPanics verifies a nil entry is ordinary callback misuse,
// not an instruction to omit construction settings.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	New(nil)
}

// TestBuilderCallbackPanicPropagates ensures application failures are not
// recovered or confused with registry resolution during publication.
func TestBuilderCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recovered := recover(); recovered != "policy failed" {
			t.Fatalf("panic = %v, want policy failed", recovered)
		}
	}()

	New(func(metrics *Builder) {
		metrics.Namespace("unused")
		panic("policy failed")
	})
}

// TestBuilderRegistryNilPreservesNativeTypedNil keeps the old SDK interface
// contract: Registry(nil) stores a typed nil, not an omitted registerer.
func TestBuilderRegistryNilPreservesNativeTypedNil(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.Registry(nil)
	})

	registry, ok := driver.config.Registerer.(*prometheus.Registry)

	if !ok || registry != nil {
		t.Fatal("typed-nil registry was normalized or replaced")
	}

	gatherer, ok := driver.config.Gatherer.(*prometheus.Registry)

	if !ok || gatherer != nil {
		t.Fatal("typed-nil gatherer was normalized or replaced")
	}
}

// TestBuilderSettingsReturnReceiver checks every native setting edits and
// returns the same draft, including nil SDK handles and negative limits.
func TestBuilderSettingsReturnReceiver(t *testing.T) {
	t.Parallel()

	metrics := &Builder{}
	results := []*Builder{
		metrics.Registry(nil), metrics.Registerer(nil), metrics.Gatherer(nil),
		metrics.Namespace(""), metrics.RuntimeMetrics(false), metrics.TargetInfo(false),
		metrics.MaxSeries(-1), metrics.MaxInstruments(-1),
	}

	for _, result := range results {
		if result != metrics {
			t.Fatal("setting did not return the same receiver")
		}
	}
}
