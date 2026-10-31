// Package diagnostic declares application diagnostics over explicit observations.
package diagnostic

import (
	"context"
	"time"

	"arkoren.dev/core/application/internal/health"
	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

// Owner supplies application observations without importing the public package.
type Owner struct {
	// State observes the current execution phase.
	State func() string
	// Started observes successful startup even after shutdown.
	Started func() StartedView
	// Ready shares the HTTP probe's readiness evaluation.
	Ready func() ReadyView
	// Health owns the check registry and cached background passes.
	Health *health.Monitor
	// Details explicitly permits diagnostic failure text.
	Details bool
}

// reasonWithheld replaces backend errors unless disclosure was approved.
const reasonWithheld = "reason withheld"

// HealthReport is the cached or explicitly refreshed check report.
type HealthReport struct {
	// Status is ok, pending, failing, or stale for an expired cached pass.
	Status string `json:"status"`
	// Items contains checks in registration order, never null.
	Items []HealthCheckView `json:"items"`
}

// HealthCheckView is an intentionally diagnostic check report. Failure text is
// withheld unless the app explicitly enabled HealthDetails.
type HealthCheckView struct {
	// Name identifies the registered check.
	Name string `json:"name"`
	// Status is ok or failing.
	Status string `json:"status"`
	// Error is safe diagnostic failure text, absent on success.
	Error string `json:"error,omitzero"`
	// Since is when the last status transition occurred.
	Since time.Time `json:"since,omitzero"`
	// CheckedAt is when the check last completed.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Age formats the age of CheckedAt.
	Age string `json:"age,omitzero"`
}

// healthInput permits an explicit check refresh instead of passive observation.
type healthInput struct {
	// Fresh runs checks immediately; omission reads only cached results.
	Fresh bool `json:"fresh,omitzero"`
}

// Tools constructs owner declarations without invoking any transport.
func (owner *Owner) Tools(group string, info mcp.Tool, events *Ring) []mcp.Tool {
	health := mcp.NewTool(group+".health", owner.diagnosticHealth,
		func(tool *mcp.ToolBuilder[healthInput, HealthReport]) {
			tool.CLI(group, "health").Role("health").ReadOnly(true).Idempotent(true).OpenWorld(false)
			tool.Description("Health checks: cached readiness state, or an explicit fresh run")
			tool.Input(validate.Struct(func(object *validate.StructBuilder[healthInput]) {
				object.Field("Fresh", func(fresh *validate.BoolBuilder[bool]) {
					fresh.Describe("Run every check now")
				})
			}))
			tool.Example("Run checks now", `{"fresh":true}`)
		})

	tools := []mcp.Tool{info, health,
		conditionTool(owner, group, owner.Started),
		healthConditionTool(owner, group, "healthy", owner.HealthyView),
		readyConditionTool(owner, group),
	}

	if events != nil {
		tools = append(tools, events.tool(group))
	}

	return tools
}

// diagnosticHealth observes the cache unless a refresh is explicitly requested.
// It never probes a dependency simply to distinguish pending from no checks.
func (owner *Owner) diagnosticHealth(ctx context.Context, input healthInput) (HealthReport, error) {
	if !input.Fresh {
		view := owner.HealthyView()

		return HealthReport{Status: view.Status, Items: view.Checks}, nil
	}

	report := HealthReport{Status: diagnostic.StatusOK, Items: []HealthCheckView{}}

	for _, check := range owner.Health.RunChecks(ctx) {
		item := owner.healthCheck(check)

		if check.Err != nil {
			report.Status = diagnostic.StatusFailing
		}

		report.Items = append(report.Items, item)
	}

	return report, nil
}

// healthCheck applies the app's explicit disclosure policy to failure text.
func (owner *Owner) healthCheck(check health.Status) HealthCheckView {
	item := HealthCheckView{Name: check.Name, Status: diagnostic.StatusOK, Since: check.Since,
		CheckedAt: check.CheckedAt, Age: diagnostic.Age(time.Now(), check.CheckedAt)}

	if check.Err != nil {
		item.Status, item.Error = diagnostic.StatusFailing, reasonWithheld

		if owner.Details {
			item.Error = check.Err.Error()
		}
	}

	return item
}
