package schedule_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/memoryschedule"
	"arkoren.dev/foundation/validate"
)

// schedulerWith registers a calendar entry while leaving the scheduler stopped.
// Manual tools can execute entries without starting the calendar driver.
func schedulerWith(t *testing.T, name string, fn func(context.Context) error) *schedule.Scheduler {
	t.Helper()

	scheduler := schedule.New(memoryschedule.New())

	if err := scheduler.Add(schedule.NewEntry(name, fn, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Cron("0 3 * * *"))
	})); err != nil {
		t.Fatal(err)
	}

	return scheduler
}

// callTool decodes one direct MCP call into its expected result shape.
// It fails the test on invalid definitions, calls, or output JSON.
func callTool[T any](t *testing.T, tool mcp.Tool, input string) T {
	t.Helper()

	body, err := tool.Call(context.Background(), []byte(input))

	if err != nil {
		t.Fatal(err)
	}

	var result T

	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	return result
}

// TestMCPToolsMetadata checks native names, CLI grouping, and read/action hints.
func TestMCPToolsMetadata(t *testing.T) {
	t.Parallel()

	tools := schedule.New(memoryschedule.New()).MCPTools("scheduler")

	if len(tools) != 2 {
		t.Fatalf("tools = %v", tools)
	}

	read, action := tools[0].Describe(), tools[1].Describe()

	if read.Name != "scheduler.entries" || action.Name != "scheduler.run" {
		t.Fatalf("names = %s, %s", read.Name, action.Name)
	}

	if read.CLI.Group != "scheduler" || action.CLI.Positional[0] != "name" {
		t.Fatalf("CLI = %+v, %+v", read.CLI, action.CLI)
	}

	if !read.Annotations.ReadOnly || read.Annotations.OpenWorld || action.Annotations.ReadOnly {
		t.Fatalf("annotations = %+v, %+v", read.Annotations, action.Annotations)
	}
}

// TestMCPToolsDefaultNames checks the intentionally unprefixed native names.
func TestMCPToolsDefaultNames(t *testing.T) {
	t.Parallel()

	tools := schedule.New(memoryschedule.New()).MCPTools("")

	if tools[0].Name() != "entries" || tools[1].Name() != "run" || tools[0].Describe().CLI.Group != "schedule" {
		t.Fatalf("tools = %+v", tools)
	}
}

// TestMCPToolsRejectInvalidPrefix checks final tool-name validation.
func TestMCPToolsRejectInvalidPrefix(t *testing.T) {
	t.Parallel()

	for _, tool := range schedule.New(memoryschedule.New()).MCPTools("Bad Name") {
		if !errors.Is(tool.Err(), mcp.ErrInvalidTool) {
			t.Fatalf("err = %v", tool.Err())
		}
	}
}

// TestMCPEmptyEntriesReturnsArray checks that empty structured output remains
// an object containing an array, not null or a bare top-level list.
func TestMCPEmptyEntriesReturnsArray(t *testing.T) {
	t.Parallel()

	body := callTool[map[string]jsontext.Value](t, schedule.New(memoryschedule.New()).MCPTools("")[0], `{}`)

	if string(body["items"]) != "[]" || string(body["truncated"]) != "false" {
		t.Fatalf("body = %s", body)
	}
}

// TestMCPEntriesLiveValues checks paired metadata and late registrations.
func TestMCPEntriesLiveValues(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		scheduler := schedule.New(memoryschedule.New())
		tool := scheduler.MCPTools("jobs")[0]

		if err := scheduler.Add(schedule.NewEntry("report", func(context.Context) error {
			return nil
		}, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Cron("*/5 * * * *"))
			entry.Description("builds the report")
			entry.Timeout(90 * time.Second)
			entry.WithoutOverlapping()
		})); err != nil {
			t.Fatal(err)
		}

		body := callTool[map[string]jsontext.Value](t, tool, `{}`)
		var items []map[string]any

		if err := json.Unmarshal(body["items"], &items); err != nil {
			t.Fatal(err)
		}

		if len(items) != 1 || items[0]["next_in"] != "in 5m" {
			t.Fatalf("items = %+v", items)
		}

		if items[0]["timeout"] != "1m30s" || items[0]["without_overlapping"] != true {
			t.Fatalf("settings = %+v", items[0])
		}

		if items[0]["name"] != "report" || items[0]["description"] != "builds the report" {
			t.Fatalf("identity = %+v", items[0])
		}
	})
}

// TestMCPRunWaitsForCompletion checks one execution and paired duration values.
func TestMCPRunWaitsForCompletion(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ran := 0
		scheduler := schedulerWith(t, "cleanup", func(context.Context) error {
			ran++
			time.Sleep(1500 * time.Millisecond)

			return nil
		})

		body := callTool[struct {
			Name       string            `json:"name"`
			DurationMS float64           `json:"duration_ms"`
			Duration   validate.Duration `json:"duration"`
		}](t, scheduler.MCPTools("jobs")[1], `{"name":"cleanup"}`)

		if ran != 1 || body.Name != "cleanup" || body.DurationMS != 1500 || body.Duration.Std() != 1500*time.Millisecond {
			t.Fatalf("ran %d, result %+v", ran, body)
		}
	})
}

// TestMCPRunUnknownName checks safe domain failures independently of transport.
func TestMCPRunUnknownName(t *testing.T) {
	t.Parallel()

	tool := schedule.New(memoryschedule.New()).MCPTools("jobs")[1]

	_, err := tool.Call(context.Background(), []byte(`{"name":"nope"}`))
	failure, ok := mcp.ToolFailureInfo(err)

	if !ok || failure.Code != "not_found" || !strings.Contains(failure.Message, "nope") {
		t.Fatalf("failure = %+v, err = %v", failure, err)
	}

	_, err = tool.Call(context.Background(), []byte(`{}`))

	if !errors.Is(err, mcp.ErrInvalidInput) {
		t.Fatalf("missing name: %v", err)
	}
}

// TestMCPRunRetainsFailureWithoutRetry checks that private errors retain their
// identity for adapters to redact and that execution happens only once.
func TestMCPRunRetainsFailureWithoutRetry(t *testing.T) {
	t.Parallel()

	failure := errors.New("disk full")
	ran := 0
	scheduler := schedulerWith(t, "flaky", func(context.Context) error {
		ran++

		return failure
	})

	_, err := scheduler.MCPTools("jobs")[1].Call(context.Background(), []byte(`{"name":"flaky"}`))

	if !errors.Is(err, failure) || ran != 1 {
		t.Fatalf("ran %d, err = %v", ran, err)
	}
}

// TestMCPRunAvailabilityDeniesBeforeDecode checks application-owned capability
// policy without introducing a scheduler-specific authorization mechanism.
func TestMCPRunAvailabilityDeniesBeforeDecode(t *testing.T) {
	t.Parallel()

	ran := 0
	scheduler := schedulerWith(t, "cleanup", func(context.Context) error {
		ran++

		return nil
	})

	tool := scheduler.MCPTools("jobs")[1].WithAvailability(func(context.Context) (bool, error) {
		return false, nil
	})

	_, err := tool.Call(context.Background(), []byte(`not json`))

	if !errors.Is(err, mcp.ErrUnavailable) || ran != 0 {
		t.Fatalf("ran %d, err = %v", ran, err)
	}
}

// TestMCPRunPreservesNestedUnknownTask verifies that lookup classification
// does not turn an existing entry's application failure into a safe not-found.
func TestMCPRunPreservesNestedUnknownTask(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(memoryschedule.New())
	missing := schedule.NewTask[string]("missing-task")

	if err := scheduler.Add(schedule.NewEntry("report", func(ctx context.Context) error {
		_, err := scheduler.At(ctx, missing, "payload", time.Now())

		return err
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	})); err != nil {
		t.Fatal(err)
	}

	_, err := scheduler.MCPTools("jobs")[1].Call(t.Context(), []byte(`{"name":"report"}`))

	if !errors.Is(err, schedule.ErrUnknownName) {
		t.Fatalf("execution cause lost: %v", err)
	}

	if failure, ok := mcp.ToolFailureInfo(err); ok {
		t.Fatalf("application error became safe failure: %+v", failure)
	}
}

// TestMCPRunHonorsEntryTimeout checks that the tool waits for the timed-out
// handler and returns its cancellation error rather than bypassing RunNow.
func TestMCPRunHonorsEntryTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		scheduler := schedule.New(memoryschedule.New())

		if err := scheduler.Add(schedule.NewEntry("slow", func(ctx context.Context) error {
			<-ctx.Done()

			return ctx.Err()
		}, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Every(time.Minute))
			entry.Timeout(time.Second)
		})); err != nil {
			t.Fatal(err)
		}

		_, err := scheduler.MCPTools("")[1].Call(context.Background(), []byte(`{"name":"slow"}`))

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestMCPRunHonorsCallerCancellation checks that execution receives the call
// context without starting a detached background operation.
func TestMCPRunHonorsCallerCancellation(t *testing.T) {
	t.Parallel()

	scheduler := schedulerWith(t, "cleanup", func(ctx context.Context) error {
		return ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := scheduler.MCPTools("")[1].Call(ctx, []byte(`{"name":"cleanup"}`))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
