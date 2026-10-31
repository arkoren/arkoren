package diagnostic

import (
	"context"
	"encoding/json/v2"
	"time"

	"arkoren.dev/core/application/internal/health"
	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

const (
	// defaultConditionTimeout bounds waits without an explicit timeout.
	defaultConditionTimeout = 30 * time.Second
	// conditionInterval bounds observation lag without creating a goroutine.
	conditionInterval = 25 * time.Millisecond
)

// conditionInput distinguishes passive observation from bounded waiting.
type conditionInput struct {
	// Wait requests waiting until the condition holds.
	Wait bool `json:"wait,omitzero"`
	// Timeout is a nonnegative duration; zero selects thirty seconds.
	Timeout validate.Duration `json:"timeout,omitzero"`
}

// healthConditionInput adds opt-in failure on a fresh failed background pass.
type healthConditionInput struct {
	// Wait enables server-side waiting.
	Wait bool `json:"wait,omitzero"`
	// Timeout bounds the wait; nonzero requires Wait.
	Timeout validate.Duration `json:"timeout,omitzero"`
	// FailFast stops on fresh failed checks and requires Wait.
	FailFast bool `json:"fail_fast,omitzero"`
}

// StartedView retains successful startup even after the app stops.
type StartedView struct {
	// Started reports successful startup completion.
	Started bool `json:"started"`
	// State reports the current lifecycle phase.
	State string `json:"state"`
	// StartedAt is absent before startup completes.
	StartedAt time.Time `json:"started_at,omitzero"`
}

// HealthyView observes background health without running dependency checks.
type HealthyView struct {
	// Status is ok, pending, failing, or stale.
	Status string `json:"status"`
	// CheckedAt is the last completed background pass.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Checks contains safe check results in registration order, never null.
	Checks []HealthCheckView `json:"checks"`
}

// ReadyView agrees with the owner's readiness probe.
type ReadyView struct {
	// Ready reports whether traffic may be accepted.
	Ready bool `json:"ready"`
	// Reason explains an unsatisfied condition without dependency error text.
	Reason string `json:"reason,omitzero"`
}

// conditionSchema defines shared wait controls for startup.
var conditionSchema = validate.Struct(conditionFields[conditionInput])

// healthConditionSchema adds fail-fast without exposing it on startup tools.
var healthConditionSchema = validate.Struct(func(object *validate.StructBuilder[healthConditionInput]) {
	conditionFields(object)
	object.Field("FailFast", func(failFast *validate.BoolBuilder[bool]) {
		failFast.Describe("Stop on a fresh failed background pass; requires wait")
	})
})

// conditionFields registers common fields on both condition input records.
func conditionFields[In any](object *validate.StructBuilder[In]) {
	object.Field("Wait", func(wait *validate.BoolBuilder[bool]) {
		wait.Describe("Wait until the condition holds; never runs checks")
	})
	object.Field("Timeout", func(timeout *validate.TextBuilder[validate.Duration]) {
		timeout.Use(diagnostic.DurationSchema("Wait limit; requires wait; zero selects 30s"))
	})
}

// conditionTool binds startup observation to its typed MCP input and metadata.
func conditionTool(owner *Owner, group string, observe func() StartedView) mcp.Tool {
	return mcp.NewTool(group+".started", func(ctx context.Context, input conditionInput) (StartedView, error) {
		return awaitCondition(ctx, owner, input, func() (StartedView, bool, error) {
			result := observe()

			return result, result.Started, nil
		})
	}, func(tool *mcp.ToolBuilder[conditionInput, StartedView]) {
		tool.CLI(group, "started").ReadOnly(true).Idempotent(true).OpenWorld(false).Input(conditionSchema)
		tool.Description("Whether startup completed successfully")
		tool.Example("Wait for startup", `{"wait":true,"timeout":"60s"}`)
	})
}

// healthConditionTool owns the health-specific failure policy and metadata.
func healthConditionTool(owner *Owner, group, operation string, observe func() HealthyView) mcp.Tool {
	return mcp.NewTool(group+"."+operation, func(ctx context.Context, input healthConditionInput) (HealthyView, error) {
		return awaitHealthCondition(ctx, owner, input, func() (HealthyView, bool) {
			result := observe()

			return result, result.Status == diagnostic.StatusOK
		})
	}, func(tool *mcp.ToolBuilder[healthConditionInput, HealthyView]) {
		tool.CLI(group, operation).ReadOnly(true).Idempotent(true).OpenWorld(false).Input(healthConditionSchema)
		tool.Description("Whether background health checks passed recently")
		tool.Example("Wait for recovery", `{"wait":true,"timeout":"60s"}`)
		tool.Example("Stop on fresh failure", `{"wait":true,"fail_fast":true}`)
	})
}

// readyConditionTool uses exactly the probe's readiness evaluation.
func readyConditionTool(owner *Owner, group string) mcp.Tool {
	return mcp.NewTool(group+".ready", func(ctx context.Context, input healthConditionInput) (ReadyView, error) {
		return awaitHealthCondition(ctx, owner, input, func() (ReadyView, bool) {
			result := owner.Ready()

			return result, result.Ready
		})
	}, func(tool *mcp.ToolBuilder[healthConditionInput, ReadyView]) {
		tool.CLI(group, "ready").ReadOnly(true).Idempotent(true).OpenWorld(false).Input(healthConditionSchema)
		tool.Description("Whether the application can receive traffic")
		tool.Example("Wait for readiness", `{"wait":true}`)
	})
}

// awaitHealthCondition adds fail-fast without exposing backend errors unless
// the owner explicitly approved health details.
func awaitHealthCondition[Out any](
	ctx context.Context,
	owner *Owner,
	input healthConditionInput,
	observe func() (Out, bool),
) (Out, error) {
	if input.FailFast && !input.Wait {
		result, _ := observe()

		return result, mcp.ToolFailure("invalid_input", "fail_fast requires wait=true", nil)
	}

	wait := conditionInput{Wait: input.Wait, Timeout: input.Timeout}

	return awaitCondition(ctx, owner, wait, func() (Out, bool, error) {
		result, satisfied := observe()

		if !input.FailFast || satisfied {
			return result, satisfied, nil
		}

		return result, satisfied, owner.conditionFailure(result)
	})
}

// conditionFailure returns a safe typed failure only for a fresh failed pass.
// Details carry the exact operation result and check DTOs, never raw errors.
func (owner *Owner) conditionFailure(result any) error {
	health := owner.HealthyView()

	if health.Status != diagnostic.StatusFailing {
		return nil
	}

	details, err := json.Marshal(struct {
		// Result is the exact operation-specific observation that failed.
		Result any `json:"result"`

		// Checks are check records filtered through the owner's disclosure policy.
		Checks []HealthCheckView `json:"checks"`
	}{Result: result, Checks: health.Checks})

	if err != nil {
		return err
	}

	return mcp.ToolFailure("condition_failed", "Application health check failed", details)
}

// conditionError serializes a safe last observation for timeout or terminal
// lifecycle failures. The fixed message never includes a dependency error.
func conditionError(code, message string, result any) error {
	details, err := json.Marshal(struct {
		// Result preserves the final safe observation for the caller's diagnosis.
		Result any `json:"result"`
	}{Result: result})

	if err != nil {
		return err
	}

	return mcp.ToolFailure(code, message, details)
}

// awaitCondition owns one fixed timeout and one ticker. It reads lifecycle
// before observing so startup completion cannot be lost at a terminal phase.
// Its observer returns both the value and satisfaction from one observation.
func awaitCondition[Out any](
	ctx context.Context,
	owner *Owner,
	input conditionInput,
	observe func() (Out, bool, error),
) (Out, error) {
	var zero Out

	if !input.Wait && input.Timeout != 0 {
		return zero, mcp.ToolFailure("invalid_input", "timeout requires wait=true", nil)
	}

	if !input.Wait {
		result, _, err := observe()

		return result, err
	}

	timeout := input.Timeout.Std()

	if timeout == 0 {
		timeout = defaultConditionTimeout
	}

	return waitCondition(ctx, owner, timeout, observe)
}

// waitCondition performs passive observations until success or one bounded
// exit. A final observation is always taken when the deadline fires.
func waitCondition[Out any](
	ctx context.Context,
	owner *Owner,
	timeout time.Duration,
	observe func() (Out, bool, error),
) (Out, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(conditionInterval)
	defer ticker.Stop()
	expired := false

	for {
		state := owner.State()

		result, satisfied, err := observe()

		if err != nil || satisfied {
			return result, err
		}

		switch state {
		case "failed", "stopping", "stopped":
			return result, conditionError("condition_stopped", "Application wait cannot complete", result)
		}

		if expired {
			return result, conditionError("condition_timeout", "Application wait timed out", result)
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-timer.C:
			expired = true
		case <-ticker.C:
		}
	}
}

// HealthyView distinguishes absent checks from an uncompleted first pass and
// applies the same error-disclosure policy as explicit diagnostics.
func (owner *Owner) HealthyView() HealthyView {
	snapshot := owner.Health.Snapshot()
	report := HealthyView{Status: snapshot.HealthStatus(owner.Health.MaxAge()),
		CheckedAt: snapshot.CompletedAt, Checks: make([]HealthCheckView, 0, len(snapshot.Results))}

	for _, result := range snapshot.Results {
		report.Checks = append(report.Checks, owner.healthCheck(health.Status{
			Name: result.Name, Err: result.Err, Since: result.ChangedAt, CheckedAt: snapshot.CompletedAt,
		}))
	}

	return report
}
