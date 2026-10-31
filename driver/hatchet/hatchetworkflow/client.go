package hatchetworkflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hatchet-dev/hatchet/pkg/client" //nolint:staticcheck // SDK collision errors expose this legacy type
	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"arkoren.dev/core/service/workflow"
)

// hatchetBackend implements [backend] on a Hatchet client.
type hatchetBackend struct {
	// client provides the native connection used for engine operations.
	client *hatchet.Client
}

// trigger submits the wire input and metadata without waiting for completion.
// SDK collision errors remain intact for the caller to inspect.
func (backend hatchetBackend) trigger(ctx context.Context, name string, in wire, metadata map[string]string) (string, error) {
	ref, err := backend.client.RunNoWait(ctx, name, in, hatchet.WithRunMetadata(metadata))

	if err != nil {
		return "", nativeFailure(ctx, "trigger run", err)
	}

	return ref.RunId, nil
}

// details reads the physical run and its first task outcome. Invalid or missing
// run identifiers map to the workflow not-found sentinel.
func (backend hatchetBackend) details(ctx context.Context, id string) (runDetail, error) {
	parsed, err := uuid.Parse(id)

	if err != nil {
		return runDetail{}, errNoRun
	}

	got, err := backend.client.Runs().GetDetails(ctx, parsed)

	if err != nil {
		return runDetail{}, nativeFailure(ctx, "get run details", err)
	}

	detail := runDetail{
		Status: string(got.Status),
		Input:  got.Input,
	}

	for _, task := range got.TaskRuns {
		detail.Output = task.Output

		if task.Error != nil {
			detail.Error = *task.Error
		}

		break
	}

	return detail, nil
}

// mark claims a signal marker's idempotency key and returns its physical owner.
// A collision reuses the existing identifier instead of replacing the first
// value.
func (backend hatchetBackend) mark(ctx context.Context, name string, in wire) (string, error) {
	runID, err := backend.trigger(ctx, name, in, nil)

	if err != nil {
		if existing, ok := collisionRun(err); ok && existing != "" {
			return existing, nil
		}

		return "", fmt.Errorf("mark signal: %w", err)
	}

	return runID, nil
}

// cancel requests cancellation of one physical run. Invalid UUIDs and native
// missing-run responses become the workflow not-found sentinel.
func (backend hatchetBackend) cancel(ctx context.Context, id string) error {
	parsed, err := uuid.Parse(id)

	if err != nil {
		return errNoRun
	}

	ids := []uuid.UUID{parsed}

	if _, err := backend.client.Runs().Cancel(ctx, rest.V1CancelTaskRequest{
		ExternalIds: &ids,
	}); err != nil {
		return nativeFailure(ctx, "cancel run", err)
	}

	return nil
}

// push writes the signal envelope to the specified physical execution scope and
// wraps transport failures with operation context.
func (backend hatchetBackend) push(ctx context.Context, key string, payload wire, scope string) error {
	if err := backend.client.Events().Push(ctx, key, payload, hatchet.WithFilterScope(&scope)); err != nil {
		return nativeFailure(ctx, "push event", err)
	}

	return nil
}

// nativeFailure preserves caller cancellation across the SDK's gRPC status
// conversion, maps missing resources to the contract, and retains other causes
// (including idempotency collision types) under operation context.
func nativeFailure(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", op, ctx.Err())
	}

	if isNotFound(err) {
		return fmt.Errorf("%s: %w", op, workflow.ErrNotFound)
	}

	return fmt.Errorf("%s: %w", op, err)
}

// isNotFound reports whether err carries the gRPC code NotFound.
func isNotFound(err error) bool {
	st, ok := status.FromError(err)

	return ok && st.Code() == codes.NotFound
}

// collisionRun returns the id of the existing run when err reports an
// idempotency key collision.
func collisionRun(err error) (string, bool) {
	if sdk, ok := errors.AsType[*hatchet.IdempotencyCollisionError](err); ok {
		return sdk.ExistingRunExternalId, true
	}

	if legacy, ok := errors.AsType[*client.IdempotencyViolationErr](err); ok {
		return legacy.ExistingRunExternalId, true
	}

	return "", false
}
