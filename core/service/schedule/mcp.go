package schedule

import (
	"context"
	"time"

	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

// entryView reports calendar configuration without exposing execution functions.
type entryView struct {
	// Name is the entry name.
	Name string `json:"name"`

	// Spec is the entry's cron expression.
	Spec string `json:"spec"`

	// Description is the entry's description; may be empty.
	Description string `json:"description,omitzero"`

	// Timeout is the per-run timeout; omitted when there is none.
	Timeout validate.Duration `json:"timeout,omitzero"`

	// WithoutOverlapping reports that a run is skipped while the previous
	// one is still running.
	WithoutOverlapping bool `json:"without_overlapping"`

	// NextRun is the next scheduled time; omitted when the entry never runs
	// again.
	NextRun time.Time `json:"next_run,omitzero"`

	// NextIn is how long after the response NextRun is, such as "in 3m".
	NextIn string `json:"next_in,omitzero"`
}

// runView reports one completed manual execution without its application data.
type runView struct {
	// Name is the entry that ran.
	Name string `json:"name"`

	// StartedAt is when the run started.
	StartedAt time.Time `json:"started_at"`

	// DurationMS is how long the run took in milliseconds.
	DurationMS float64 `json:"duration_ms"`

	// Duration is DurationMS as text, such as "182.1ms".
	Duration validate.Duration `json:"duration"`
}

// runInput is the input of the run operation.
type runInput struct {
	// Name selects the registered calendar entry for manual execution.
	Name string `json:"name"`
}

// entriesView preserves an object-shaped list even when no entries exist.
// Entries are returned in registration order without a truncation limit.
type entriesView struct {
	// Items contains registered calendar entries and is never null.
	Items []entryView `json:"items"`
	// Truncated is false because all registered entries are returned.
	Truncated bool `json:"truncated"`
}

// MCPTools returns live entries and manual-run tools for explicit registration.
// A nonempty prefix produces prefix.entries and prefix.run in that CLI group;
// an empty prefix produces entries and run in the schedule CLI group. Tool Err
// reports invalid names. No authentication, policy, or registration is implicit.
//
// entries only reads configuration. run executes application code once and
// waits through RunNow, preserving cancellation and entry timeouts. As with
// RunNow, driver-owned overlap guards do not apply to manual execution.
// Callers must not automatically retry it. Unknown names return a safe
// not_found ToolFailure; other errors retain their identity for adapter redaction.
// The caller must protect this capability with its own availability policy.
//
// Example:
//
//	tools := scheduler.MCPTools("scheduler")
//	if err := tools[0].Err(); err != nil {
//		return err
//	}
//	result, err := tools[0].Call(ctx, []byte(`{}`))
//	if err != nil {
//		return err
//	}
//	fmt.Println(string(result))
func (scheduler *Scheduler) MCPTools(prefix string) []mcp.Tool {
	group, stem := "schedule", ""

	if prefix != "" {
		group, stem = prefix, prefix+"."
	}

	entries := mcp.NewTool(stem+"entries", func(context.Context, mcp.NoInput) (entriesView, error) {
		now := time.Now()
		items := []entryView{}

		for _, entry := range scheduler.Entries() {
			next := entry.Next(now)
			items = append(items, entryView{
				Name:               entry.Name(),
				Spec:               entry.Spec().Expression(),
				Description:        entry.DescriptionValue(),
				Timeout:            validate.Duration(entry.TimeoutValue()),
				WithoutOverlapping: !entry.Overlapping(),
				NextRun:            next,
				NextIn:             diagnostic.Age(now, next),
			})
		}

		return entriesView{
			Items: items,
		}, nil
	}, func(read *mcp.ToolBuilder[mcp.NoInput, entriesView]) {
		read.Description("Scheduled entries with their next run")
		read.CLI(group, "entries").GroupSummary("Scheduled entries")
		read.ReadOnly(true).Idempotent(true).OpenWorld(false)
	})

	run := mcp.NewTool(stem+"run", scheduler.runTool, func(action *mcp.ToolBuilder[runInput, runView]) {
		action.Description("Run a scheduled entry once and wait for completion; do not automatically retry")
		action.CLI(group, "run").GroupSummary("Scheduled entries")
		action.ReadOnly(false).Idempotent(false)
		action.Input(validate.Struct(func(object *validate.StructBuilder[runInput]) {
			object.Field("Name", func(name *validate.StringBuilder[string]) {
				name.Required()
				name.Describe("Name of the schedule entry")
			})
		}))
		action.Positional("name")
	})

	return []mcp.Tool{entries, run}
}

// runTool delegates to the same execution path as calendar dispatch. Only the
// unknown-name error is translated to a client-safe failure; job errors remain
// internal and retain their original identity.
func (scheduler *Scheduler) runTool(ctx context.Context, input runInput) (runView, error) {
	started := time.Now()

	found, err := scheduler.runNow(ctx, input.Name)

	if !found {
		return runView{}, mcp.ToolFailure("not_found", "no schedule entry named "+input.Name, nil)
	}

	if err != nil {
		return runView{}, err
	}

	elapsed := time.Since(started)

	return runView{
		Name:       input.Name,
		StartedAt:  started,
		DurationMS: diagnostic.Millis(elapsed),
		Duration:   diagnostic.DurationText(elapsed),
	}, nil
}
