package memoryfeature

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sync"

	"arkoren.dev/core/service/feature"
)

// flag is one stored flag.
type flag struct {
	// value is the default value as bool, string, int64 or float64. Nil means
	// the flag has no default value, only overrides.
	value any

	// overrides maps a subject key to its value, normalized like value.
	overrides map[string]any

	// disabled makes Evaluate report the flag as off.
	disabled bool
}

// Driver implements [feature.Driver] in process memory. It is safe for
// concurrent use and needs no Start or Stop.
//
// Values passed to [Driver.Set] and [Driver.SetFor] share the exact Go type
// of a feature.Flag definition. Integers are stored as int64 and floats as
// float64, including named types. Definitions do not create provider schemas.
//
//	driver := memoryfeature.New()
//	if err := driver.Set(feature.NewFlag("max-upload-mb", 10), 25); err != nil {
//		log.Print(err)
//	}
type Driver struct {
	// mu guards flags.
	mu sync.RWMutex

	// flags maps flag keys to their definitions.
	flags map[string]flag
}

var _ feature.Driver = (*Driver)(nil)

// New returns an empty Driver.
// It starts no background work and requires no lifecycle registration.
//
// Example:
//
//	driver := memoryfeature.New()
//	if err := driver.Set(feature.NewFlag("checkout", false), true); err != nil {
//		return err
//	}
func New() *Driver {
	return &Driver{flags: make(map[string]flag)}
}

// normalize converts a supported primitive, including named types, to the
// driver's canonical representation without a JSON or float round trip.
func normalize[T feature.Primitive](value T) any {
	v := reflect.ValueOf(value)

	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.String:
		return v.String()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	default:
		return v.Int()
	}
}

// Set creates definition's provider key or replaces its stored default value,
// keeping overrides and disabled state. The definition's fallback is not
// installed: evaluation alone applies it. Invalid definitions return their
// Err without mutating the driver; value must have the definition's Go type.
//
//	checkout := feature.NewFlag("new-checkout", false)
//	if err := driver.Set(checkout, true); err != nil {
//		log.Print(err)
//	}
func (driver *Driver) Set[T feature.Primitive](definition feature.Flag[T], value T) error {
	if err := definition.Err(); err != nil {
		return err
	}

	key := definition.Name()

	driver.mu.Lock()
	defer driver.mu.Unlock()

	f := driver.flags[key]
	f.value = normalize(value)
	driver.flags[key] = f

	return nil
}

// SetFor sets definition's value for the subject whose [feature.Subject.Key] is
// subjectKey. It creates the flag, with no default value, when it is missing,
// and keeps the default of an existing one. Like Set it validates the definition
// before mutation, and requires its exact Go type. An empty subject key never
// matches during evaluation.
//
//	checkout := feature.NewFlag("new-checkout", false)
//	if err := driver.SetFor(checkout, "user-42", true); err != nil {
//		log.Print(err)
//	}
func (driver *Driver) SetFor[T feature.Primitive](definition feature.Flag[T], subjectKey string, value T) error {
	if err := definition.Err(); err != nil {
		return err
	}

	key := definition.Name()

	driver.mu.Lock()
	defer driver.mu.Unlock()

	f := driver.flags[key]
	// Evaluate reads overrides after releasing the lock, so the map is
	// replaced, never modified in place.
	f.overrides = maps.Clone(f.overrides)

	if f.overrides == nil {
		f.overrides = make(map[string]any)
	}

	f.overrides[subjectKey] = normalize(value)
	driver.flags[key] = f

	return nil
}

// Disable turns key off. The flag stays known, so Evaluate returns no value
// with [feature.ReasonDisabled] instead of [feature.ErrNotFound]. Disabling a
// missing key does nothing.
//
// Example: driver.Disable(definition.Name())
func (driver *Driver) Disable(key string) {
	driver.setDisabled(key, true)
}

// Enable undoes [Driver.Disable]. Enabling a missing key does nothing.
// Overrides and the stored default are retained while disabled.
//
// Example: driver.Enable(definition.Name())
func (driver *Driver) Enable(key string) {
	driver.setDisabled(key, false)
}

// setDisabled changes the disabled state of an existing flag.
func (driver *Driver) setDisabled(key string, disabled bool) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	f, ok := driver.flags[key]

	if !ok {
		return
	}

	f.disabled = disabled
	driver.flags[key] = f
}

// Delete removes the flag key and its overrides. Deleting a missing key does
// nothing.
//
// Example: driver.Delete(definition.Name())
func (driver *Driver) Delete(key string) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	delete(driver.flags, key)
}

// Evaluate implements [feature.Driver]. A canceled ctx fails with its error,
// then an empty or unknown request.Kind fails with [feature.ErrTypeMismatch]
// before flag lookup. Known kinds are Bool, String, Int, and Float. An unknown
// key fails with [feature.ErrNotFound]. A stored value that cannot be read as a
// known kind fails with [feature.ErrTypeMismatch]. A disabled flag
// returns no value with [feature.ReasonDisabled]; a matching override returns
// [feature.ReasonTargetingMatch]; a default value returns
// [feature.ReasonStatic]; a flag with only non-matching overrides returns no
// value with [feature.ReasonDefault]. Subject attributes are ignored and an
// empty subject key never matches an override.
//
// Example:
//
//	result, err := driver.Evaluate(ctx, feature.Request{Key: "checkout", Kind: feature.KindBool})
//	if err != nil {
//		return err
//	}
//	_ = result.Value
func (driver *Driver) Evaluate(ctx context.Context, request feature.Request) (feature.Result, error) {
	if err := ctx.Err(); err != nil {
		return feature.Result{}, fmt.Errorf("memoryfeature: evaluate %q: %w", request.Key, err)
	}

	switch request.Kind {
	case feature.KindBool, feature.KindString, feature.KindInt, feature.KindFloat:
	default:
		return feature.Result{}, fmt.Errorf("memoryfeature: evaluate %q: unsupported kind %q: %w",
			request.Key, request.Kind, feature.ErrTypeMismatch)
	}

	driver.mu.RLock()

	stored, ok := driver.flags[request.Key]
	driver.mu.RUnlock()

	if !ok {
		return feature.Result{}, fmt.Errorf("memoryfeature: flag %q: %w", request.Key, feature.ErrNotFound)
	}

	return stored.evaluate(request)
}

// evaluate resolves a validated request against one immutable flag snapshot.
// It runs without the driver's mutex: SetFor replaces override maps rather than
// mutating them. Disabled and unmatched flags return no value only after the
// driver has checked cancellation and kind validity.
func (stored flag) evaluate(request feature.Request) (feature.Result, error) {
	if stored.disabled {
		return feature.Result{Reason: feature.ReasonDisabled}, nil
	}

	value, reason := stored.value, feature.ReasonStatic

	if override, ok := stored.overrides[request.Subject.Key]; ok && request.Subject.Key != "" {
		value, reason = override, feature.ReasonTargetingMatch
	}

	if value == nil {
		return feature.Result{Reason: feature.ReasonDefault}, nil
	}

	value, ok := as(request.Kind, value)

	if !ok {
		return feature.Result{}, fmt.Errorf("memoryfeature: flag %q stored as %T, requested as %s: %w",
			request.Key, value, request.Kind, feature.ErrTypeMismatch)
	}

	return feature.Result{Value: value, Reason: reason}, nil
}

// as returns value in the representation kind expects, and false when value
// cannot be read as kind. An int64 satisfies KindInt, and becomes a float64
// for KindFloat; a float64 satisfies KindInt too, leaving the facade to check
// that it is whole.
func as(kind feature.Kind, value any) (any, bool) {
	switch v := value.(type) {
	case bool:
		return v, kind == feature.KindBool
	case string:
		return v, kind == feature.KindString
	case int64:
		switch kind {
		case feature.KindInt:
			return v, true
		case feature.KindFloat:
			return float64(v), true
		}
	case float64:
		return v, kind == feature.KindInt || kind == feature.KindFloat
	}

	return value, false
}
