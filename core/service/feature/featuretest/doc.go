// Package featuretest provides a conformance suite for [feature.Driver]
// implementations, modeled on testing/slogtest and testing/fstest.
//
// [Run] executes one subtest per documented behavior of the contract: every
// value kind, integers read as floats, subject overrides, anonymous subjects,
// attributes, unknown keys, type mismatches, disabled flags, cancellation,
// updates, deletion and concurrency. A driver that passes is interchangeable
// with every other driver behind [feature.Flags].
// Raw empty and unknown kinds must return feature.ErrTypeMismatch before flag
// lookup or disabled/default shortcuts, with cancellation taking precedence.
// These checks do not retry: kind validation cannot depend on propagation.
// Harnesses supply ready drivers, so adapters separately test invalid kinds
// before readiness and provider I/O.
//
// Feature flags are read-only in the contract, so the suite cannot create
// them through the driver. The caller supplies a [Harness] that installs,
// replaces and removes [Flag] fixtures in the backend, by calling a setter
// (memory), an admin API, a test data source, or by writing a file. Each
// subtest gets its own harness from the factory passed to [Run] and its own
// unique flag keys, so a shared backend works.
//
// Supply newHarness with signature func(*testing.T) featuretest.Harness. Its
// Set method adapts the raw [Flag] fixture to the backend's administration API;
// it is not an application [feature.Flag] declaration. For typed memory setup,
// switch on the fixture's canonical value type and construct a matching
// [feature.Flag] before calling Set or SetFor. The suite intentionally controls
// provider values independently so it can test mismatched evaluation kinds.
//
//	func TestDriver(t *testing.T) {
//		featuretest.Run(t, newHarness)
//	}
//
// Backends that pick up changes asynchronously, such as ones that poll a file
// or an HTTP endpoint, pass [WithPropagation]; fixture-state reads are retried
// until it shows the expected state or the timeout passes. Backends where a
// change after the first evaluation cannot be observed pass
// [WithoutUpdates].
//
// The suite checks the reason only where the contract fixes it: a subject
// override must report [feature.ReasonTargetingMatch] and a disabled flag
// [feature.ReasonDisabled]. A plain value may report [feature.ReasonStatic],
// [feature.ReasonDefault] or [feature.ReasonUnknown]. Variants are never
// checked. Fixture values are bool, string, int64 and float64 so that
// harnesses for remote backends can serialize them.
package featuretest
