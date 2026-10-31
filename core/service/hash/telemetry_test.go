package hash_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/service/hash"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// driverPanic identifies a panic raised inside a hashing driver, allowing tests
// to distinguish it from an accidental failure in encoding or instrumentation.
const driverPanic = "hash driver panic"

// codecPanic identifies a panic raised by application-owned JSON encoding.
const codecPanic = "hash codec panic"

// panickingHash never completes a driver operation. Its methods expose whether
// the facade mistakenly treats unwinding as an ordinary completed result.
type panickingHash struct{}

// Hash panics without returning a hash or an error.
func (panickingHash) Hash([]byte) ([]byte, error) {
	panic(driverPanic)
}

// Check panics without returning a match or an error.
func (panickingHash) Check([]byte, []byte) (bool, error) {
	panic(driverPanic)
}

// NeedsRehash panics rather than producing a rehash policy observation.
func (panickingHash) NeedsRehash([]byte) bool {
	panic(driverPanic)
}

// panickingEncoding models application code invoked during typed encoding.
// Driver calls must not occur after its MarshalJSON panics.
type panickingEncoding struct{}

// MarshalJSON propagates an application panic instead of returning JSON.
func (panickingEncoding) MarshalJSON() ([]byte, error) {
	panic(codecPanic)
}

// assertHashPanicUnobserved invokes a facade operation and verifies both exact
// panic propagation and absence of completed metric observations. Instrument
// declarations are allowed; recorded points, including zero-valued ones, are not.
func assertHashPanicUnobserved(t *testing.T, want string, driver hash.Driver, call func(*hash.Hasher)) {
	t.Helper()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	hasher := hash.New(driver, func(hasher *hash.Builder) {
		hasher.Telemetry(metrics)
	})
	func() {
		defer func() {
			if got := recover(); got != want {
				t.Fatalf("panic = %v, want %q", got, want)
			}
		}()
		call(hasher)
	}()

	for _, metric := range fake.Metrics() {
		if len(metric.Points) != 0 {
			t.Fatalf("panic recorded completed metric: %+v", metric)
		}
	}

	fake.AssertSpanCount(t, 0)
}

// TestHashDriverPanicHasNoCompletedMetrics checks typed hashing unwinding.
func TestHashDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertHashPanicUnobserved(t, driverPanic, panickingHash{}, func(hasher *hash.Hasher) {
		_, _ = hasher.Hash("password")
	})
}

// TestHashRawDriverPanicHasNoCompletedMetrics checks raw hashing unwinding.
func TestHashRawDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertHashPanicUnobserved(t, driverPanic, panickingHash{}, func(hasher *hash.Hasher) {
		_, _ = hasher.HashRaw([]byte("password"))
	})
}

// TestCheckDriverPanicHasNoCompletedMetrics rejects a fabricated mismatch.
func TestCheckDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertHashPanicUnobserved(t, driverPanic, panickingHash{}, func(hasher *hash.Hasher) {
		_, _ = hasher.Check("password", nil)
	})
}

// TestCheckRawDriverPanicHasNoCompletedMetrics checks raw verification unwinding.
func TestCheckRawDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertHashPanicUnobserved(t, driverPanic, panickingHash{}, func(hasher *hash.Hasher) {
		_, _ = hasher.CheckRaw([]byte("password"), nil)
	})
}

// TestNeedsRehashDriverPanicHasNoCompletedMetrics rejects a fabricated current
// policy observation when the driver never returned its policy decision.
func TestNeedsRehashDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertHashPanicUnobserved(t, driverPanic, panickingHash{}, func(hasher *hash.Hasher) {
		hasher.NeedsRehash(nil)
	})
}

// TestHashCodecPanicHasNoCompletedMetrics checks application encoding unwinding.
func TestHashCodecPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertHashPanicUnobserved(t, codecPanic, panickingHash{}, func(hasher *hash.Hasher) {
		_, _ = hasher.Hash(panickingEncoding{})
	})
}

// TestCheckCodecPanicHasNoCompletedMetrics checks encoding is not a mismatch.
func TestCheckCodecPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertHashPanicUnobserved(t, codecPanic, panickingHash{}, func(hasher *hash.Hasher) {
		_, _ = hasher.Check(panickingEncoding{}, nil)
	})
}

// TestHashMetricsCountTypedAndRawOnce checks scope and absence of traces.
func TestHashMetricsCountTypedAndRawOnce(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	hasher := hash.New(identity{}, func(hasher *hash.Builder) {
		hasher.Telemetry(metrics)
	})

	if _, err := hasher.Hash("private-password"); err != nil {
		t.Fatal(err)
	}

	if _, err := hasher.HashRaw([]byte("private-password")); err != nil {
		t.Fatal(err)
	}

	if match, err := hasher.Check("private-password", []byte("private-password")); err != nil || !match {
		t.Fatalf("Check = %v, %v", match, err)
	}

	if match, err := hasher.CheckRaw([]byte("wrong"), []byte("private-password")); err != nil || match {
		t.Fatalf("CheckRaw = %v, %v", match, err)
	}

	if hasher.NeedsRehash([]byte("private-password")) {
		t.Fatal("unexpected rehash")
	}

	fake.AssertCounter(t, "arkoren.hash.operations", 5)
	fake.AssertCounter(t, "arkoren.hash.operations", 2, telemetry.String("arkoren.hash.operation", "hash"))
	fake.AssertCounter(t, "arkoren.hash.operations", 1, telemetry.String("arkoren.hash.outcome", "match"))
	fake.AssertCounter(t, "arkoren.hash.operations", 1, telemetry.String("arkoren.hash.outcome", "mismatch"), telemetry.String("error.type", ""))
	fake.AssertHistogram(t, "arkoren.hash.operation.duration", 5)
	fake.AssertSpanCount(t, 0)

	if strings.Contains(fmt.Sprint(fake.Metrics()), "private-password") {
		t.Fatal("password leaked to metrics")
	}
}

// failedHash returns a supplied error from both driver operations, allowing
// sentinel identity and native-error redaction to be checked independently.
type failedHash struct {
	// identity supplies the unchanged rehash policy for this error-only driver.
	identity
	// err is returned unchanged to the caller, not emitted to telemetry.
	err error
}

// Hash preserves the configured error without processing the value.
func (driver failedHash) Hash([]byte) ([]byte, error) {
	return nil, driver.err
}

// Check preserves the configured error rather than reporting a mismatch.
func (driver failedHash) Check([]byte, []byte) (bool, error) {
	return false, driver.err
}

// TestHashMetricsRedactErrors checks bounded classification and error identity.
func TestHashMetricsRedactErrors(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	errSecret := fmt.Errorf("private-native-error: %w", hash.ErrInvalidHash)
	hasher := hash.New(failedHash{err: errSecret}, func(hasher *hash.Builder) {
		hasher.Telemetry(metrics)
	})

	if _, err := hasher.CheckRaw(nil, nil); !errors.Is(err, errSecret) {
		t.Fatalf("error identity lost: %v", err)
	}

	if _, err := hasher.HashRaw(nil); !errors.Is(err, errSecret) {
		t.Fatalf("error identity lost: %v", err)
	}

	fake.AssertCounter(t, "arkoren.hash.operations", 2, telemetry.String("error.type", "hash.ErrInvalidHash"))

	if strings.Contains(fmt.Sprint(fake.Metrics()), "private-native-error") {
		t.Fatal("native error leaked")
	}
}

// TestHashMetricsUnknownErrorHasFixedClass checks an arbitrary backend failure
// cannot introduce a new native error type or secret-bearing label.
func TestHashMetricsUnknownErrorHasFixedClass(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	want := errors.New("private-driver-detail")
	hasher := hash.New(failedHash{err: want}, func(hasher *hash.Builder) {
		hasher.Telemetry(metrics)
	})

	if _, err := hasher.Hash("private-input"); !errors.Is(err, want) {
		t.Fatalf("Hash = %v", err)
	}

	fake.AssertCounter(t, "arkoren.hash.operations", 1, telemetry.String("error.type", "error"))

	if strings.Contains(fmt.Sprint(fake.Metrics()), "private-") {
		t.Fatal("input or native error leaked")
	}
}

// TestHashMetricsIncludeEncodingFailures checks codec failures without driver
// calls and verifies both typed entry points report one final failure.
func TestHashMetricsIncludeEncodingFailures(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	hasher := hash.New(nil, func(hasher *hash.Builder) {
		hasher.Telemetry(metrics)
	})

	if _, err := hasher.Hash(make(chan int)); err == nil {
		t.Fatal("Hash accepted a channel")
	}

	if _, err := hasher.Check(make(chan int), nil); err == nil {
		t.Fatal("Check accepted a channel")
	}

	fake.AssertCounter(t, "arkoren.hash.operations", 2, telemetry.String("error.type", "error"))
}

// TestHashMetricsFollowDefault checks dynamic default replacement and an
// explicitly disabled facade, without racing other tests' global configuration.
func TestHashMetricsFollowDefault(t *testing.T) {
	old := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(old)
	})

	first, second := telemetrytest.New(), telemetrytest.New()
	one, two := first.Telemetry(t), second.Telemetry(t)
	hasher := hash.New(identity{}, func(hasher *hash.Builder) {
		hasher.Telemetry(one).Telemetry(nil)
	})

	disabled := hash.New(identity{}, func(hasher *hash.Builder) {
		hasher.Telemetry(telemetry.New(nil))
	})
	telemetry.SetDefault(one)
	hasher.NeedsRehash(nil)
	telemetry.SetDefault(two)
	hasher.NeedsRehash(nil)
	disabled.NeedsRehash(nil)

	first.AssertCounter(t, "arkoren.hash.operations", 1)
	second.AssertCounter(t, "arkoren.hash.operations", 1)
}

// TestHashMetricsConcurrentResolution exercises cold instrument resolution and
// aggregate recording with an immutable driver under the race detector.
func TestHashMetricsConcurrentResolution(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	hasher := hash.New(identity{}, func(hasher *hash.Builder) {
		hasher.Telemetry(metrics)
	})

	var workers sync.WaitGroup

	for range 20 {
		workers.Go(func() {
			hasher.NeedsRehash(nil)
		})
	}

	workers.Wait()

	fake.AssertCounter(t, "arkoren.hash.operations", 20)
}

// TestHashDisabledMetricsAllocateNothing verifies the boolean-only policy
// check adds no allocations when explicitly disabled, independent of defaults.
func TestHashDisabledMetricsAllocateNothing(t *testing.T) {
	hasher := hash.New(identity{}, func(hasher *hash.Builder) {
		hasher.Telemetry(telemetry.New(nil))
	})

	allocations := testing.AllocsPerRun(100, func() {
		hasher.NeedsRehash(nil)
	})

	if allocations != 0 {
		t.Fatalf("disabled allocations = %v", allocations)
	}
}
