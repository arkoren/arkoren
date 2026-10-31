package featuretest

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/service/feature"
)

// invalidKinds exercises the raw driver boundary, not the typed facade. Empty
// and unknown kinds must fail even when normal evaluation would return no value;
// cancellation wins over that validation. Valid reads establish fixture
// propagation before checking paths that could otherwise hide an invalid kind.
func invalidKinds(t *testing.T, s suite) {
	s.sub(t, "MissingFlag", func(t *testing.T, h Harness, key string) {
		wantInvalidKinds(t, h.Driver(), feature.Request{Key: key})
	})
	s.sub(t, "StoredDefault", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: true})

		s.wantPlain(t, h, key, feature.KindBool, feature.Subject{}, true)
		wantInvalidKinds(t, h.Driver(), feature.Request{Key: key})
	})
	s.sub(t, "DisabledFlag", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Value: true, Disabled: true})

		result, err := s.read(t, h, key, feature.KindBool, feature.Subject{}, func(r feature.Result, err error) bool {
			return err == nil && r.Reason == feature.ReasonDisabled
		})

		if err != nil || result.Reason != feature.ReasonDisabled {
			t.Fatalf("fixture not disabled: %#v, %v", result, err)
		}

		wantInvalidKinds(t, h.Driver(), feature.Request{Key: key})
	})
	s.sub(t, "UnmatchedOverrideDefaultsToNil", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Overrides: map[string]any{"member": true}})

		s.wantValue(t, h, key, feature.KindBool, feature.Subject{Key: "member"}, true)
		wantInvalidKinds(t, h.Driver(), feature.Request{Key: key})
	})
	s.sub(t, "MatchedOverride", func(t *testing.T, h Harness, key string) {
		h.Set(t, Flag{Key: key, Overrides: map[string]any{"member": true}})

		subject := feature.Subject{Key: "member"}

		s.wantValue(t, h, key, feature.KindBool, subject, true)
		wantInvalidKinds(t, h.Driver(), feature.Request{Key: key, Subject: subject})
	})
	s.sub(t, "CanceledBeforeKindValidation", func(t *testing.T, h Harness, key string) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		for _, kind := range []feature.Kind{"", "future-kind"} {
			_, err := h.Driver().Evaluate(ctx, feature.Request{Key: key, Kind: kind})

			if !errors.Is(err, context.Canceled) {
				t.Errorf("Evaluate(canceled, kind=%q) = %v, want context.Canceled", kind, err)
			}
		}
	})
}

// wantInvalidKinds checks immediate rejection without propagation retries.
// Kind validation is local and cannot depend on backend state or readiness.
func wantInvalidKinds(t *testing.T, driver feature.Driver, request feature.Request) {
	t.Helper()

	for _, kind := range []feature.Kind{"", "future-kind"} {
		request.Kind = kind

		_, err := driver.Evaluate(t.Context(), request)

		if !errors.Is(err, feature.ErrTypeMismatch) {
			t.Errorf("Evaluate(kind=%q) = %v, want ErrTypeMismatch", kind, err)
		}
	}
}
