package llm_test

import (
	"reflect"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestModelConstructionSettingsReturnSameReceiver verifies each role method
// independently, including ignored return values in normal callback use.
func TestModelConstructionSettingsReturnSameReceiver(t *testing.T) {
	t.Parallel()

	llm.New(nil, "", func(model *llm.ModelBuilder) {
		for _, returned := range []*llm.ModelBuilder{
			model.Telemetry(nil), model.Events(nil), model.Provider("openai"),
			model.MaxTokens(3), model.MaxSteps(2),
		} {
			if returned != model {
				t.Fatal("setting replaced model draft")
			}
		}
	})
}

// TestEmbedderConstructionSettingsReturnSameReceiver checks every applicable
// method without exposing a model-only setting on the embedding draft.
func TestEmbedderConstructionSettingsReturnSameReceiver(t *testing.T) {
	t.Parallel()

	llm.NewEmbedder(nil, "", func(embedder *llm.EmbedderBuilder) {
		for _, returned := range []*llm.EmbedderBuilder{
			embedder.Telemetry(nil), embedder.Events(nil), embedder.Provider("openai"),
			embedder.Dimensions(3), embedder.Purpose(llm.PurposeQuery),
		} {
			if returned != embedder {
				t.Fatal("setting replaced embedder draft")
			}
		}
	})
}

// TestModelConstructionKeepsDynamicDefaultTelemetry changes the process
// default after construction and again after the first instrumented operation.
func TestModelConstructionKeepsDynamicDefaultTelemetry(t *testing.T) {
	previous := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(previous)
	})

	model := llm.New(newStub(), "gpt-5.2", func(model *llm.ModelBuilder) {
		model.Telemetry(nil)
	})

	first := telemetrytest.New()
	telemetry.SetDefault(first.Telemetry(t))

	if _, err := model.Generate(t.Context(), userRequest("first")); err != nil {
		t.Fatal(err)
	}

	second := telemetrytest.New()
	telemetry.SetDefault(second.Telemetry(t))

	if _, err := model.Generate(t.Context(), userRequest("second")); err != nil {
		t.Fatal(err)
	}

	first.AssertSpan(t, "chat gpt-5.2")
	second.AssertSpan(t, "chat gpt-5.2")
}

// TestEmbedderConstructionKeepsDynamicDefaultTelemetry verifies that changing
// the default after construction and between calls changes the recording store.
func TestEmbedderConstructionKeepsDynamicDefaultTelemetry(t *testing.T) {
	previous := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(previous)
	})

	driver := newStub()
	driver.vectors = [][]float32{{1, 2}}
	embedder := llm.NewEmbedder(driver, "embed-1", func(embedder *llm.EmbedderBuilder) {
		embedder.Telemetry(nil)
	})

	first := telemetrytest.New()
	telemetry.SetDefault(first.Telemetry(t))

	if _, err := embedder.Embed(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}

	second := telemetrytest.New()
	telemetry.SetDefault(second.Telemetry(t))

	if _, err := embedder.Embed(t.Context(), "second"); err != nil {
		t.Fatal(err)
	}

	first.AssertSpan(t, "embeddings embed-1")
	second.AssertSpan(t, "embeddings embed-1")
}

// TestModelConstructionPublishesOrderedSnapshot checks callback cardinality,
// receiver identity, role settings and independent Config reporting records.
func TestModelConstructionPublishesOrderedSnapshot(t *testing.T) {
	t.Parallel()

	driver := newStub()
	bus := event.New()
	service := telemetry.New(nil)
	var retained *llm.ModelBuilder
	calls := 0
	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
		retained = model
		calls++

		if model.MaxTokens(12).MaxSteps(2) != model {
			t.Fatal("settings changed receiver")
		}

		model.Telemetry(service)
		model.Events(bus)
		model.Provider("first")
	}, func(model *llm.ModelBuilder) {
		calls++

		if model != retained {
			t.Fatal("callbacks received different drafts")
		}

		model.MaxTokens(24)
		model.Provider("final")
	})
	retained.MaxTokens(99)
	retained.Events(nil)

	config := model.Config()

	if calls != 2 {
		t.Fatalf("calls=%d config=%+v", calls, config)
	}

	if config != (llm.Config{
		Telemetry: service,
		Events:    bus,
		Provider:  "final",
		MaxTokens: 24,
		MaxSteps:  2,
	}) {
		t.Fatalf("completed settings changed: %+v", config)
	}

	config.MaxTokens = 77
	config.Provider = "mutated"

	if model.Config().MaxTokens != 24 || model.Config().Provider != "final" {
		t.Fatal("Config exposed mutable root")
	}

	if model.Driver() != driver {
		t.Fatal("model changed driver")
	}
}

// TestEmbedderConstructionPublishesOrderedSnapshot checks that embedder policy
// applies once without exposing generation-only settings or retained root state.
func TestEmbedderConstructionPublishesOrderedSnapshot(t *testing.T) {
	t.Parallel()

	bus := event.New()
	service := telemetry.New(nil)
	var retained *llm.EmbedderBuilder
	calls := 0
	embedder := llm.NewEmbedder(newStub(), "embed-1", func(embedder *llm.EmbedderBuilder) {
		retained = embedder
		calls++

		if embedder.Dimensions(2).Purpose(llm.PurposeDocument) != embedder {
			t.Fatal("settings changed receiver")
		}

		embedder.Telemetry(service)
		embedder.Events(bus)
		embedder.Provider("first")
	}, func(embedder *llm.EmbedderBuilder) {
		calls++

		if embedder != retained {
			t.Fatal("callbacks received different drafts")
		}

		embedder.Dimensions(3)
		embedder.Purpose(llm.PurposeQuery)
		embedder.Provider("final")
	})
	retained.Dimensions(99)
	retained.Events(nil)

	config := embedder.Config()

	if calls != 2 {
		t.Fatalf("calls=%d config=%+v", calls, config)
	}

	if config != (llm.Config{
		Telemetry:  service,
		Events:     bus,
		Provider:   "final",
		Dimensions: 3,
		Purpose:    llm.PurposeQuery,
	}) {
		t.Fatalf("completed settings changed: %+v", config)
	}

	config.Dimensions = 77
	config.Provider = "mutated"

	if embedder.Config().Dimensions != 3 || embedder.Config().Provider != "final" {
		t.Fatal("Config exposed mutable root")
	}
}

// TestLLMConstructionRolesHaveOnlyApplicableMethods prevents an ignored-role
// setting from reappearing on either construction surface.
func TestLLMConstructionRolesHaveOnlyApplicableMethods(t *testing.T) {
	t.Parallel()

	model := reflect.TypeFor[*llm.ModelBuilder]()

	for _, name := range []string{"Telemetry", "Events", "Provider", "MaxTokens", "MaxSteps"} {
		if _, ok := model.MethodByName(name); !ok {
			t.Fatalf("ModelBuilder lacks %s", name)
		}
	}

	embedder := reflect.TypeFor[*llm.EmbedderBuilder]()

	for _, name := range []string{"Telemetry", "Events", "Provider", "Dimensions", "Purpose"} {
		if _, ok := embedder.MethodByName(name); !ok {
			t.Fatalf("EmbedderBuilder lacks %s", name)
		}
	}

	if model.NumMethod() != 5 || embedder.NumMethod() != 5 {
		t.Fatal("construction role exposes extra settings")
	}
}

// TestModelConstructionEmptyPoliciesPreserveDefaults covers omitted edits,
// explicit empty callbacks, slice expansion and fresh drafts across constructors.
func TestModelConstructionEmptyPoliciesPreserveDefaults(t *testing.T) {
	t.Parallel()

	var policies []func(*llm.ModelBuilder)

	if llm.New(nil, "", policies...).Config() != llm.New(nil, "").Config() {
		t.Fatal("nil expansion changed defaults")
	}

	if llm.New(nil, "", []func(*llm.ModelBuilder){}...).Config() != (llm.Config{}) {
		t.Fatal("empty expansion changed defaults")
	}

	var first *llm.ModelBuilder
	llm.New(nil, "", func(model *llm.ModelBuilder) {
		first = model
	})
	llm.New(nil, "", func(model *llm.ModelBuilder) {
		if model == first {
			t.Fatal("construction reused a draft")
		}
	})
}

// TestEmbedderConstructionEmptyPoliciesPreserveDefaults also preserves lazy
// required-input validation: no constructor invokes the absent driver.
func TestEmbedderConstructionEmptyPoliciesPreserveDefaults(t *testing.T) {
	t.Parallel()

	var policies []func(*llm.EmbedderBuilder)

	if llm.NewEmbedder(nil, "", policies...).Config() != (llm.Config{Purpose: llm.PurposeUnspecified}) {
		t.Fatal("nil expansion changed defaults")
	}

	if llm.NewEmbedder(nil, "", []func(*llm.EmbedderBuilder){}...).Config() != (llm.Config{Purpose: llm.PurposeUnspecified}) {
		t.Fatal("empty expansion changed defaults")
	}

	var first *llm.EmbedderBuilder
	llm.NewEmbedder(nil, "", func(embedder *llm.EmbedderBuilder) {
		first = embedder
	})
	llm.NewEmbedder(nil, "", func(embedder *llm.EmbedderBuilder) {
		if embedder == first {
			t.Fatal("construction reused a draft")
		}
	})
}

// TestModelConstructionNilPolicyPanics proves nil entries are not filtered,
// even with invalid required inputs; later callbacks must not execute.
func TestModelConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil policy did not interrupt in order: calls=%d", calls)
		}
	}()
	llm.New(nil, "", func(model *llm.ModelBuilder) {
		calls++
	}, nil, func(model *llm.ModelBuilder) {
		calls++
	})
}

// TestEmbedderConstructionNilPolicyPanics preserves the same direct-invocation
// rule without confusing nil drivers with construction callback misuse.
func TestEmbedderConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil policy did not interrupt in order: calls=%d", calls)
		}
	}()
	llm.NewEmbedder(nil, "", func(embedder *llm.EmbedderBuilder) {
		calls++
	}, nil, func(embedder *llm.EmbedderBuilder) {
		calls++
	})
}

// TestModelConstructionApplicationPanicPropagates preserves the exact panic
// value instead of translating callback failures into request validation.
func TestModelConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	failure := "construction policy"
	defer func() {
		if recover() != failure {
			t.Fatal("application panic was replaced")
		}
	}()
	llm.New(nil, "", func(model *llm.ModelBuilder) {
		panic(failure)
	})
}

// TestEmbedderConstructionApplicationPanicPropagates preserves application
// panic identity without treating it as a domain validation failure.
func TestEmbedderConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	failure := "construction policy"
	defer func() {
		if recover() != failure {
			t.Fatal("application panic was replaced")
		}
	}()
	llm.NewEmbedder(nil, "", func(embedder *llm.EmbedderBuilder) {
		panic(failure)
	})
}
