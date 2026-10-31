package crypto_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// driverPanic identifies panics from driver calls, including fallback attempts.
const driverPanic = "crypto driver panic"

// codecPanic identifies application panics in typed JSON encoding or decoding.
const codecPanic = "crypto codec panic"

// panickingCrypto never completes a cipher operation. Its failures must not
// produce success observations or trigger panic recovery in the facade.
type panickingCrypto struct{}

// Encrypt panics without producing ciphertext or an ordinary error.
func (panickingCrypto) Encrypt([]byte) ([]byte, error) {
	panic(driverPanic)
}

// Decrypt panics without producing plaintext or an ordinary error.
func (panickingCrypto) Decrypt([]byte) ([]byte, error) {
	panic(driverPanic)
}

// panickingCodec models application-owned JSON methods invoked by typed calls.
type panickingCodec struct{}

// MarshalJSON panics rather than supplying bytes to the encryption driver.
func (panickingCodec) MarshalJSON() ([]byte, error) {
	panic(codecPanic)
}

// UnmarshalJSON panics after successful authenticated decryption. Even when a
// previous driver supplied the plaintext, no completed logical result exists.
func (*panickingCodec) UnmarshalJSON([]byte) error {
	panic(codecPanic)
}

// assertCryptoPanicUnobserved configures a nonempty key chain, invokes call and
// verifies exact panic propagation without completed metric points or traces.
// Instrument declarations alone are allowed; each driver remains shared.
func assertCryptoPanicUnobserved(t *testing.T, want string, drivers []crypto.Driver, call func(*crypto.Encrypter)) {
	t.Helper()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)

	encrypter, err := crypto.New(drivers[0], func(encrypter *crypto.Builder) {
		encrypter.Telemetry(metrics).Previous(drivers[1:]...)
	})

	if err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if got := recover(); got != want {
				t.Fatalf("panic = %v, want %q", got, want)
			}
		}()
		call(encrypter)
	}()

	for _, metric := range fake.Metrics() {
		if len(metric.Points) != 0 {
			t.Fatalf("panic recorded completed metric: %+v", metric)
		}
	}

	fake.AssertSpanCount(t, 0)
}

// TestEncryptDriverPanicHasNoCompletedMetrics checks typed encryption unwinding.
func TestEncryptDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertCryptoPanicUnobserved(t, driverPanic, []crypto.Driver{panickingCrypto{}}, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.Encrypt("token")
	})
}

// TestEncryptRawDriverPanicHasNoCompletedMetrics checks raw encryption unwinding.
func TestEncryptRawDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertCryptoPanicUnobserved(t, driverPanic, []crypto.Driver{panickingCrypto{}}, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.EncryptRaw(nil)
	})
}

// TestDecryptDriverPanicHasNoCompletedMetrics checks typed primary-key unwinding.
func TestDecryptDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertCryptoPanicUnobserved(t, driverPanic, []crypto.Driver{panickingCrypto{}}, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.Decrypt[string](nil)
	})
}

// TestDecryptWithRotationDriverPanicHasNoCompletedMetrics checks primary-key
// unwinding does not become a successful current-key rotation observation.
func TestDecryptWithRotationDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertCryptoPanicUnobserved(t, driverPanic, []crypto.Driver{panickingCrypto{}}, func(encrypter *crypto.Encrypter) {
		_, _, _ = encrypter.DecryptWithRotation[string](nil)
	})
}

// TestDecryptRawDriverPanicHasNoCompletedMetrics checks raw wrapper unwinding.
func TestDecryptRawDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertCryptoPanicUnobserved(t, driverPanic, []crypto.Driver{panickingCrypto{}}, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.DecryptRaw(nil)
	})
}

// TestDecryptRawWithRotationDriverPanicHasNoCompletedMetrics checks raw primary
// decryption does not report success when it panics instead of returning.
func TestDecryptRawWithRotationDriverPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertCryptoPanicUnobserved(t, driverPanic, []crypto.Driver{panickingCrypto{}}, func(encrypter *crypto.Encrypter) {
		_, _, _ = encrypter.DecryptRawWithRotation(nil)
	})
}

// TestDecryptFallbackPanicHasNoCompletedMetrics checks fallback unwinding is
// neither a completed primary-key failure nor a successful logical decrypt.
func TestDecryptFallbackPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	drivers := []crypto.Driver{failedCrypto{err: crypto.ErrInvalidCiphertext}, panickingCrypto{}}

	assertCryptoPanicUnobserved(t, driverPanic, drivers, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.Decrypt[string](nil)
	})
}

// TestDecryptWithRotationFallbackPanicHasNoCompletedMetrics checks the typed
// rotation entry point preserves a fallback panic without recording an outcome.
func TestDecryptWithRotationFallbackPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	drivers := []crypto.Driver{failedCrypto{err: crypto.ErrInvalidCiphertext}, panickingCrypto{}}

	assertCryptoPanicUnobserved(t, driverPanic, drivers, func(encrypter *crypto.Encrypter) {
		_, _, _ = encrypter.DecryptWithRotation[string](nil)
	})
}

// TestDecryptRawFallbackPanicHasNoCompletedMetrics checks the raw wrapper does
// not publish any logical outcome when a fallback driver panics.
func TestDecryptRawFallbackPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	drivers := []crypto.Driver{failedCrypto{err: crypto.ErrInvalidCiphertext}, panickingCrypto{}}

	assertCryptoPanicUnobserved(t, driverPanic, drivers, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.DecryptRaw(nil)
	})
}

// TestDecryptRawWithRotationFallbackPanicHasNoCompletedMetrics checks fallback
// unwinding cannot be mistaken for a completed current-key success.
func TestDecryptRawWithRotationFallbackPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	drivers := []crypto.Driver{failedCrypto{err: crypto.ErrInvalidCiphertext}, panickingCrypto{}}

	assertCryptoPanicUnobserved(t, driverPanic, drivers, func(encrypter *crypto.Encrypter) {
		_, _, _ = encrypter.DecryptRawWithRotation(nil)
	})
}

// TestEncryptCodecPanicHasNoCompletedMetrics checks JSON encoding unwinding.
func TestEncryptCodecPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	assertCryptoPanicUnobserved(t, codecPanic, []crypto.Driver{panickingCrypto{}}, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.Encrypt(panickingCodec{})
	})
}

// TestDecryptCodecPanicHasNoCompletedMetrics checks a decoding panic invalidates
// the whole typed operation even after the primary driver successfully decrypts.
func TestDecryptCodecPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	drivers := []crypto.Driver{successfulDriver{plaintext: []byte(`{}`)}}

	assertCryptoPanicUnobserved(t, codecPanic, drivers, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.Decrypt[panickingCodec](nil)
	})
}

// TestDecryptWithRotationCodecPanicHasNoCompletedMetrics checks primary-key
// success cannot hide a subsequent application-owned decoding panic.
func TestDecryptWithRotationCodecPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	drivers := []crypto.Driver{successfulDriver{plaintext: []byte(`{}`)}}

	assertCryptoPanicUnobserved(t, codecPanic, drivers, func(encrypter *crypto.Encrypter) {
		_, _, _ = encrypter.DecryptWithRotation[panickingCodec](nil)
	})
}

// TestDecryptFallbackCodecPanicHasNoCompletedMetrics rejects a successful
// previous-key observation when decoding its plaintext panics.
func TestDecryptFallbackCodecPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	drivers := []crypto.Driver{failedCrypto{err: crypto.ErrInvalidCiphertext}, successfulDriver{plaintext: []byte(`{}`)}}

	assertCryptoPanicUnobserved(t, codecPanic, drivers, func(encrypter *crypto.Encrypter) {
		_, _ = encrypter.Decrypt[panickingCodec](nil)
	})
}

// TestDecryptWithRotationFallbackCodecPanicHasNoCompletedMetrics checks fallback
// success is not observed if application decoding subsequently panics.
func TestDecryptWithRotationFallbackCodecPanicHasNoCompletedMetrics(t *testing.T) {
	t.Parallel()

	drivers := []crypto.Driver{failedCrypto{err: crypto.ErrInvalidCiphertext}, successfulDriver{plaintext: []byte(`{}`)}}

	assertCryptoPanicUnobserved(t, codecPanic, drivers, func(encrypter *crypto.Encrypter) {
		_, _, _ = encrypter.DecryptWithRotation[panickingCodec](nil)
	})
}

// TestCryptoMetricsCountTypedAndRawOnce verifies complete logical operations
// without duplicated nested records or synthetic root traces.
func TestCryptoMetricsCountTypedAndRawOnce(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)

	encrypter, err := crypto.New(driver{}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(metrics)
	})

	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := encrypter.Encrypt("private-plaintext")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := encrypter.Decrypt[string](ciphertext); err != nil {
		t.Fatal(err)
	}

	if _, _, err := encrypter.DecryptWithRotation[string](ciphertext); err != nil {
		t.Fatal(err)
	}

	raw, err := encrypter.EncryptRaw([]byte("private-plaintext"))

	if err != nil {
		t.Fatal(err)
	}

	if _, err := encrypter.DecryptRaw(raw); err != nil {
		t.Fatal(err)
	}

	if _, _, err := encrypter.DecryptRawWithRotation(raw); err != nil {
		t.Fatal(err)
	}

	fake.AssertCounter(t, "arkoren.crypto.operations", 6)
	fake.AssertCounter(t, "arkoren.crypto.operations", 4, telemetry.String("arkoren.crypto.operation", "decrypt"))
	fake.AssertHistogram(t, "arkoren.crypto.operation.duration", 6)
	fake.AssertSpanCount(t, 0)

	if strings.Contains(fmt.Sprint(fake.Metrics()), "private-plaintext") {
		t.Fatal("plaintext leaked to metrics")
	}
}

// TestCryptoMetricsFallbackIsOneSuccess checks that primary and intermediate
// key failures disappear from the final successful logical decrypt outcome.
func TestCryptoMetricsFallbackIsOneSuccess(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)

	encrypter, err := crypto.New(keyedDriver{key: "primary"}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(metrics).Previous(keyedDriver{key: "wrong"}, keyedDriver{key: "old"})
	})

	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := (keyedDriver{key: "old"}).Encrypt([]byte(`"private-value"`))

	if err != nil {
		t.Fatal(err)
	}

	value, previous, err := encrypter.DecryptWithRotation[string](ciphertext)

	if err != nil || !previous || value != "private-value" {
		t.Fatalf("decrypt = %q, %v, %v", value, previous, err)
	}

	if _, previous, err := encrypter.DecryptRawWithRotation(ciphertext); err != nil || !previous {
		t.Fatalf("raw decrypt = %v, %v", previous, err)
	}

	fake.AssertCounter(t, "arkoren.crypto.operations", 2, telemetry.String("arkoren.crypto.outcome", "previous_key"), telemetry.String("error.type", ""))
	fake.AssertHistogram(t, "arkoren.crypto.operation.duration", 2)
	fake.AssertSpanCount(t, 0)
}

// failedCrypto returns a configured error from encryption and decryption.
// Different errors for primary and fallback expose error-precedence regressions.
type failedCrypto struct {
	// err remains visible to callers but its text must not appear in metrics.
	err error
}

// Encrypt returns the configured failure without observing its input.
func (driver failedCrypto) Encrypt([]byte) ([]byte, error) {
	return nil, driver.err
}

// Decrypt returns the configured failure without observing its input.
func (driver failedCrypto) Decrypt([]byte) ([]byte, error) {
	return nil, driver.err
}

// TestCryptoMetricsAllKeysFailPreservesPrimary verifies one final failure,
// original error identity, bounded sentinel labels and native-error redaction.
func TestCryptoMetricsAllKeysFailPreservesPrimary(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	primary := fmt.Errorf("private-native-error: %w", crypto.ErrInvalidCiphertext)

	encrypter, err := crypto.New(failedCrypto{err: primary}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(metrics).Previous(failedCrypto{err: crypto.ErrEncrypterClosed})
	})

	if err != nil {
		t.Fatal(err)
	}

	_, previous, err := encrypter.DecryptRawWithRotation([]byte("private-ciphertext"))

	if !errors.Is(err, primary) || previous {
		t.Fatalf("decrypt = %v, %v", previous, err)
	}

	fake.AssertCounter(t, "arkoren.crypto.operations", 1, telemetry.String("error.type", "crypto.ErrInvalidCiphertext"), telemetry.String("arkoren.crypto.outcome", "error"))

	if strings.Contains(fmt.Sprint(fake.Metrics()), "private-") {
		t.Fatal("ciphertext or native error leaked")
	}
}

// TestCryptoMetricsIncludeCodecFailures verifies encoding failures and typed
// decoding failures, including after a previous key succeeds, count as errors.
func TestCryptoMetricsIncludeCodecFailures(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)

	encrypter, err := crypto.New(keyedDriver{key: "primary"}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(metrics).Previous(successfulDriver{plaintext: []byte("not-json")})
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := encrypter.Encrypt(make(chan int)); err == nil {
		t.Fatal("Encrypt accepted a channel")
	}

	if _, err := encrypter.Decrypt[string]([]byte("legacy")); err == nil {
		t.Fatal("Decrypt accepted invalid JSON")
	}

	if _, previous, err := encrypter.DecryptWithRotation[string]([]byte("legacy")); err == nil || !previous {
		t.Fatalf("decode failure = %v, %v", previous, err)
	}

	fake.AssertCounter(t, "arkoren.crypto.operations", 3, telemetry.String("arkoren.crypto.outcome", "error"), telemetry.String("error.type", "error"))
	fake.AssertHistogram(t, "arkoren.crypto.operation.duration", 3)
}

// TestCryptoMetricsEncryptionFailure classifies stopped drivers without changing
// their error identity or recording native error descriptions.
func TestCryptoMetricsEncryptionFailure(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)

	encrypter, err := crypto.New(failedCrypto{err: crypto.ErrEncrypterClosed}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(metrics)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := encrypter.EncryptRaw(nil); !errors.Is(err, crypto.ErrEncrypterClosed) {
		t.Fatalf("EncryptRaw = %v", err)
	}

	fake.AssertCounter(t, "arkoren.crypto.operations", 1, telemetry.String("error.type", "crypto.ErrEncrypterClosed"))
}

// TestCryptoMetricsUnknownFailureHasFixedClass checks arbitrary backend errors
// retain identity for callers but cannot create unbounded telemetry labels.
func TestCryptoMetricsUnknownFailureHasFixedClass(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	want := errors.New("private-driver-detail")

	encrypter, err := crypto.New(failedCrypto{err: want}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(metrics)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := encrypter.Encrypt("private-input"); !errors.Is(err, want) {
		t.Fatalf("Encrypt = %v", err)
	}

	fake.AssertCounter(t, "arkoren.crypto.operations", 1, telemetry.String("error.type", "error"))

	if strings.Contains(fmt.Sprint(fake.Metrics()), "private-") {
		t.Fatal("input or native error leaked")
	}
}

// TestCryptoMetricsSnapshotAndConcurrentResolution checks published settings
// detach from retained drafts and cold instrument lookup remains race-free.
func TestCryptoMetricsSnapshotAndConcurrentResolution(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	var retained *crypto.Builder

	encrypter, err := crypto.New(driver{}, func(encrypter *crypto.Builder) {
		retained = encrypter

		if encrypter.Telemetry(metrics) != encrypter {
			t.Fatal("Telemetry returned another draft")
		}
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Telemetry(telemetry.New(nil))
	var workers sync.WaitGroup

	for range 20 {
		workers.Go(func() {
			if _, err := encrypter.EncryptRaw(nil); err != nil {
				t.Error(err)
			}
		})
	}

	workers.Wait()

	fake.AssertCounter(t, "arkoren.crypto.operations", 20)
}

// TestCryptoMetricsFollowDefault checks per-call resolution and explicit disable
// without running concurrently with other global-default tests.
func TestCryptoMetricsFollowDefault(t *testing.T) {
	old := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(old)
	})

	first, second := telemetrytest.New(), telemetrytest.New()
	one, two := first.Telemetry(t), second.Telemetry(t)

	encrypter, err := crypto.New(driver{}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(one).Telemetry(nil)
	})

	if err != nil {
		t.Fatal(err)
	}

	disabled, err := crypto.New(driver{}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(telemetry.New(nil))
	})

	if err != nil {
		t.Fatal(err)
	}

	telemetry.SetDefault(one)

	if _, err := encrypter.EncryptRaw(nil); err != nil {
		t.Fatal(err)
	}

	telemetry.SetDefault(two)

	if _, err := encrypter.EncryptRaw(nil); err != nil {
		t.Fatal(err)
	}

	if _, err := disabled.EncryptRaw(nil); err != nil {
		t.Fatal(err)
	}

	first.AssertCounter(t, "arkoren.crypto.operations", 1)
	second.AssertCounter(t, "arkoren.crypto.operations", 1)
}

// TestCryptoDisabledMetricsAllocateNothing uses a no-allocation error path to
// isolate instrumentation cost from cipher buffers. It runs serially because
// testing.AllocsPerRun cannot execute alongside parallel tests.
func TestCryptoDisabledMetricsAllocateNothing(t *testing.T) {
	encrypter, err := crypto.New(failedCrypto{err: crypto.ErrEncrypterClosed}, func(encrypter *crypto.Builder) {
		encrypter.Telemetry(telemetry.New(nil))
	})

	if err != nil {
		t.Fatal(err)
	}

	allocations := testing.AllocsPerRun(100, func() {
		if _, _, err := encrypter.DecryptRawWithRotation(nil); !errors.Is(err, crypto.ErrEncrypterClosed) {
			t.Fatal(err)
		}
	})

	if allocations != 0 {
		t.Fatalf("disabled allocations = %v", allocations)
	}
}
