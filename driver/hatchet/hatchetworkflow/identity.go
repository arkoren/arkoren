package hatchetworkflow

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"arkoren.dev/core/service/workflow"
)

// runIdentity describes the ownership proof required before a native execution
// can be used. Ordinary lookup leaves physical empty to select the newest match;
// trigger responses and marker lookup constrain it to the expected physical ID.
type runIdentity struct {
	// workflow is the exact namespaced engine workflow, not its list filter.
	workflow string
	// key is the encoded logical or signal-marker idempotency key.
	key string
	// logical is the stored application ID; marker inputs deliberately omit it.
	logical string
	// physical optionally constrains the engine execution returned by discovery.
	physical string
}

// find retries missing inventory until deadline, preserving immediate failure
// for a candidate with corrupt or foreign stored input. Past deadlines still
// perform one listing. The last backoff is capped to the remaining grace so a
// final discovery pass can observe its tail. Native request time is additional
// to the wait window. No unverified physical ID enters the caller's cache.
func (driver *Driver) find(ctx context.Context, be backend, identity runIdentity, deadline time.Time) (string, wire, error) {
	delay := resolveStart

	for {
		runs, err := be.list(ctx, identity.workflow, identity.key, time.Now().Add(-driver.config.Lookback))

		if err != nil {
			return "", wire{}, err
		}

		id, input, err := findListedRun(ctx, be, identity, runs)

		if err != nil {
			return "", wire{}, err
		}

		if id != "" {
			return id, input, nil
		}

		remaining := time.Until(deadline)

		if remaining <= 0 {
			return "", wire{}, workflow.ErrNotFound
		}

		if err := sleep(ctx, min(delay, remaining)); err != nil {
			return "", wire{}, err
		}

		delay = min(delay*2, resolveCap)
	}
}

// findListedRun sorts the caller-owned listing in place and verifies candidates
// newest first. Missing native details may be skipped; all other failures stop
// at that candidate, including ErrNotFound from invalid input. Empty ID with nil
// error means no eligible candidate was visible, not that an invalid candidate
// may be ignored or retried.
func findListedRun(ctx context.Context, be backend, identity runIdentity, runs []listedRun) (string, wire, error) {
	slices.SortFunc(runs, func(a, b listedRun) int {
		return b.Created.Compare(a.Created)
	})

	for _, run := range runs {
		if run.ID == "" || run.Workflow != identity.workflow {
			continue
		}

		if identity.physical != "" && run.ID != identity.physical {
			continue
		}

		detail, err := be.details(ctx, run.ID)

		if errors.Is(err, workflow.ErrNotFound) {
			continue
		}

		if err != nil {
			return "", wire{}, err
		}

		input, err := identity.decode(detail.Input)

		if err != nil {
			return "", wire{}, err
		}

		return run.ID, input, nil
	}

	return "", wire{}, nil
}

// decode verifies stored wire ownership without changing payloads or protocol
// versions. Parsing and identity failures preserve the historical not-found
// classification and stop discovery rather than selecting an older execution.
func (identity runIdentity) decode(raw []byte) (wire, error) {
	var input wire

	if err := json.Unmarshal(raw, &input); err != nil {
		return wire{}, fmt.Errorf("verify run input: %w: %w", workflow.ErrNotFound, err)
	}

	if input.Key != identity.key || input.ID != identity.logical {
		return wire{}, fmt.Errorf("run identity mismatch: %w", workflow.ErrNotFound)
	}

	return input, nil
}
