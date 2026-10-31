// Package feature provides a vendor-neutral, read-only feature flag contract
// and a typed facade.
//
// A [Driver] answers one question: what is the value of a flag key for a
// [Subject]? Flags are created and changed in the vendor's own tools, never
// through this package. [Flags] sits on top of a driver and turns the answer
// into a Go value with [Flags.Value] or, with the variant and [Reason],
// [Flags.ValueDetail]. Four kinds exist: bool, string, int, and float. A
// request for any integer or float type, or a named type such as
// type Plan string, maps onto them.
// Declare a shared immutable [Flag] with [NewFlag] to bind the provider name,
// Go type, and safe fallback. Definitions do not register or validate an
// external provider's schema; driver type checks still run on every answer.
// Raw Driver.Evaluate calls check cancellation first, then reject empty or
// unknown Kind values with ErrTypeMismatch before readiness, flag lookup,
// disabled/default shortcuts, or provider I/O. The only accepted kinds are
// KindBool, KindString, KindInt, and KindFloat. Unknown string representations
// remain unchanged in JSON and String; typed zero definitions still fail with
// ErrInvalidFlag before driver access.
//
//		flags := feature.New(driver, func(flags *feature.Builder) {
//		flags.Telemetry(app.Telemetry())
//	})
//
//	subject := feature.Subject{Key: user.ID, Attributes: map[string]any{"plan": "pro"}}
//	checkout := feature.NewFlag("new-checkout", false)
//	if on, _ := flags.Value(ctx, checkout, subject); on {
//		// serve the new checkout
//	}
//
// # Default guarantee
//
// Every call uses its definition's fallback and the driver never sees it. The
// returned value is the default whenever the error is non-nil, and also,
// with a nil error, when the flag is off or has no value for the subject.
// Ignoring the error is therefore safe: the code takes the old path.
// Failures are [ErrNotFound], [ErrTypeMismatch], [ErrNotReady], and
// [ErrInvalidSubject]. An invalid or zero definition fails with [ErrInvalidFlag]
// before the driver is called. An integer request accepts a float result holding a
// whole number; 3.5, or a value that overflows the requested type, fails with
// [ErrTypeMismatch].
//
// # Subjects
//
// A [Subject] is passed explicitly on every call. Its Key identifies the
// subject stably, such as a user ID, and percentage rollouts hash it. An
// empty Key is an anonymous subject and is valid; the driver then returns the
// flag's default or a value chosen without bucketing. Attributes are matched
// by targeting rules and must be string, bool, int64, float64, []string, or
// time.Time; the facade rejects anything else with [ErrInvalidSubject]
// before calling the driver.
//
// # Events
//
// With [Builder.Events], [Flags] publishes [EvaluationFailed]
// ("feature.evaluation.failed") with the flag key and the error when the
// driver fails an evaluation and the caller gets its default value. Normal
// evaluations publish nothing, and the payload never carries the subject or
// flag values.
//
// # Telemetry
//
// With telemetry enabled, every evaluation emits one internal span named
// "feature evaluate" and two metrics, and costs nothing when telemetry is
// off. The span is internal rather than a client span because the facade
// cannot know whether the driver calls a remote service. It carries
// feature_flag.key, feature_flag.provider.name (the [Builder.Name] value),
// feature_flag.result.reason, feature_flag.result.variant when the driver
// gave one, and error.type with an error status on failure. The counter
// arkoren.feature.evaluations ({evaluation}) is labeled with the key,
// provider, reason, and error.type; the histogram
// arkoren.feature.evaluation.duration (s) with the key, provider, and
// error.type. The reason of a failed evaluation is "error", and error.type
// names the package sentinel, such as feature.ErrNotFound.
//
// Flag keys are recorded because developers choose them and their number is
// bounded, unlike cache keys. Subject keys, subject attributes, flag values,
// and error text are never recorded, because they can identify people.
//
// New takes Builder policies for Telemetry, Name and Events. They run directly
// once in order on a fresh draft; nil entries panic and expanded nil/empty slices
// supply no edits. Publication detaches settings, not the live driver or shared
// services. Retained drafts cannot reconfigure Flags. Nil telemetry resolves the
// process default on each evaluation.
package feature
