package featuretest

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/feature"
)

// pollInterval is how often a read is retried while waiting for a change to
// propagate.
const pollInterval = 100 * time.Millisecond

// concurrentReaders is the number of goroutines in the concurrency subtest.
const concurrentReaders = 50

// Flag is a fixture the suite installs through a [Harness].
//
// A flag always carries a default Value, or at least one override: the suite
// never asks a harness to create a flag with neither, because some backends
// (memory among them) cannot represent one.
//
// Example: fixture := featuretest.Flag{Key: "checkout", Value: true}
type Flag struct {
	// Key is the flag key, unique per subtest.
	//
	// Example: fixture := featuretest.Flag{Key: "checkout", Value: true}
	Key string

	// Value is the default value as bool, string, int64 or float64. Nil means
	// the flag has no default value and only its Overrides.
	//
	// Example: fixture := featuretest.Flag{Key: "quota", Value: int64(25)}
	Value any

	// Overrides maps a subject key to the value that subject gets, with the
	// same types as Value.
	//
	// Example: fixture := featuretest.Flag{Key: "checkout", Overrides: map[string]any{"user-42": true}}
	Overrides map[string]any

	// Disabled turns the flag off. The harness installs Value and Overrides
	// first and then disables the flag, because a flag must exist to be
	// disabled.
	//
	// Example: fixture := featuretest.Flag{Key: "checkout", Value: true, Disabled: true}
	Disabled bool
}

// Harness gives the suite a driver and control over its backend's flags.
//
// The suite calls Set before the first evaluation of a flag, and may call it
// again, or Delete, after the driver has served evaluations. Changes may
// become visible asynchronously when [WithPropagation] is given.
// A consumer implements this interface using its disposable backend setup;
// production flag-management operations do not belong to feature.Driver.
//
// Example:
//
//	featuretest.Run(t, func(*testing.T) featuretest.Harness {
//		return harness // A caller-owned implementation for an isolated backend.
//	})
type Harness interface {
	// Driver returns the driver under test. It must return the same driver on
	// every call and be ready to evaluate (started, if it has a lifecycle).
	//
	// Example: driver := harness.Driver()
	Driver() feature.Driver

	// Set creates flag or replaces its definition entirely. It fails the test
	// itself on error.
	//
	// Example: harness.Set(t, featuretest.Flag{Key: "checkout", Value: true})
	Set(t *testing.T, flag Flag)

	// Delete removes the flag key. Deleting a key that does not exist is not an
	// error. It fails the test itself on error.
	//
	// Example: harness.Delete(t, "checkout")
	Delete(t *testing.T, key string)
}

// config holds the settings produced by [Option] values.
type config struct {
	// propagation is how long a change may take to become visible. Zero means
	// changes are visible immediately.
	propagation time.Duration

	// noUpdates skips the subtests that change a served flag.
	noUpdates bool
}

// Option declares a capability or environment of the driver under test.
// Options never relax the contract's semantics.
//
// Example: options := []featuretest.Option{featuretest.WithPropagation(time.Second)}
type Option func(*config)

// WithPropagation declares that changes made through the [Harness] reach the
// driver asynchronously, within timeout. Every read that follows a Set or
// Delete, including the first read after the initial Set, is retried every
// 100 ms until it matches the expected state or the timeout passes. Without
// this option every read happens once.
// This controls the test's observation budget, not provider retry behavior.
//
// Example: option := featuretest.WithPropagation(time.Second)
func WithPropagation(timeout time.Duration) Option {
	return func(config *config) {
		config.propagation = timeout
	}
}

// WithoutUpdates skips the subtests that change or delete a flag after the
// driver has served it, for backends where that cannot be observed in a test.
// It does not change the expected result of any evaluation that is exercised.
//
// Example: option := featuretest.WithoutUpdates()
func WithoutUpdates() Option {
	return func(config *config) {
		config.noUpdates = true
	}
}

// suite carries the state shared by the subtests of one [Run] call.
type suite struct {
	// newHarness creates the harness under test.
	newHarness func(t *testing.T) Harness

	// config holds the declared options.
	config config
}

// Run executes the feature flag conformance suite against harnesses produced
// by newHarness. The factory is called once per subtest, so each subtest gets
// an isolated driver where the backend allows it; subtests also use unique
// flag keys so a shared backend works. Run calls t.Run for every group and
// marks the subtests parallel.
// Invalid-kind cases call the raw driver directly without propagation retries,
// including missing, disabled, default-only, and unmatched-override flags.
// Cancellation must take precedence. Harness drivers are ready by contract;
// adapters must separately test rejection before readiness or provider I/O.
//
//	featuretest.Run(t, func(*testing.T) featuretest.Harness {
//		return harness // A caller-owned implementation for an isolated backend.
//	}, featuretest.WithPropagation(time.Second))
func Run(t *testing.T, newHarness func(t *testing.T) Harness, opts ...Option) {
	t.Helper()

	var settings config

	for _, opt := range opts {
		opt(&settings)
	}

	s := suite{newHarness: newHarness, config: settings}

	for _, group := range []struct {
		// name labels the conformance group in test output.
		name string
		// fn runs the group with its shared suite configuration.
		fn func(*testing.T, suite)
	}{
		{"Values", values},
		{"Targeting", targeting},
		{"Errors", errorCases},
		{"InvalidKinds", invalidKinds},
		{"Disabled", disabled},
		{"Updates", updates},
		{"Concurrency", concurrency},
	} {
		t.Run(group.name, func(t *testing.T) {
			group.fn(t, s)
		})
	}
}

// sub runs fn as a parallel subtest with a fresh harness and a unique flag
// key, removed again when the subtest ends.
func (suite suite) sub(t *testing.T, name string, fn func(t *testing.T, h Harness, key string)) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Parallel()

		h := suite.newHarness(t)
		key := "arkoren-conformance-" + rand.Text()
		t.Cleanup(func() {
			h.Delete(t, key)
		})

		fn(t, h, key)
	})
}

// eventually calls cond until it returns true or the propagation timeout
// passes. Without propagation it calls cond exactly once.
func (suite suite) eventually(cond func() bool) {
	deadline := time.Now().Add(suite.config.propagation)

	for !cond() && time.Now().Before(deadline) {
		time.Sleep(pollInterval)
	}
}

// read evaluates key for subject as kind, retrying until done reports true or
// the propagation timeout passes, and returns the last outcome for the caller
// to check.
func (suite suite) read(t *testing.T, h Harness, key string, kind feature.Kind, subject feature.Subject, done func(feature.Result, error) bool) (result feature.Result, err error) {
	t.Helper()

	request := feature.Request{Key: key, Kind: kind, Subject: subject}
	suite.eventually(func() bool {
		result, err = h.Driver().Evaluate(t.Context(), request)

		return done(result, err)
	})

	return result, err
}

// hasValue reports whether an evaluation succeeded with exactly want, which
// also compares the dynamic Go type.
func hasValue(want any) func(feature.Result, error) bool {
	return func(result feature.Result, err error) bool {
		return err == nil && result.Value == want
	}
}

// hasError reports whether an evaluation failed with an error wrapping target.
func hasError(target error) func(feature.Result, error) bool {
	return func(_ feature.Result, err error) bool {
		return errors.Is(err, target)
	}
}

// wantValue asserts that key evaluates to want for subject as kind, and
// returns the result so callers can check the reason.
func (suite suite) wantValue(t *testing.T, h Harness, key string, kind feature.Kind, subject feature.Subject, want any) feature.Result {
	t.Helper()

	result, err := suite.read(t, h, key, kind, subject, hasValue(want))

	if err != nil {
		t.Fatalf("Evaluate(%q, %s) = %v, want %#v", key, kind, err, want)
	}

	if result.Value != want {
		t.Fatalf("Evaluate(%q, %s) value = %#v, want %#v", key, kind, result.Value, want)
	}

	return result
}

// wantPlain asserts a plain (not overridden, not disabled) value, whose
// reason may be Static, Default or Unknown.
func (suite suite) wantPlain(t *testing.T, h Harness, key string, kind feature.Kind, subject feature.Subject, want any) {
	t.Helper()

	result := suite.wantValue(t, h, key, kind, subject, want)

	switch result.Reason {
	case "", feature.ReasonStatic, feature.ReasonDefault, feature.ReasonUnknown:
	default:
		t.Errorf("Evaluate(%q, %s) reason = %s, want static, default or unknown", key, kind, result.Reason)
	}
}

// wantError asserts that evaluating key as kind fails with an error wrapping
// target.
func (suite suite) wantError(t *testing.T, h Harness, key string, kind feature.Kind, target error) {
	t.Helper()

	_, err := suite.read(t, h, key, kind, feature.Subject{}, hasError(target))

	if !errors.Is(err, target) {
		t.Fatalf("Evaluate(%q, %s) error = %v, want %v", key, kind, err, target)
	}
}

// values covers each kind and numeric widening.
func values(t *testing.T, s suite) {
	s.sub(t, "Kinds/Bool", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{
			Key:   key,
			Value: true,
		})

		s.wantPlain(t, h, key, feature.KindBool, feature.Subject{Key: "u1"}, true)
	})
	s.sub(t, "Kinds/String", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{
			Key:   key,
			Value: "value",
		})

		s.wantPlain(t, h, key, feature.KindString, feature.Subject{Key: "u1"}, "value")
	})
	s.sub(t, "Kinds/Int", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{
			Key:   key,
			Value: int64(42),
		})

		s.wantPlain(t, h, key, feature.KindInt, feature.Subject{Key: "u1"}, int64(42))
	})
	s.sub(t, "Kinds/Float", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{
			Key:   key,
			Value: 1.5,
		})

		s.wantPlain(t, h, key, feature.KindFloat, feature.Subject{Key: "u1"}, 1.5)
	})

	// The driver may answer with a float64 or an int64; the facade converts
	// either, so the check goes through it.
	s.sub(t, "IntAsFloat", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: int64(3)})

		flags := feature.New(h.Driver())

		var got float64
		var err error
		s.eventually(func() bool {
			got, err = flags.Value(t.Context(), feature.NewFlag(key, -1.0), feature.Subject{Key: "u1"})

			return err == nil && got == 3
		})

		if err != nil || got != 3 {
			t.Errorf("Value[float64](%q) = %v, %v, want 3, nil", key, got, err)
		}
	})
}

// targeting covers subject overrides, anonymous subjects and attributes.
func targeting(t *testing.T, s suite) {
	s.sub(t, "Override", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: "a", Overrides: map[string]any{"u1": "b"}})

		result := s.wantValue(t, h, key, feature.KindString, feature.Subject{Key: "u1"}, "b")

		if result.Reason != feature.ReasonTargetingMatch {
			t.Errorf("override reason = %s, want targeting_match", result.Reason)
		}

		s.wantPlain(t, h, key, feature.KindString, feature.Subject{Key: "u2"}, "a")
	})
	s.sub(t, "Anonymous", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: "a", Overrides: map[string]any{"u1": "b"}})

		s.wantPlain(t, h, key, feature.KindString, feature.Subject{}, "a")
	})
	s.sub(t, "Attributes", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: true})

		subject := feature.Subject{Key: "u1", Attributes: map[string]any{
			"string":   "pro",
			"bool":     true,
			"int64":    int64(7),
			"float64":  2.5,
			"strings":  []string{"a", "b"},
			"time":     time.Unix(1_700_000_000, 0).UTC(),
			"emptykey": "",
		}}

		s.wantPlain(t, h, key, feature.KindBool, subject, true)
	})
}

// errorCases covers unknown keys, type mismatches and cancellation.
func errorCases(t *testing.T, s suite) {
	s.sub(t, "NotFound", func(t *testing.T, h Harness, key string) {
		_, err := h.Driver().Evaluate(t.Context(), feature.Request{Key: key, Kind: feature.KindBool})

		if !errors.Is(err, feature.ErrNotFound) {
			t.Errorf("Evaluate(unknown) error = %v, want ErrNotFound", err)
		}
	})
	s.sub(t, "TypeMismatch", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: "text"})

		s.wantError(t, h, key, feature.KindBool, feature.ErrTypeMismatch)
	})
	s.sub(t, "Canceled", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: true})

		s.wantPlain(t, h, key, feature.KindBool, feature.Subject{}, true)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := h.Driver().Evaluate(ctx, feature.Request{Key: key, Kind: feature.KindBool})

		if !errors.Is(err, context.Canceled) {
			t.Errorf("Evaluate(canceled) error = %v, want context.Canceled", err)
		}
	})
}

// disabled covers a flag that exists but is off.
func disabled(t *testing.T, s suite) {
	s.sub(t, "Disabled", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: true, Disabled: true})

		result, err := s.read(t, h, key, feature.KindBool, feature.Subject{Key: "u1"}, func(r feature.Result, err error) bool {
			return err == nil && r.Reason == feature.ReasonDisabled
		})

		if err != nil || result.Value != nil || result.Reason != feature.ReasonDisabled {
			t.Errorf("Evaluate(disabled) = %#v, %v, want nil value, disabled, nil", result, err)
		}
	})
}

// updates covers changes to a flag after the driver has served it.
func updates(t *testing.T, s suite) {
	if s.config.noUpdates {
		t.Skip("WithoutUpdates")
	}

	s.sub(t, "Update", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: "a"})

		s.wantPlain(t, h, key, feature.KindString, feature.Subject{}, "a")

		h.Set(t, Flag{Key: key, Value: "b"})

		s.wantPlain(t, h, key, feature.KindString, feature.Subject{}, "b")
	})
	s.sub(t, "Delete", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: true})

		s.wantPlain(t, h, key, feature.KindBool, feature.Subject{}, true)

		h.Delete(t, key)

		s.wantError(t, h, key, feature.KindBool, feature.ErrNotFound)
	})
}

// concurrency covers parallel evaluation of one flag; it is meaningful under
// the race detector.
func concurrency(t *testing.T, s suite) {
	s.sub(t, "Concurrency", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: "a", Overrides: map[string]any{"u1": "b"}})

		s.wantPlain(t, h, key, feature.KindString, feature.Subject{}, "a")

		var wg sync.WaitGroup

		for i := range concurrentReaders {
			wg.Go(func() {
				subject, want := feature.Subject{Key: "u2"}, "a"

				if i%2 == 0 {
					subject, want = feature.Subject{Key: "u1"}, "b"
				}

				result, err := h.Driver().Evaluate(t.Context(), feature.Request{
					Key:     key,
					Kind:    feature.KindString,
					Subject: subject,
				})

				if err != nil || result.Value != want {
					t.Errorf("Evaluate(%q) = %#v, %v, want %q", subject.Key, result, err, want)
				}
			})
		}

		wg.Wait()
	})
}
