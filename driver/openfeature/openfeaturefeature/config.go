package openfeaturefeature

import (
	"maps"

	"github.com/open-feature/go-sdk/openfeature"
)

// Builder configures ambient context for one owned provider. New invokes each
// callback directly once in order on one fresh draft; nil entries panic and
// expanded nil or empty slices omit edits. NewFrom has no optional settings.
// Retained drafts cannot change the constructed driver's evaluation context.
//
// Example:
//
//	configure := func(features *openfeaturefeature.Builder) {
//		features.EvaluationContext(openfeature.NewTargetlessEvaluationContext(nil))
//	}
//	driver := openfeaturefeature.New(provider, configure)
//	_ = driver
type Builder struct {
	// context holds copied ambient attributes, not arbitrary cloned values.
	context openfeature.EvaluationContext
}

// BuilderFunc is an ordinary callback alias for owned-provider construction.
// Policies run once in order; nil entries panic and NewFrom accepts no policies.
//
// Example:
//
//	var ambient openfeaturefeature.BuilderFunc = func(features *openfeaturefeature.Builder) {
//		features.EvaluationContext(openfeature.NewTargetlessEvaluationContext(map[string]any{"env": "prod"}))
//	}
//	driver := openfeaturefeature.New(provider, ambient)
//	_ = driver.Client()
type BuilderFunc = func(*Builder)

// configureDriver invokes construction policies and detaches their completed
// context. Attribute values retain their existing ownership and identity.
func configureDriver(configure ...BuilderFunc) openfeature.EvaluationContext {
	features := &Builder{}

	for _, configure := range configure {
		configure(features)
	}

	return cloneContext(features.context)
}

// EvaluationContext replaces ambient context and returns the same draft. The
// outer attribute map is copied now and at publication. The zero context adds
// nothing. Per-evaluation subject attributes and nonempty targeting key take
// precedence over ambient values. Nested application values are not cloned.
//
// Example:
//
//	configure := func(features *openfeaturefeature.Builder) {
//		features.EvaluationContext(openfeature.NewTargetlessEvaluationContext(
//			map[string]any{"env": "prod"},
//		))
//	}
//	_ = configure
func (features *Builder) EvaluationContext(evaluationContext openfeature.EvaluationContext) *Builder {
	features.context = cloneContext(evaluationContext)

	return features
}

// cloneContext copies the owned outer attribute map, preserving targeting key
// and the identity of arbitrary application values stored in the map.
func cloneContext(evaluationContext openfeature.EvaluationContext) openfeature.EvaluationContext {
	return openfeature.NewEvaluationContext(evaluationContext.TargetingKey(), maps.Clone(evaluationContext.Attributes()))
}
