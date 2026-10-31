package feature_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// Values that must never reach telemetry.
const (
	secretSubject   = "alice@example.com"
	secretAttribute = "secret-plan-value"
	secretFlagValue = "secret-flag-value"
)

// failingDriver is a driver whose Evaluate fails with err.
type failingDriver struct {
	// err is the failure returned by the test driver.
	err error
}

// Evaluate returns the configured error.
func (driver failingDriver) Evaluate(context.Context, feature.Request) (feature.Result, error) {
	return feature.Result{}, driver.err
}

// secretSubjectValue is a subject carrying data that must stay out of telemetry.
func secretSubjectValue() feature.Subject {
	return feature.Subject{Key: secretSubject, Attributes: map[string]any{"plan": secretAttribute}}
}

// newTraced returns Flags over driver recording into the returned fake.
func newTraced(t *testing.T, driver feature.Driver, opts ...func(*feature.Builder)) (*feature.Flags, *telemetrytest.Fake) {
	t.Helper()

	fake := telemetrytest.New()

	return feature.New(driver, append([]func(*feature.Builder){func(flags *feature.Builder) {
		flags.Telemetry(fake.Telemetry(t))
	}}, opts...)...), fake
}

// TestEvaluationSpanAndMetrics checks provider, flag, and result attributes on client telemetry.
func TestEvaluationSpanAndMetrics(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	checkout := feature.NewFlag("checkout", false)

	if err := driver.Set(checkout, true); err != nil {
		t.Fatal(err)
	}

	flags, fake := newTraced(t, driver, func(flags *feature.Builder) {
		flags.Name("memory")
	})

	if _, err := flags.Value(t.Context(), checkout, feature.Subject{}); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "feature evaluate",
		telemetry.String("feature_flag.key", "checkout"),
		telemetry.String("feature_flag.provider.name", "memory"),
		telemetry.String("feature_flag.result.reason", "static"))
	fake.AssertCounter(t, "arkoren.feature.evaluations", 1,
		telemetry.String("feature_flag.key", "checkout"),
		telemetry.String("feature_flag.provider.name", "memory"),
		telemetry.String("feature_flag.result.reason", "static"),
		telemetry.String("error.type", ""))
	fake.AssertHistogram(t, "arkoren.feature.evaluation.duration", 1,
		telemetry.String("feature_flag.key", "checkout"),
		telemetry.String("feature_flag.provider.name", "memory"))

	span, _ := fake.Span("feature evaluate")

	if span.Kind != telemetry.Internal || span.Scope != "arkoren.dev/core/service/feature" || span.Status == telemetry.Error {
		t.Errorf("kind, scope, status = %v, %q, %v; want Internal, feature scope, not error", span.Kind, span.Scope, span.Status)
	}

	if _, found := span.Attr("feature_flag.result.variant"); found {
		t.Error("empty variant was recorded")
	}

	if _, found := span.Attr("error.type"); found {
		t.Error("successful evaluation has error.type")
	}
}

// TestEvaluationRecordsVariantOnSpanOnly checks variants do not increase metric cardinality.
func TestEvaluationRecordsVariantOnSpanOnly(t *testing.T) {
	t.Parallel()

	flags, fake := newTraced(t, &stub{
		result: feature.Result{
			Value:   true,
			Variant: "on",
			Reason:  feature.ReasonTargetingMatch,
		},
	})

	if _, err := flags.Value(t.Context(), feature.NewFlag("checkout", false), feature.Subject{}); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "feature evaluate",
		telemetry.String("feature_flag.result.variant", "on"),
		telemetry.String("feature_flag.result.reason", "targeting_match"),
		telemetry.String("feature_flag.provider.name", feature.DefaultName))

	for _, metric := range fake.Metrics() {
		for _, point := range metric.Points {
			for _, label := range point.Values {
				if label == "on" {
					t.Errorf("metric %s has the variant as a label", metric.Name)
				}
			}
		}
	}
}

// TestEvaluationWithNoValueRecordsDriverReason checks fallback values retain the driver's reason.
func TestEvaluationWithNoValueRecordsDriverReason(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	checkout := feature.NewFlag("checkout", false)

	if err := driver.Set(checkout, true); err != nil {
		t.Fatal(err)
	}

	driver.Disable("checkout")

	flags, fake := newTraced(t, driver)

	if _, err := flags.Value(t.Context(), checkout, feature.Subject{}); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "feature evaluate", telemetry.String("feature_flag.result.reason", "disabled"))
	fake.AssertCounter(t, "arkoren.feature.evaluations", 1, telemetry.String("feature_flag.result.reason", "disabled"))
}

// TestNotFoundErrorType checks missing flags use the stable contract error label.
func TestNotFoundErrorType(t *testing.T) {
	t.Parallel()

	flags, fake := newTraced(t, memoryfeature.New())

	if _, err := flags.Value(t.Context(), feature.NewFlag("absent", false), feature.Subject{}); !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}

	assertFailure(t, fake, "absent", "feature.ErrNotFound")
}

// TestNotReadyErrorType checks readiness failures use the stable contract error label.
func TestNotReadyErrorType(t *testing.T) {
	t.Parallel()

	flags, fake := newTraced(t, failingDriver{err: feature.ErrNotReady})

	if _, err := flags.Value(t.Context(), feature.NewFlag("checkout", false), feature.Subject{}); !errors.Is(err, feature.ErrNotReady) {
		t.Fatalf("err = %v, want not ready", err)
	}

	assertFailure(t, fake, "checkout", "feature.ErrNotReady")
}

// TestDriverTypeMismatchErrorType checks driver-reported type errors retain their label.
func TestDriverTypeMismatchErrorType(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()

	// Simulate a provider configured with the wrong type.
	if err := driver.Set(feature.NewFlag("checkout", ""), "yes"); err != nil {
		t.Fatal(err)
	}

	flags, fake := newTraced(t, driver)

	if _, err := flags.Value(t.Context(), feature.NewFlag("checkout", false), feature.Subject{}); !errors.Is(err, feature.ErrTypeMismatch) {
		t.Fatalf("err = %v, want type mismatch", err)
	}

	assertFailure(t, fake, "checkout", "feature.ErrTypeMismatch")
}

// TestConversionTypeMismatchErrorType checks facade conversion failures are classified consistently.
func TestConversionTypeMismatchErrorType(t *testing.T) {
	t.Parallel()

	flags, fake := newTraced(t, &stub{result: feature.Result{Value: "yes", Reason: feature.ReasonStatic}})

	if _, err := flags.Value(t.Context(), feature.NewFlag("checkout", false), feature.Subject{}); !errors.Is(err, feature.ErrTypeMismatch) {
		t.Fatalf("err = %v, want type mismatch", err)
	}

	assertFailure(t, fake, "checkout", "feature.ErrTypeMismatch")
}

// TestInvalidSubjectErrorTypeSkipsDriver checks validation failure telemetry precedes driver access.
func TestInvalidSubjectErrorTypeSkipsDriver(t *testing.T) {
	t.Parallel()

	driver := &stub{}
	flags, fake := newTraced(t, driver)
	subject := feature.Subject{Attributes: map[string]any{"seats": 5}}

	if _, err := flags.Value(t.Context(), feature.NewFlag("checkout", false), subject); !errors.Is(err, feature.ErrInvalidSubject) {
		t.Fatalf("err = %v, want invalid subject", err)
	}

	assertFailure(t, fake, "checkout", "feature.ErrInvalidSubject")

	if driver.calls.Load() != 0 {
		t.Error("driver was called for an invalid subject")
	}
}

// TestOtherErrorsRecordTypeOnly guards against recording sensitive provider error messages.
func TestOtherErrorsRecordTypeOnly(t *testing.T) {
	t.Parallel()

	cause := errors.New("connection to " + secretSubject + " refused")
	flags, fake := newTraced(t, failingDriver{err: cause})

	if _, err := flags.Value(t.Context(), feature.NewFlag("checkout", false), feature.Subject{}); err == nil {
		t.Fatal("expected error")
	}

	assertFailure(t, fake, "checkout", "*errors.errorString")

	span, _ := fake.Span("feature evaluate")

	if len(span.Events) != 0 || strings.Contains(span.StatusDescription, "alice") {
		t.Errorf("error text leaked: events=%v description=%q", span.Events, span.StatusDescription)
	}
}

// TestSubjectAndValuesNeverRecorded guards against disclosing evaluation inputs and outputs.
func TestSubjectAndValuesNeverRecorded(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	plan := feature.NewFlag("plan", "")

	if err := driver.Set(plan, secretFlagValue); err != nil {
		t.Fatal(err)
	}

	if err := driver.SetFor(plan, secretSubject, secretFlagValue); err != nil {
		t.Fatal(err)
	}

	flags, fake := newTraced(t, driver)

	_, _ = flags.Value(t.Context(), plan, secretSubjectValue())
	_, _ = flags.Value(t.Context(), feature.NewFlag("absent", ""), secretSubjectValue())
	_, _ = flags.Value(t.Context(), plan, feature.Subject{
		Key: secretSubject, Attributes: map[string]any{"n": 1},
	})

	secrets := []string{"alice", secretAttribute, secretFlagValue}

	for _, span := range fake.Spans() {
		for _, secret := range secrets {
			if strings.Contains(span.Name, secret) || strings.Contains(span.StatusDescription, secret) {
				t.Errorf("span %q holds %q", span.Name, secret)
			}

			for _, attr := range span.Attrs {
				if strings.Contains(attr.Text(), secret) {
					t.Errorf("attribute %s=%q holds %q", attr.Key(), attr.Text(), secret)
				}
			}
		}
	}

	for _, metric := range fake.Metrics() {
		for _, point := range metric.Points {
			for _, label := range point.Values {
				for _, secret := range secrets {
					if strings.Contains(label, secret) {
						t.Errorf("metric %s label %q holds %q", metric.Name, label, secret)
					}
				}
			}
		}
	}
}

// TestDefaultTelemetryResolvedLazily checks telemetry installed after construction is observed.
func TestDefaultTelemetryResolvedLazily(t *testing.T) {
	flags := feature.New(memoryfeature.New())
	fake := telemetrytest.New()

	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	_, _ = flags.Value(t.Context(), feature.NewFlag("absent", false), feature.Subject{})

	fake.AssertSpan(t, "feature evaluate")
}

// TestDisabledTelemetryAllocatesNothing compares evaluation with the direct driver baseline.
func TestDisabledTelemetryAllocatesNothing(t *testing.T) {
	driver := &stub{result: feature.Result{Value: true, Reason: feature.ReasonStatic}}
	ctx := context.Background()
	flags := feature.New(driver, func(flags *feature.Builder) {
		flags.Telemetry(telemetry.New(nil))
	})

	request := feature.Request{Key: "checkout", Kind: feature.KindBool}

	traced := testing.AllocsPerRun(100, func() {
		_, _ = flags.Value(ctx, feature.NewFlag("checkout", false), feature.Subject{})
	})

	baseline := testing.AllocsPerRun(100, func() {
		_, _ = driver.Evaluate(ctx, request)
	})

	if traced != baseline {
		t.Errorf("disabled telemetry changed allocations per Value from %v to %v", baseline, traced)
	}
}

// assertFailure checks that the only evaluation of key failed with the given
// error.type on the span, the counter, and the histogram, and that the reason
// label is "error".
func assertFailure(t *testing.T, fake *telemetrytest.Fake, key, errorType string) {
	t.Helper()

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "feature evaluate",
		telemetry.String("feature_flag.key", key),
		telemetry.String("feature_flag.result.reason", "error"),
		telemetry.String("error.type", errorType))
	fake.AssertCounter(t, "arkoren.feature.evaluations", 1,
		telemetry.String("feature_flag.key", key),
		telemetry.String("feature_flag.result.reason", "error"),
		telemetry.String("error.type", errorType))
	fake.AssertHistogram(t, "arkoren.feature.evaluation.duration", 1,
		telemetry.String("feature_flag.key", key), telemetry.String("error.type", errorType))

	span, _ := fake.Span("feature evaluate")

	if span.Status != telemetry.Error {
		t.Errorf("status = %v, want error", span.Status)
	}
}

// TestInvalidFlagTelemetryUsesSentinel checks definition validation records
// one failed operation without passing the fallback to telemetry.
func TestInvalidFlagTelemetryUsesSentinel(t *testing.T) {
	t.Parallel()

	flags, fake := newTraced(t, memoryfeature.New())

	value, err := flags.Value(t.Context(), feature.NewFlag("", secretFlagValue), feature.Subject{})

	if !errors.Is(err, feature.ErrInvalidFlag) || value != secretFlagValue {
		t.Fatalf("Value = %q, %v", value, err)
	}

	assertFailure(t, fake, "", "feature.ErrInvalidFlag")
}
