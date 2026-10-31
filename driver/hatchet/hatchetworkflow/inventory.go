package hatchetworkflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/hatchet-dev/hatchet/pkg/client/rest"

	"arkoren.dev/core/service/workflow"
)

// exists accepts only the exact engine-owned name, including its namespace.
// The server's name query can return partial matches, which are not ownership.
func (backend hatchetBackend) exists(ctx context.Context, name string) (bool, error) {
	_, err := backend.workflowID(ctx, name)

	if errors.Is(err, workflow.ErrNotFound) {
		return false, nil
	}

	return err == nil, err
}

// workflowID pages through partial native name matches until it finds the exact
// namespaced workflow. Both existence checks and execution listings use this
// resolution, so a valid registration cannot disappear beyond the first page.
func (backend hatchetBackend) workflowID(ctx context.Context, name string) (uuid.UUID, error) {
	// limit is the SDK workflow-list page size, including partial name matches.
	const limit = 50
	identity := ""

	for offset := 0; ; offset += limit {
		page, err := backend.client.Workflows().List(ctx, &rest.WorkflowListParams{
			Name: &name, Limit: new(limit), Offset: &offset,
		})

		if err != nil {
			return uuid.Nil, fmt.Errorf("find workflow: %w", err)
		}

		if page.Rows == nil {
			return uuid.Nil, workflow.ErrNotFound
		}

		index := slices.IndexFunc(*page.Rows, func(row rest.Workflow) bool {
			return row.Name == name
		})

		if index >= 0 {
			identity = (*page.Rows)[index].Metadata.Id
			break
		}

		if len(*page.Rows) < limit {
			return uuid.Nil, workflow.ErrNotFound
		}
	}

	id, err := uuid.Parse(identity)

	if err != nil {
		return uuid.Nil, fmt.Errorf("decode workflow identity: %w", err)
	}

	return id, nil
}

// list resolves the exact workflow name to its engine UUID before listing
// runs. The SDK's run list does not apply its client namespace automatically.
func (backend hatchetBackend) list(ctx context.Context, name, key string, since time.Time) ([]listedRun, error) {
	id, err := backend.workflowID(ctx, name)

	if err != nil {
		return nil, fmt.Errorf("find workflow for run listing: %w", err)
	}

	ids := []uuid.UUID{id}
	keys := []string{key}
	limit := int64(50)
	payloads := false

	rows, err := backend.client.Runs().List(ctx, rest.V1WorkflowRunListParams{
		Since:           since,
		Limit:           &limit,
		IdempotencyKeys: &keys,
		WorkflowIds:     &ids,
		IncludePayloads: &payloads,
	})

	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	runs := make([]listedRun, 0, len(rows.Rows))

	for _, row := range rows.Rows {
		id := row.WorkflowRunExternalId

		if id == uuid.Nil {
			id = row.TaskExternalId
		}

		name := ""

		if row.WorkflowName != nil {
			name = *row.WorkflowName
		}

		runs = append(runs, listedRun{
			ID:       id.String(),
			Created:  row.CreatedAt,
			Workflow: name,
		})
	}

	return runs, nil
}
