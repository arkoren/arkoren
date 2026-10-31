package search

import (
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// DefaultName labels operations when Builder.Name is empty. It distinguishes
// the ordinary unnamed engine from explicitly named engines.
//
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Name(search.DefaultName)
//	})
const DefaultName = "default"

// settings holds completed construction state. Index registration storage is
// owned; telemetry and index implementations remain shared dependencies.
type settings struct {
	// Events is the shared operational bus; nil disables notifications.
	Events *event.Bus
	// Indexes accumulates registrations in callback order.
	Indexes []Indexer
	// Telemetry is resolved dynamically per operation when nil.
	Telemetry *telemetry.Telemetry
	// Name is the stable telemetry label; empty reports DefaultName.
	Name string
}

// Events selects best-effort in-process Sync and Drop notifications and returns
// the same draft. Nil disables them. Events do not establish index consistency
// and are not a durable audit log; query and document operations emit none.
//
// Example:
//
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Events(event.New())
//	})
//	_ = engine
func (engine *Builder) Events(events *event.Bus) *Builder {
	engine.config.Events = events

	return engine
}

// Builder edits an engine's construction draft. New snapshots registrations
// before publication; retaining this draft cannot change the engine.
//
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
type Builder struct {
	// config owns registration storage until publication.
	config settings
}

// Indexes appends registrations in order, copying the input slice. Nil or empty
// lists add nothing. Nil entries and duplicate names retain normal use-time
// errors. Index implementations are shared, not recursively cloned.
//
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products, Users)
//	})
func (engine *Builder) Indexes(values ...Indexer) *Builder {
	engine.config.Indexes = append(engine.config.Indexes, values...)

	return engine
}

// Telemetry selects the shared instrumentation service. Nil follows
// telemetry.Default per operation. It returns the same draft.
//
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Telemetry(app.Telemetry())
//	})
func (engine *Builder) Telemetry(value *telemetry.Telemetry) *Builder {
	engine.config.Telemetry = value

	return engine
}

// Name replaces the engine's stable telemetry label. Empty reports DefaultName;
// it does not rename indexes. It returns the same draft.
//
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Name("catalog")
//	})
func (engine *Builder) Name(value string) *Builder {
	engine.config.Name = value

	return engine
}

// BuilderFunc edits one engine draft synchronously. New invokes callbacks once
// in order before validating registrations; nil entries panic as caller misuse.
//
// Example:
//
//	var configure search.BuilderFunc = func(engine *search.Builder) {
//		engine.Indexes(Products)
//	}
//	engine := search.New(driver, configure)
//	_ = engine.Driver()
type BuilderFunc = func(*Builder)
