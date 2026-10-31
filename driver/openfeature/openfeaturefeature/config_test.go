package openfeaturefeature_test

import (
	"context"
	"testing"

	"github.com/open-feature/go-sdk/openfeature"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/driver/openfeature/openfeaturefeature"
)

// TestBuilderDetachesAmbientContext verifies input capture and publication copy
// the outer attribute map while direct callbacks share one ordered draft.
func TestBuilderDetachesAmbientContext(t *testing.T) {
	t.Parallel()

	attributes := map[string]any{"env": "prod"}
	ambient := openfeature.NewEvaluationContext("service", attributes)
	var retained *openfeaturefeature.Builder
	calls := 0
	provider := &stub{}
	driver := openfeaturefeature.New(provider, func(features *openfeaturefeature.Builder) {
		retained = features
		calls++

		if features.EvaluationContext(ambient) != features {
			t.Fatal("method returned another draft")
		}

		attributes["env"] = "input-mutated"
	}, func(features *openfeaturefeature.Builder) {
		calls++

		if features != retained || calls != 2 {
			t.Fatal("callbacks were replayed or reordered")
		}
	})

	retained.EvaluationContext(openfeature.NewTargetlessEvaluationContext(map[string]any{"env": "later"}))

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	_, err := driver.Evaluate(t.Context(), feature.Request{
		Key:  "flag",
		Kind: feature.KindBool,
	})

	if err != nil {
		t.Fatal(err)
	}

	if got := *provider.last.Load(); got["env"] != "prod" {
		t.Fatalf("ambient context = %v", got)
	}
}

// TestBuilderNilCallbackPanics verifies supplied nil remains a direct-call
// panic even though provider setup itself is deferred to Start.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	openfeaturefeature.New(&stub{}, nil)
}

// TestBuilderEmptySlicesOmitEdits checks expanded nil and empty callback slices
// preserve construction without invoking policies or initializing providers.
func TestBuilderEmptySlicesOmitEdits(t *testing.T) {
	t.Parallel()

	var configure []func(*openfeaturefeature.Builder)

	if openfeaturefeature.New(&stub{}, configure...) == nil {
		t.Fatal("nil expansion failed")
	}

	if openfeaturefeature.New(&stub{}, []func(*openfeaturefeature.Builder){}...) == nil {
		t.Fatal("empty expansion failed")
	}
}

// TestBuilderApplicationPanicPropagates checks native provider construction does
// not swallow or replace a panic from a supplied policy.
func TestBuilderApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "application panic" {
			t.Fatalf("panic = %v", got)
		}
	}()

	openfeaturefeature.New(&stub{}, func(features *openfeaturefeature.Builder) {
		panic("application panic")
	})
}
