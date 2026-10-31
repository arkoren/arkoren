package hatchetworkflow

import (
	"cmp"
	"fmt"
	"math"
	"time"

	"arkoren.dev/core/service/workflow"
)

// maxNativeDuration is the largest whole-second duration representable without
// overflow. Native timeout rendering rounds upward to this granularity.
const maxNativeDuration = time.Duration(math.MaxInt64/int64(time.Second)) * time.Second

// maxBackoffFactor is the pinned engine's registration limit. Checking it before
// float32 conversion also rejects an infinite factor accepted by a core policy.
const maxBackoffFactor = 1000

// validateRegistry rejects policies the native wire cannot represent before any
// host registration is retained. Valid core definitions are not silently changed
// by integer narrowing or overflow in timeout/idempotency arithmetic.
func validateRegistry(registry *workflow.Registry, config settings) error {
	for _, handler := range registry.Workflows() {
		if err := validateWorkflow(handler, config); err != nil {
			return err
		}
	}

	for _, handler := range registry.Steps() {
		if err := validateStep(handler); err != nil {
			return err
		}
	}

	return nil
}

// validateWorkflow checks both independently owned native durations: execution
// timeout (including its margin) and trigger-relative idempotency retention.
func validateWorkflow(handler workflow.WorkflowHandler, config settings) error {
	options := handler.Options

	if options.MaxDuration > maxNativeDuration-config.TimeoutMargin {
		return fmt.Errorf("workflow %s native execution timeout overflows: %w", handler.Name, workflow.ErrUnsupported)
	}

	retention := cmp.Or(options.Retention, config.Lookback)

	if options.MaxDuration > time.Duration(math.MaxInt64)-retention {
		return fmt.Errorf("workflow %s native retention overflows: %w", handler.Name, workflow.ErrUnsupported)
	}

	return nil
}

// validateStep checks the native int32 retry/backoff fields and whole-second
// timeout before registration. The pinned engine requires a finite execution
// timeout, so unlimited core policies fail explicitly instead of being capped.
func validateStep(handler workflow.StepHandler) error {
	options := handler.Options

	if options.Timeout == 0 {
		return fmt.Errorf("step %s unlimited native timeout: %w", handler.Name, workflow.ErrUnsupported)
	}

	if options.Attempts-1 > math.MaxInt32 {
		return fmt.Errorf("step %s native retry count overflows: %w", handler.Name, workflow.ErrUnsupported)
	}

	if options.Timeout > maxNativeDuration-stepGrace {
		return fmt.Errorf("step %s native timeout overflows: %w", handler.Name, workflow.ErrUnsupported)
	}

	if options.Max > time.Duration(math.MaxInt32)*time.Second || options.Factor > maxBackoffFactor {
		return fmt.Errorf("step %s native backoff is out of range: %w", handler.Name, workflow.ErrUnsupported)
	}

	return nil
}
