package webhook

import "arkoren.dev/core/event"

// RejectionReason classifies a 4xx receipt without exposing the request or cause.
// [Handler] emits only the constants below. The empty zero value is unspecified;
// it is not normalized to another reason. Its string and JSON representations
// preserve the category bytes; use an explicit conversion for string-only APIs.
// It does not classify opaque provider keys such as [Delivery.Type].
//
// Example:
//
//	reason := webhook.RejectionReasonBodyTooLarge
//	rejected := webhook.RejectedEvent{Reason: reason, Status: http.StatusRequestEntityTooLarge}
//	logger.WarnContext(ctx, "webhook rejected", "reason", string(rejected.Reason))
type RejectionReason string

const (
	// RejectionReasonBodyTooLarge identifies a body over the effective size
	// limit. The receiver commits the native 413 body-limit problem, not the
	// generic webhook rejection representation.
	//
	// Example:
	//	if rejected.Reason == webhook.RejectionReasonBodyTooLarge {
	//		logger.WarnContext(ctx, "webhook body exceeds limit", "label", rejected.Label)
	//	}
	RejectionReasonBodyTooLarge RejectionReason = "body_too_large"

	// RejectionReasonUnreadableBody identifies a malformed body read, answered
	// with 400. Cancellation and timeout are failures at [FailureStageRead], not
	// malformed-request rejections.
	//
	// Example:
	//	if rejected.Reason == webhook.RejectionReasonUnreadableBody {
	//		logger.WarnContext(ctx, "malformed webhook body", "label", rejected.Label)
	//	}
	RejectionReasonUnreadableBody RejectionReason = "unreadable_body"

	// RejectionReasonVerify identifies a verifier error classified by
	// [Builder.Reject] or [ErrRejected]. The selected 4xx is [RejectedEvent.Status];
	// an unclassified verifier error instead reports [FailureStageVerify].
	//
	// Example:
	//	if rejected.Reason == webhook.RejectionReasonVerify {
	//		logger.WarnContext(ctx, "webhook verification rejected", "status", rejected.Status)
	//	}
	RejectionReasonVerify RejectionReason = "verify"
)

// FailureStage identifies the processing step that could not complete, answered
// with 503. [Handler] emits only the constants below. The empty zero value is
// unspecified and stays empty, including in JSON; there is no default stage.
// The stage describes a local observation, not a provider retry guarantee.
//
// Example:
//
//	failed := webhook.FailedEvent{Stage: webhook.FailureStageAccept}
//	logger.ErrorContext(ctx, "webhook failed", "stage", string(failed.Stage))
type FailureStage string

const (
	// FailureStageRead identifies a body read interrupted by cancellation or a
	// timeout. Verification has not run, so this is not a signature failure.
	//
	// Example:
	//	if failed.Stage == webhook.FailureStageRead {
	//		logger.ErrorContext(ctx, "webhook read interrupted", "label", failed.Label)
	//	}
	FailureStageRead FailureStage = "read"

	// FailureStageVerify identifies an unclassified verifier error. Errors
	// matched by [Builder.Reject] or [ErrRejected] are rejections instead.
	//
	// Example:
	//	if failed.Stage == webhook.FailureStageVerify {
	//		logger.ErrorContext(ctx, "webhook verifier unavailable", "label", failed.Label)
	//	}
	FailureStageVerify FailureStage = "verify"

	// FailureStageDecode identifies an event decoder or [Event.Map] conversion
	// error. An authenticated [ErrSkip] from either step is acknowledged instead.
	//
	// Example:
	//	if failed.Stage == webhook.FailureStageDecode {
	//		logger.ErrorContext(ctx, "webhook decoding failed", "label", failed.Label)
	//	}
	FailureStageDecode FailureStage = "decode"

	// FailureStageAccept identifies any [AcceptFunc] error, including [ErrSkip]
	// and permanent queue errors. Acceptance may already have committed work;
	// this category does not imply rollback or safe replay.
	//
	// Example:
	//	if failed.Stage == webhook.FailureStageAccept {
	//		logger.ErrorContext(ctx, "webhook acceptance failed", "label", failed.Label)
	//	}
	FailureStageAccept FailureStage = "accept"
)

// IgnoreReason explains why an authenticated delivery was acknowledged without
// a bound handler. [Handler] currently emits only [IgnoreReasonUnbound]. The
// empty zero value is unspecified and remains empty in strings and JSON; it
// does not mean unbound. Skips and duplicates do not publish [Ignored].
//
// Example:
//
//	ignored := webhook.IgnoredEvent{Reason: webhook.IgnoreReasonUnbound}
//	logger.DebugContext(ctx, "webhook ignored", "reason", string(ignored.Reason))
type IgnoreReason string

const (
	// IgnoreReasonUnbound identifies an authenticated provider event type with
	// no bound handler. It answers 200 without decoding or remembering its ID.
	//
	// Example:
	//	if ignored.Reason == webhook.IgnoreReasonUnbound {
	//		logger.DebugContext(ctx, "unbound webhook acknowledged", "label", ignored.Label)
	//	}
	IgnoreReasonUnbound IgnoreReason = "unbound"
)

// RejectedEvent is the payload of [Rejected]. It carries a label and categories,
// never a body, header, provider event ID, event type, or cause. Labels remain
// application-controlled and event contexts retain request context values;
// these omissions do not sanitize those values or returned receipt errors.
//
// Example:
//
//	unsubscribe, err := bus.Listen(webhook.Rejected, func(ctx context.Context, rejected webhook.RejectedEvent) error {
//		logger.WarnContext(ctx, "webhook rejected", "label", rejected.Label, "reason", rejected.Reason)
//
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type RejectedEvent struct {
	// Label is the application-selected [Builder.Label], empty if none. It is
	// not a provider identifier and must not contain credentials or personal data.
	//
	// Example:
	//	logger.WarnContext(ctx, "webhook rejected", "label", rejected.Label)
	Label string

	// Reason is the bounded rejection category. See [RejectionReason] for its
	// values; empty means unspecified in a manually constructed payload.
	//
	// Example:
	//	if rejected.Reason == webhook.RejectionReasonVerify {
	//		logger.WarnContext(ctx, "webhook verification rejected", "status", rejected.Status)
	//	}
	Reason RejectionReason

	// Status is the selected 4xx status, including a mapped [Builder.Reject]
	// status or the native 413. It is zero in a zero payload, not a default 400.
	//
	// Example:
	//	logger.WarnContext(ctx, "webhook rejected", "status", rejected.Status)
	Status int
}

// FailedEvent is the payload of [Failed]. It carries no cause, body, header,
// provider event ID or event type.
//
// Example:
//
//	unsubscribe, err := bus.Listen(webhook.Failed, func(ctx context.Context, failed webhook.FailedEvent) error {
//		logger.ErrorContext(ctx, "webhook failed", "label", failed.Label, "stage", failed.Stage)
//
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type FailedEvent struct {
	// Label is the application-selected [Builder.Label], empty if none. Keep it
	// low-cardinality and free of credentials; it is not a provider event key.
	//
	// Example:
	//	logger.ErrorContext(ctx, "webhook failed", "label", failed.Label)
	Label string

	// Stage is where processing failed. See [FailureStage] for its values;
	// empty is unspecified, not an implicit verification failure.
	//
	// Example:
	//	logger.ErrorContext(ctx, "webhook failed", "stage", string(failed.Stage))
	Stage FailureStage
}

// IgnoredEvent is the payload of [Ignored]. It deliberately carries no event
// type, because the type is supplied by the provider.
//
// Example:
//
//	unsubscribe, err := bus.Listen(webhook.Ignored, func(ctx context.Context, ignored webhook.IgnoredEvent) error {
//		logger.DebugContext(ctx, "webhook ignored", "label", ignored.Label, "reason", ignored.Reason)
//
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type IgnoredEvent struct {
	// Label is the application-selected [Builder.Label], empty if none. It names
	// the receiver for diagnostics, not the ignored event or deduplication scope.
	//
	// Example:
	//	logger.DebugContext(ctx, "webhook ignored", "label", ignored.Label)
	Label string

	// Reason is why the authenticated delivery was acknowledged without
	// handling. See [IgnoreReason]; empty is unspecified, not an unbound default.
	//
	// Example:
	//	if ignored.Reason == webhook.IgnoreReasonUnbound {
	//		logger.DebugContext(ctx, "unbound webhook acknowledged", "label", ignored.Label)
	//	}
	Reason IgnoreReason
}

// Rejected is published when a delivery is answered with a 4xx status. It is
// published after the outcome is known; a listener cannot change the response.
//
// Example:
//
//	unsubscribe, err := bus.Listen(webhook.Rejected, onRejected)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Rejected = event.NewTopic[RejectedEvent]("http.webhook.rejected")

// Failed is published when a delivery is answered 503 because it could not be
// processed now. Whether the provider retries is the provider's policy.
//
// Example:
//
//	unsubscribe, err := bus.Listen(webhook.Failed, onFailed)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Failed = event.NewTopic[FailedEvent]("http.webhook.failed")

// Ignored is published when an authenticated delivery is acknowledged with 200
// because no handler is bound for its event type.
//
// Example:
//
//	unsubscribe, err := bus.Listen(webhook.Ignored, onIgnored)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Ignored = event.NewTopic[IgnoredEvent]("http.webhook.ignored")
