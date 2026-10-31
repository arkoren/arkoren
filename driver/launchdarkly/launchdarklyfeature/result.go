package launchdarklyfeature

import (
	"cmp"
	"fmt"

	"github.com/launchdarkly/go-sdk-common/v3/ldreason"

	"arkoren.dev/core/service/feature"
)

// evaluationError maps the error kind of an ERROR reason to the contract's
// sentinel, keeping the SDK's error, when it gave one, in the chain. MALFORMED_FLAG,
// EXCEPTION, and kinds without a sentinel produce a plain error naming the kind.
func evaluationError(key string, kind ldreason.EvalErrorKind, err error) error {
	var sentinel error

	switch kind {
	case ldreason.EvalErrorFlagNotFound:
		sentinel = feature.ErrNotFound
	case ldreason.EvalErrorWrongType:
		sentinel = feature.ErrTypeMismatch
	case ldreason.EvalErrorClientNotReady:
		sentinel = feature.ErrNotReady
	case ldreason.EvalErrorUserNotSpecified:
		sentinel = feature.ErrInvalidSubject
	default:
		if err == nil {
			return fmt.Errorf("launchdarkly: evaluate %q: %s", key, cmp.Or(kind, "unknown error"))
		}

		return fmt.Errorf("launchdarkly: evaluate %q: %s: %w", key, cmp.Or(kind, "unknown error"), err)
	}

	if err == nil {
		return fmt.Errorf("launchdarkly: evaluate %q: %w", key, sentinel)
	}

	return fmt.Errorf("launchdarkly: evaluate %q: %w: %w", key, sentinel, err)
}

// reasonOf maps a successful evaluation's reason to the contract's. OFF and
// PREREQUISITE_FAILED are [feature.ReasonDisabled]; TARGET_MATCH is
// [feature.ReasonTargetingMatch], as is RULE_MATCH; FALLTHROUGH is
// [feature.ReasonDefault]. A rule match or fallthrough that is part of an
// experiment is a percentage rollout and maps to [feature.ReasonSplit]; the
// SDK reports no other rollout information.
func reasonOf(reason ldreason.EvaluationReason) feature.Reason {
	switch reason.GetKind() {
	case ldreason.EvalReasonOff, ldreason.EvalReasonPrerequisiteFailed:
		return feature.ReasonDisabled
	case ldreason.EvalReasonTargetMatch:
		return feature.ReasonTargetingMatch
	case ldreason.EvalReasonRuleMatch:
		if reason.IsInExperiment() {
			return feature.ReasonSplit
		}

		return feature.ReasonTargetingMatch
	case ldreason.EvalReasonFallthrough:
		if reason.IsInExperiment() {
			return feature.ReasonSplit
		}

		return feature.ReasonDefault
	default:
		return feature.ReasonUnknown
	}
}
