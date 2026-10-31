package hatchetschedule

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/hatchet-dev/hatchet/sdks/go/features"

	"arkoren.dev/core/service/schedule"
)

// taskInput is the input of all three registered tasks. New deferred jobs
// use PayloadJSONV1 so Hatchet's generic maps never parse business numbers.
// Payload is read only for pending legacy jobs; its numbers may already have
// been rounded by the engine. Recurring entries supply only Name.
type taskInput struct {
	// Name selects the entry or typed task in the scheduler's dispatch table.
	Name string `json:"name"`

	// Payload holds legacy raw JSON, including JSON strings as business data.
	// It must not coexist with PayloadJSONV1, even when either value is null.
	Payload jsontext.Value `json:"payload,omitzero"`

	// PayloadJSONV1 holds a JSON string containing the complete business JSON.
	// Raw storage distinguishes absence from null and other invalid encodings.
	// The versioned field name avoids interpreting legacy strings as envelopes.
	PayloadJSONV1 jsontext.Value `json:"payload_json_v1,omitzero"`
}

// taskOutput is the JSON output of every Hatchet task this driver registers.
type taskOutput struct {
	// Name echoes the application entry name after dispatch, including a skipped
	// overlapping run, so the engine output identifies the handled job.
	Name string `json:"name"`
}

// handle decodes the opaque v1 payload, or passes legacy JSON through without
// interpreting legacy strings as envelopes. Mixed fields, non-string v1 data,
// and invalid enclosed JSON fail before dispatch. Assignments received before
// Run wait for readiness or native cancellation, without losing queued work.
func (driver *Driver) handle(ctx context.Context, input taskInput) (taskOutput, error) {
	dispatch, err := driver.dispatchReady(ctx)

	if err != nil {
		return taskOutput{}, fmt.Errorf("dispatch %q: %w", input.Name, err)
	}

	defer driver.calls.Done()

	// payload is restored only at dispatch, after all SDK generic-map decodes.
	payload := []byte(input.Payload)

	if len(input.PayloadJSONV1) > 0 {
		if len(input.Payload) > 0 || input.PayloadJSONV1.Kind() != jsontext.KindString {
			return taskOutput{}, fmt.Errorf("dispatch %q: invalid payload encoding", input.Name)
		}

		var encoded string

		if err := json.Unmarshal(input.PayloadJSONV1, &encoded); err != nil {
			return taskOutput{}, fmt.Errorf("dispatch %q: decode payload encoding: %w", input.Name, err)
		}

		payload = []byte(encoded)

		if !jsontext.Value(payload).IsValid() {
			return taskOutput{}, fmt.Errorf("dispatch %q: invalid payload JSON", input.Name)
		}
	}

	if err := dispatch(ctx, input.Name, payload); err != nil {
		if errors.Is(err, schedule.ErrSkipped) {
			return taskOutput{
				Name: input.Name,
			}, nil
		}

		return taskOutput{}, fmt.Errorf("dispatch %q: %w", input.Name, err)
	}

	return taskOutput{
		Name: input.Name,
	}, nil
}

// Defer creates a Hatchet scheduled run for job and returns its identifier.
// Nonempty Payload must be valid JSON. It is carried as an opaque JSON string
// in payload_json_v1 so the engine and SDK preserve integer precision, nested
// values, and JSON string payloads. Empty payloads remain absent. Upgrade all
// workers before producers; old workers do not understand the new field.
//
// Example:
//
//	id, err := driver.Defer(ctx, schedule.Job{
//		Name: "remind", Payload: []byte(`{"id":9007199254740993}`), At: when,
//	})
func (driver *Driver) Defer(ctx context.Context, job schedule.Job) (string, error) {
	_, schedules, err := driver.clients()

	if err != nil {
		return "", fmt.Errorf("defer %q: %w", job.Name, err)
	}

	// input exposes only strings to Hatchet, never nested business JSON.
	input := map[string]any{"name": job.Name}

	if len(job.Payload) > 0 {
		if !jsontext.Value(job.Payload).IsValid() {
			return "", fmt.Errorf("defer %q: invalid payload JSON", job.Name)
		}

		input["payload_json_v1"] = string(job.Payload)
	}

	run, err := schedules.Create(ctx, driver.deferredWorkflow(), features.CreateScheduledRunTrigger{
		TriggerAt: job.At.UTC(),
		Input:     input,
	})

	if err != nil {
		return "", fmt.Errorf("defer %q: %w", job.Name, err)
	}

	if run == nil {
		return "", fmt.Errorf("defer %q: empty response", job.Name)
	}

	return run.Metadata.Id, nil
}
