package hatchetworkflow

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"time"

	"arkoren.dev/core/service/workflow"
)

// Signal sends v to the verified physical execution currently resolved for id.
// Both the event scope and first-value marker include that engine workflow-run
// ID, so reusing a logical ID after its TTL cannot reuse an earlier decision.
// Concurrent senders share one first value; marker collisions are verified for
// namespace, physical owner, signal key, and protocol before any event is sent.
// The running check is best effort: the execution may end before delivery.
// Legacy workflow inputs fail closed with [workflow.ErrNotFound].
//
// Example:
//
//	err := driver.Signal(ctx, "order", "order-42", "approved", payload)
//	if errors.Is(err, workflow.ErrNotFound) {
//		// The execution is missing, finished, or uses a legacy protocol.
//	}
func (driver *Driver) Signal(ctx context.Context, name, id, signal string, v []byte) error {
	be, err := driver.backend()

	if err != nil {
		return fmt.Errorf("signal %s %s: %w", name, id, err)
	}

	runID, err := driver.resolve(ctx, be, name, id)

	if err != nil {
		return err
	}

	key := driver.runKey(name, id)

	detail, err := driver.details(ctx, be, key, runID)

	if err != nil {
		return fmt.Errorf("signal %s %s: %w", name, id, err)
	}

	if err := validateSignalExecution(detail, name, id); err != nil {
		return err
	}

	// Hatchet delivers the newest of several pending events, but a signal
	// resolves to its first value. The first sender stores the value in a
	// marker run, unique per run and signal, and every event points to it.
	if err := driver.checkWorkflow(ctx, be, driver.signalWorkflow()); err != nil {
		return fmt.Errorf("signal %s %s %s: %w", name, id, signal, err)
	}

	scope := signalScope(key, runID)
	markerKey := signalMarkerKey(scope, signal)

	marker, err := be.mark(ctx, driver.signalWorkflow(), wire{
		Data:          string(v),
		Key:           markerKey,
		SignalVersion: signalVersion,
		ExecutionID:   runID,
	})

	if err != nil {
		return fmt.Errorf("signal %s %s %s: %w", name, id, signal, err)
	}

	if _, err := driver.firstValue(ctx, marker, markerKey, runID); err != nil {
		return err
	}

	if err := be.push(ctx, driver.signalKey(signal), wire{
		Marker:        marker,
		SignalVersion: signalVersion,
		ExecutionID:   runID,
	}, scope); err != nil {
		return fmt.Errorf("signal %s %s %s: %w", name, id, signal, err)
	}

	return nil
}

// validateSignalExecution checks signal eligibility in historical precedence:
// terminal/unknown state first, then retained JSON and finally protocol version.
// A failure occurs before marker creation or event delivery, with the same
// not-found identity used for missing or inaccessible executions.
func validateSignalExecution(detail runDetail, name, id string) error {
	if state, err := mapStatus(detail.Status); err != nil || state != workflow.Running {
		return fmt.Errorf("signal %s %s: %w: run is not running", name, id, workflow.ErrNotFound)
	}

	var input wire

	if err := json.Unmarshal(detail.Input, &input); err != nil {
		return fmt.Errorf("verify signal execution input: %w: %w", workflow.ErrNotFound, err)
	}

	if input.SignalVersion != signalVersion {
		return fmt.Errorf("signal execution uses a legacy protocol: %w", workflow.ErrNotFound)
	}

	return nil
}

// firstValue reads retained input only after proving the exact marker workflow,
// namespaced physical key, execution owner, and private protocol version. Both
// collision responses and received events pass through this check. Empty marker
// IDs fail closed rather than selecting an arbitrary marker under the key.
func (driver *Driver) firstValue(ctx context.Context, marker, key, executionID string) ([]byte, error) {
	if marker == "" || executionID == "" {
		return nil, fmt.Errorf("missing signal marker identity: %w", workflow.ErrNotFound)
	}

	be, err := driver.backend()

	if err != nil {
		return nil, err
	}

	_, input, err := driver.find(ctx, be, runIdentity{
		workflow: driver.namespace + driver.signalWorkflow(), key: key, physical: marker,
	}, time.Now().Add(max(driver.config.LookupGrace, 0)))

	if err != nil {
		return nil, fmt.Errorf("verify signal marker: %w", err)
	}

	if input.SignalVersion != signalVersion || input.ExecutionID != executionID {
		return nil, fmt.Errorf("signal marker execution mismatch: %w", workflow.ErrNotFound)
	}

	return []byte(input.Data), nil
}
