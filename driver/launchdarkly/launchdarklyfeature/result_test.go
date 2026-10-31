package launchdarklyfeature

import (
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/feature"

	"github.com/launchdarkly/go-sdk-common/v3/ldreason"
)

// TestEvaluationErrorExceptionIsPlainErrorNamingKind preserves the SDK failure
// kind for exceptions that do not have a dedicated contract sentinel.
func TestEvaluationErrorExceptionIsPlainErrorNamingKind(t *testing.T) {
	t.Parallel()

	cause := errors.New("store failed")

	err := evaluationError("f", ldreason.EvalErrorException, cause)

	if !strings.Contains(err.Error(), "EXCEPTION") || !errors.Is(err, cause) {
		t.Fatalf("evaluationError() = %v, want a plain error naming EXCEPTION wrapping the cause", err)
	}

	for _, sentinel := range []error{feature.ErrNotFound, feature.ErrTypeMismatch, feature.ErrNotReady, feature.ErrInvalidSubject} {
		if errors.Is(err, sentinel) {
			t.Errorf("evaluationError() matches %v, want a plain error", sentinel)
		}
	}
}

// TestEvaluationErrorUserNotSpecifiedIsInvalidSubject maps missing SDK identity
// to the subject category so callers can correct their request.
func TestEvaluationErrorUserNotSpecifiedIsInvalidSubject(t *testing.T) {
	t.Parallel()

	err := evaluationError("f", ldreason.EvalErrorUserNotSpecified, nil)

	if !errors.Is(err, feature.ErrInvalidSubject) {
		t.Fatalf("evaluationError() = %v, want ErrInvalidSubject", err)
	}
}
