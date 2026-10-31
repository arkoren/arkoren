package launchdarklyfeature

import (
	"time"

	ld "github.com/launchdarkly/go-server-sdk/v7"
)

// DefaultStartWait is the initialization wait used when Builder.StartWait is
// zero. Negative waits return immediately and require an already-ready source.
//
// Example:
//
//	configure := func(features *launchdarklyfeature.Builder) {
//		features.StartWait(launchdarklyfeature.DefaultStartWait)
//	}
//	_ = configure
const DefaultStartWait = 5 * time.Second

// Builder configures one owned SDK client. New invokes callbacks directly once
// in order on a fresh draft before validating the key. Nil entries panic;
// expanded nil or empty slices omit edits. Native SDK hooks remain deferred
// until Start. NewFrom has no optional settings and keeps caller ownership.
//
// Example:
//
//	configure := func(features *launchdarklyfeature.Builder) {
//		features.StartWait(time.Second)
//	}
//	driver, err := launchdarklyfeature.New("sdk-key", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// startWait is the initialization bound, with zero meaning the default.
	startWait time.Duration

	// sdk is stored until Start and retains its closure's existing ownership.
	sdk func(*ld.Config)
}

// BuilderFunc is an ordinary function alias for reusable owned-client policies.
// New invokes policies once before validation; the SDK hook stays deferred.
//
// Example:
//
//	var local launchdarklyfeature.BuilderFunc = func(features *launchdarklyfeature.Builder) {
//		features.StartWait(time.Second)
//	}
//	driver, err := launchdarklyfeature.New("sdk-key", local)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// configureDriver invokes construction callbacks directly on a fresh draft and
// returns its completed settings by value, detached from any retained pointer.
func configureDriver(configure ...BuilderFunc) Builder {
	features := &Builder{}

	for _, configure := range configure {
		configure(features)
	}

	return *features
}

// StartWait sets the initialization bound and returns the same draft. Zero uses
// DefaultStartWait; negative values return immediately, failing unless the SDK
// data source is ready. Start closes a client that fails to initialize.
//
// Example:
//
//	configure := func(features *launchdarklyfeature.Builder) {
//		features.StartWait(time.Second)
//	}
//	_ = configure
func (features *Builder) StartWait(wait time.Duration) *Builder {
	features.startWait = wait

	return features
}

// SDK stores a native SDK hook and returns the same draft. Start invokes it on
// the SDK configuration before creating the client; construction does not call
// it. Nil clears the hook. Native objects and captured state are not cloned.
//
// Example:
//
//	configure := func(features *launchdarklyfeature.Builder) {
//		features.SDK(nil)
//	}
//	_ = configure
func (features *Builder) SDK(configure func(*ld.Config)) *Builder {
	features.sdk = configure

	return features
}
