package feature

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

var (
	// ErrNotFound indicates that no flag with the requested key exists.
	// Typed evaluation still returns the definition's fallback with this error.
	//
	// Example: missing := errors.Is(err, feature.ErrNotFound)
	ErrNotFound = errors.New("feature flag not found")

	// ErrTypeMismatch indicates that the flag's value cannot be represented
	// as the requested kind or Go type.
	//
	// Example: incompatible := errors.Is(err, feature.ErrTypeMismatch)
	ErrTypeMismatch = errors.New("feature flag type mismatch")

	// ErrNotReady indicates that the driver has not loaded its flags yet,
	// for example before its first sync completes.
	//
	// Example: loading := errors.Is(err, feature.ErrNotReady)
	ErrNotReady = errors.New("feature flag driver not ready")

	// ErrInvalidSubject indicates that the subject cannot be evaluated, such
	// as an attribute of an unsupported type.
	//
	// Example: invalid := errors.Is(err, feature.ErrInvalidSubject)
	ErrInvalidSubject = errors.New("feature flag invalid subject")
)

// Kind is the value type a caller requests from a [Driver]. The facade
// derives it from the Go type parameter of [Flags.Value].
// Only KindBool, KindString, KindInt, and KindFloat are valid for evaluation.
// Empty and unknown values fail with ErrTypeMismatch at driver boundaries;
// their JSON and String representations still preserve the supplied bytes.
//
// Example: _ = feature.Request{Key: "checkout", Kind: feature.KindBool}
type Kind string

const (
	// KindBool requests a bool; [Result.Value] holds a bool.
	//
	// Example: _ = feature.Request{Key: "checkout", Kind: feature.KindBool}
	KindBool Kind = "bool"

	// KindString requests a string; [Result.Value] holds a string.
	//
	// Example: _ = feature.Request{Key: "plan", Kind: feature.KindString}
	KindString Kind = "string"

	// KindInt requests a whole number; [Result.Value] holds an int64, or a
	// float64 holding a whole number.
	//
	// Example: _ = feature.Request{Key: "seats", Kind: feature.KindInt}
	KindInt Kind = "int"

	// KindFloat requests a floating-point number; [Result.Value] holds a
	// float64.
	//
	// Example: _ = feature.Request{Key: "ratio", Kind: feature.KindFloat}
	KindFloat Kind = "float"
)

// String returns the requested value type unchanged, including empty or
// unknown values. The zero Kind remains invalid at driver boundaries.
//
// Example: _ = feature.KindBool.String() // "bool"
func (kind Kind) String() string {
	return string(kind)
}

// Reason explains how a driver chose a result. The values match OpenFeature's
// resolution reasons so that drivers can pass them through.
// Empty input means unknown; the facade normalizes it to ReasonUnknown.
//
// Example: _ = feature.Result{Value: true, Reason: feature.ReasonStatic}
type Reason string

const (
	// ReasonUnknown means the driver gave no reason. Empty input has the same
	// meaning and is normalized by the facade.
	//
	// Example: _ = feature.Result{Reason: feature.ReasonUnknown}
	ReasonUnknown Reason = "unknown"

	// ReasonStatic means the flag has one value for everyone.
	//
	// Example: _ = feature.Result{Value: true, Reason: feature.ReasonStatic}
	ReasonStatic Reason = "static"

	// ReasonDefault means no rule matched, so the result is the flag's
	// default or, with a nil value, the caller's.
	//
	// Example: _ = feature.Result{Reason: feature.ReasonDefault}
	ReasonDefault Reason = "default"

	// ReasonTargetingMatch means a targeting rule or subject override
	// matched.
	//
	// Example: _ = feature.Result{Value: true, Reason: feature.ReasonTargetingMatch}
	ReasonTargetingMatch Reason = "targeting_match"

	// ReasonSplit means a percentage rollout chose the value.
	//
	// Example: _ = feature.Result{Value: true, Reason: feature.ReasonSplit}
	ReasonSplit Reason = "split"

	// ReasonCached means the value was served from a cache and may be stale.
	//
	// Example: _ = feature.Result{Value: true, Reason: feature.ReasonCached}
	ReasonCached Reason = "cached"

	// ReasonDisabled means the flag exists but is off.
	//
	// Example: _ = feature.Result{Value: false, Reason: feature.ReasonDisabled}
	ReasonDisabled Reason = "disabled"

	// ReasonError means evaluation failed. The facade reports it in
	// [Detail.Reason] together with the error.
	//
	// Example: _ = feature.Result{Reason: feature.ReasonError}
	ReasonError Reason = "error"
)

// String returns the reason unchanged, with empty input reported as
// "unknown". Unknown nonempty provider reasons are preserved rather than lost.
//
// Example: _ = feature.ReasonTargetingMatch.String() // "targeting_match"
func (reason Reason) String() string {
	if reason == "" {
		return "unknown"
	}

	return string(reason)
}

// Subject is the entity a flag is evaluated for, usually a user.
//
// The zero Subject is anonymous and valid.
//
//	subject := feature.Subject{
//		Key:        user.ID,
//		Attributes: map[string]any{"plan": "pro", "seats": int64(5)},
//	}
type Subject struct {
	// Key identifies the subject stably, such as a user ID. Percentage
	// rollouts hash it. Empty means anonymous.
	//
	// Example: subject := feature.Subject{Key: "user-42"}
	Key string

	// Attributes are matched by targeting rules. Values must be string, bool,
	// int64, float64, []string, or time.Time; the facade rejects anything
	// else with [ErrInvalidSubject] before calling the driver. Drivers must
	// not modify the map.
	//
	// Example: subject := feature.Subject{Attributes: map[string]any{"plan": "pro"}}
	Attributes map[string]any
}

// Request is one evaluation handed to a [Driver].
// The typed facade derives Kind and validates the subject before handing it to
// the adapter. Direct callers supply those values themselves.
//
// Example:
//
//	result, err := driver.Evaluate(ctx, feature.Request{Key: "checkout", Kind: feature.KindBool})
//	if err != nil {
//		return err
//	}
//	_ = result.Value
type Request struct {
	// Key is the flag key. The facade treats it as opaque.
	//
	// Example: request := feature.Request{Key: "checkout", Kind: feature.KindBool}
	Key string

	// Kind is the value type the caller wants.
	// Only KindBool, KindString, KindInt, and KindFloat are accepted. After
	// cancellation, drivers reject empty or unknown kinds before other work.
	//
	// Example: request := feature.Request{Key: "quota", Kind: feature.KindInt}
	Kind Kind

	// Subject is the entity to evaluate for. The facade has already
	// validated its attributes; drivers must not modify them.
	//
	// Example: request := feature.Request{Key: "checkout", Kind: feature.KindBool, Subject: subject}
	Subject Subject
}

// Result is a [Driver]'s answer.
// A nil Value selects the caller's fallback; the facade otherwise validates and
// converts the representation requested by Kind.
//
// Example: result := feature.Result{Value: true, Reason: feature.ReasonStatic}
type Result struct {
	// Value holds a bool, string, int64 or float64 for the requested Kind,
	// or nil when the flag has no value for this subject (Disabled,
	// Default). An Int request may be answered with a float64 holding a
	// whole number; the facade converts it, so drivers never truncate.
	//
	// Example: result := feature.Result{Value: int64(25), Reason: feature.ReasonStatic}
	Value any

	// Variant is the optional vendor variant name.
	//
	// Example: result := feature.Result{Value: true, Variant: "treatment"}
	Variant string

	// Reason explains how the driver chose the result.
	//
	// Example: result := feature.Result{Value: true, Reason: feature.ReasonTargetingMatch}
	Reason Reason
}

// Driver evaluates feature flags. Implementations are read-only: flags are
// managed in the vendor's own tools.
//
// Evaluate first checks cancellation, then accepts only KindBool, KindString,
// KindInt, or KindFloat. An empty or unknown kind fails with an error wrapping
// ErrTypeMismatch before readiness checks, flag lookup, disabled/default
// shortcuts, or provider I/O. This validation does not change Kind serialization.
//
// Evaluate returns the value of request.Key for request.Subject as
// a valid request.Kind:
//   - an unknown key fails with an error wrapping ErrNotFound;
//   - a flag whose value cannot be expressed as Kind fails with an error
//     wrapping ErrTypeMismatch;
//   - before the driver has loaded its flags it fails with an error wrapping
//     ErrNotReady;
//   - a subject the backend rejects fails with an error wrapping
//     ErrInvalidSubject;
//   - a flag that exists but is off returns Value nil, Reason Disabled, nil error;
//   - a flag with no value for this subject returns Value nil, Reason Default.
//
// An empty Subject.Key is valid and means an anonymous subject. A percentage
// rollout then returns its default (Value nil, Reason Default) or a value
// chosen without bucketing, whichever the backend does.
// Every call fails with an error wrapping the context's error when ctx is
// already canceled. Evaluate is safe for concurrent use.
//
// Example:
//
//	var driver feature.Driver = memoryfeature.New()
//	flags := feature.New(driver)
//	value, err := flags.Value(ctx, feature.NewFlag("checkout", false), feature.Subject{})
//	if err != nil {
//		_ = value // The declared fallback is still available.
//	}
type Driver interface {
	// Evaluate resolves the requested flag for its subject and value kind.
	// Cancellation takes precedence over invalid-kind rejection. An empty or
	// unknown kind wraps ErrTypeMismatch before readiness, lookup, any
	// disabled/default shortcut, or provider I/O, as described by Driver.
	//
	// Example:
	//
	//	result, err := driver.Evaluate(ctx, feature.Request{Key: "checkout", Kind: feature.KindBool})
	//	if err != nil {
	//		return err
	//	}
	//	_ = result.Reason
	Evaluate(ctx context.Context, request Request) (Result, error)
}

// Primitive is the set of Go types a flag value can be read as. Named types
// with one of these underlying types, such as type Plan string, qualify.
// Integer types read KindInt flags and float types read KindFloat flags;
// the facade converts between them where the value allows.
// This constraint is for generic definitions and evaluation, not a runtime
// value container or an external provider schema.
//
// Example:
//
//	func evaluate[T feature.Primitive](ctx context.Context, flags *feature.Flags,
//		definition feature.Flag[T], subject feature.Subject) (T, error) {
//		return flags.Value(ctx, definition, subject)
//	}
type Primitive interface {
	~bool | ~string | ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~float32 | ~float64
}

// Detail is an evaluated value with its explanation.
// On failure Value is the definition's fallback and Reason is ReasonError.
//
// Example:
//
//	detail, err := flags.ValueDetail(ctx, feature.NewFlag("plan", "free"), subject)
//	if err != nil {
//		_ = detail.Value // "free" on failure.
//	}
type Detail[T Primitive] struct {
	// Value is the flag's value, or the caller's default when the flag is
	// off, has no value for the subject, or evaluation failed.
	//
	// Example: value := detail.Value
	Value T

	// Variant is the vendor variant name, empty when the driver gave none.
	//
	// Example: variant := detail.Variant
	Variant string

	// Reason explains the result: [ReasonError] on failure, otherwise the
	// driver's reason.
	//
	// Example: failed := detail.Reason == feature.ReasonError
	Reason Reason
}

// Flags evaluates feature flags through a [Driver].
// Construct with New and share Flag definitions between evaluation and setup.
// The facade adds validation, fallback conversion, and optional telemetry/events.
//
// Example:
//
//	flags := feature.New(memoryfeature.New())
//	value, err := flags.Value(ctx, feature.NewFlag("checkout", false), subject)
//	if err != nil {
//		_ = value // The fallback remains false.
//	}
type Flags struct {
	// driver evaluates the flags.
	driver Driver

	// config holds the telemetry settings.
	config settings

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[instruments]
}

// New creates Flags that delegate evaluation to driver. Construction policies configure its
// telemetry: every evaluation emits one internal span and the
// arkoren.feature.evaluations and arkoren.feature.evaluation.duration metrics
// when the telemetry is enabled, and costs nothing otherwise. Flag keys are
// recorded because developers choose them and their number is bounded;
// subject keys, attributes, values, and error text never are.
//
//		flags := feature.New(driver, func(flags *feature.Builder) {
//		flags.Telemetry(app.Telemetry())
//	},
//
//			func(flags *feature.Builder) {
//		flags.Name("launchdarkly")
//	},
//
//	)
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
func New(driver Driver, configure ...BuilderFunc) *Flags {
	return &Flags{
		driver:      driver,
		config:      newSettings(configure...),
		instruments: telemetry.NewInstruments(newInstruments),
	}
}

// Driver returns the underlying Driver.
// Direct Evaluate calls bypass facade validation, fallback conversion, and
// facade telemetry/events; supply a complete raw Request yourself.
//
// Example: driver := flags.Driver()
func (flags *Flags) Driver() Driver {
	return flags.driver
}

// DriverAs returns the first driver of type T in the chain of the underlying
// driver. A driver that wraps another exposes it with an Unwrap() Driver
// method, so wrappers do not hide the concrete driver.
//
// Example:
//
//	driver, ok := flags.DriverAs[*memoryfeature.Driver]()
//	if ok {
//		_ = driver // For example, application-owned memory setup.
//	}
func (flags *Flags) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](flags.Driver())
}

// Value evaluates definition for subject and returns its value as T. It returns
// the definition's fallback when the flag is off, has no value for subject, or
// evaluation fails. A non-nil error always comes with definition.Fallback().
// Invalid definitions fail with [ErrInvalidFlag] before driver access.
//
// An unknown key fails with [ErrNotFound], a value that does not fit T with
// [ErrTypeMismatch] (an int8 request for 300, an int request for 3.5), an
// attribute of an unsupported type with [ErrInvalidSubject], and a driver
// that is still loading with [ErrNotReady]. A flag that is off is not an
// error.
//
//	on, err := flags.Value(ctx, feature.NewFlag("new-checkout", false), subject)
//	if err != nil {
//		log.Print(err) // on retains the declared fallback.
//	}
func (flags *Flags) Value[T Primitive](ctx context.Context, definition Flag[T], subject Subject) (T, error) {
	detail, err := flags.ValueDetail(ctx, definition, subject)

	return detail.Value, err
}

// ValueDetail is [Flags.Value] plus the variant and reason. On failure it
// returns the definition's fallback with ReasonError together with the error;
// when the flag has no value for subject it returns the fallback with the driver's
// variant and reason and a nil error.
//
//	detail, err := flags.ValueDetail(ctx, feature.NewFlag("plan", "free"), subject)
//	if err != nil {
//		log.Print(err) // detail.Value is "free".
//	}
func (flags *Flags) ValueDetail[T Primitive](ctx context.Context, definition Flag[T], subject Subject) (Detail[T], error) {
	key, def := definition.Name(), definition.Fallback()
	ctx, op := flags.begin(ctx, key)
	failed := Detail[T]{Value: def, Reason: ReasonError}
	kind := kindOf[T]()

	if err := definition.Err(); err != nil {
		op.end(ctx, err, ReasonError, "")

		return failed, err
	}

	if err := validateSubject(subject); err != nil {
		op.end(ctx, err, ReasonError, "")

		return failed, err
	}

	result, err := flags.driver.Evaluate(ctx, Request{
		Key:     key,
		Kind:    kind,
		Subject: subject,
	})

	if err != nil {
		err = fmt.Errorf("feature: evaluate %q: %w", key, err)
		op.end(ctx, err, ReasonError, "")
		flags.config.Events.Emit(ctx, EvaluationFailed, EvaluationFailedEvent{Key: key, Err: err})

		return failed, err
	}

	result.Reason = cmp.Or(result.Reason, ReasonUnknown)

	if result.Value == nil {
		op.end(ctx, nil, result.Reason, result.Variant)

		return Detail[T]{
			Value:   def,
			Variant: result.Variant,
			Reason:  result.Reason,
		}, nil
	}

	value, err := convert[T](kind, result.Value)

	if err != nil {
		err = fmt.Errorf("feature: evaluate %q: %w", key, err)
		op.end(ctx, err, ReasonError, "")

		return failed, err
	}

	op.end(ctx, nil, result.Reason, result.Variant)

	return Detail[T]{
		Value:   value,
		Variant: result.Variant,
		Reason:  result.Reason,
	}, nil
}
