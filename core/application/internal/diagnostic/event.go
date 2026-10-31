package diagnostic

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"path"
	"sync"
	"time"

	"arkoren.dev/core/internal/diagnostic"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/validate"
)

// defaultEventLimit bounds a history snapshot when limit is omitted.
const defaultEventLimit = 100

// EventView retains metadata only. Payload values never enter capture memory.
type EventView struct {
	// ReceivedAt is when capture received the event; the wire name stays time.
	ReceivedAt time.Time `json:"time"`
	// Age is human-readable time since receipt.
	Age string `json:"age"`
	// Name identifies the event topic.
	Name string `json:"name"`
	// PayloadType identifies the Go payload type without retaining its value.
	PayloadType string `json:"payload_type"`
	// TraceID correlates dispatch with a span, when one was present.
	TraceID string `json:"trace_id,omitzero"`
}

// eventsInput combines owner filters with bounded cursor controls.
type eventsInput struct {
	// Name is a path.Match glob; empty matches all topics.
	Name string `json:"name,omitzero"`
	// Since limits event age relative to the beginning of this call.
	Since validate.Duration `json:"since,omitzero"`
	// Limit bounds returned items, at most 1000.
	Limit int `json:"limit,omitzero"`
	// Cursor selects history, a tail anchor, or continuation.
	Cursor string `json:"cursor,omitzero"`
	// Wait bounds an empty continuation call, at most 25 seconds.
	Wait validate.Duration `json:"wait,omitzero"`
}

// eventsSchema validates all controls before the handler reads capture state.
var eventsSchema = validate.Struct(func(object *validate.StructBuilder[eventsInput]) {
	object.Field("Name", func(name *validate.StringBuilder[string]) {
		name.Describe("Event name glob, such as billing.*")
		name.Check(func(_ context.Context, glob string) error {
			if _, err := path.Match(glob, ""); err != nil {
				return validate.Fail("string.regex", nil)
			}

			return nil
		})
	})
	object.Field("Since", func(since *validate.TextBuilder[validate.Duration]) {
		since.Use(diagnostic.DurationSchema("Maximum event age, such as 30s"))
	})
	object.Field("Limit", func(limit *validate.IntBuilder[int]) {
		limit.Use(diagnostic.LimitSchema(defaultEventLimit))
	})
	object.Field("Cursor", func(cursor *validate.StringBuilder[string]) {
		cursor.Describe("Opaque continuation cursor; tail anchors without history")
	})
	object.Field("Wait", func(wait *validate.TextBuilder[validate.Duration]) {
		wait.Use(diagnostic.WaitSchema())
	})
})

// Ring retains bounded metadata and atomically pairs each snapshot with
// its high-water mark and notification channel.
type Ring struct {
	// mu guards every field below.
	mu sync.Mutex
	// buffer stores retained events in circular order.
	buffer []EventView
	// start is the oldest retained index after wraparound.
	start int
	// total counts every received event, including overwritten records.
	total uint64
	// instance uniquely binds cursors to this ring.
	instance string
	// changed closes and is replaced after each write.
	changed chan struct{}
}

// NewRing creates positive-capacity capture with no event payload storage.
func NewRing(size int) *Ring {
	return &Ring{
		buffer:   make([]EventView, 0, size),
		instance: diagnostic.NewInstance(),
		changed:  make(chan struct{}),
	}
}

// add stores metadata and wakes all bounded pollers without running callbacks.
func (ring *Ring) add(event EventView) {
	ring.mu.Lock()
	defer ring.mu.Unlock()

	if len(ring.buffer) < cap(ring.buffer) {
		ring.buffer = append(ring.buffer, event)
	} else {
		ring.buffer[ring.start] = event
		ring.start = (ring.start + 1) % len(ring.buffer)
	}

	ring.total++
	close(ring.changed)

	ring.changed = make(chan struct{})
}

// snapshot returns an atomic copied observation, oldest first. A writer after
// this observation must close the returned channel, preventing missed wakeups.
func (ring *Ring) snapshot() diagnostic.Snapshot[EventView] {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	items := make([]EventView, 0, len(ring.buffer))
	items = append(items, ring.buffer[ring.start:]...)
	items = append(items, ring.buffer[:ring.start]...)

	return diagnostic.Snapshot[EventView]{
		Items:    items,
		Total:    ring.total,
		Instance: ring.instance,
		Changed:  ring.changed,
	}
}

// Record implements the event listener. Its metadata-only observation cannot
// change dispatch outcomes and never retains the payload argument.
func (ring *Ring) Record(ctx context.Context, name string, payload any) error {
	item := EventView{ReceivedAt: time.Now(), Name: name, PayloadType: fmt.Sprintf("%T", payload)}

	if traceID := telemetry.SpanFromContext(ctx).Context().TraceID; !traceID.IsZero() {
		item.TraceID = traceID.String()
	}

	ring.add(item)

	return nil
}

// tool publishes bounded event polling with CLI-discoverable cursor metadata.
func (ring *Ring) tool(group string) mcp.Tool {
	name := group + ".events"

	return mcp.NewTool(name, func(ctx context.Context, input eventsInput) (diagnostic.Page[EventView], error) {
		filters, err := json.Marshal(struct {
			// Name binds continuation to this exact event-name filter.
			Name string

			// Since binds continuation to this relative age restriction.
			Since validate.Duration
		}{input.Name, input.Since})

		if err != nil {
			return diagnostic.Page[EventView]{}, err
		}

		cutoffAt := time.Now().Add(-input.Since.Std())
		controls := diagnostic.CursorRequest{
			Key:    name + string(filters),
			Cursor: input.Cursor,
			Wait:   input.Wait.Std(),
			Limit:  input.Limit,
		}

		return diagnostic.Poll(ctx, controls,
			diagnostic.Feed[EventView, EventView]{Snapshot: ring.snapshot,
				Match: func(item EventView) bool {
					matched, _ := path.Match(input.Name, item.Name) // Schema already checked the glob.
					nameMatches := input.Name == "" || matched
					ageMatches := input.Since == 0 || !item.ReceivedAt.Before(cutoffAt)

					return nameMatches && ageMatches
				},
				Render: func(item EventView) EventView {
					item.Age = diagnostic.Age(time.Now(), item.ReceivedAt)

					return item
				},
			})
	}, func(tool *mcp.ToolBuilder[eventsInput, diagnostic.Page[EventView]]) {
		tool.CLI(group, "events").Cursor(true).ReadOnly(true).Idempotent(true).OpenWorld(false).Input(eventsSchema)
		tool.Description("Recent event names, payload types, and trace IDs; never payload values")
		tool.Positional("name").Example("Billing events", `{"name":"billing.*","since":"1m"}`)
	})
}
